package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/adminauth"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const testAdminPassword = "correct-horse-battery-staple"

func TestOpenComposesAuthenticatedVNextStackAndSPA(t *testing.T) {
	options := testRuntimeOptions(t)
	runtime, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Errorf("close runtime: %v", err)
		}
	})

	for _, name := range []string{DefaultDatabaseName, "jieshan-secret.key"} {
		info, err := os.Stat(filepath.Join(options.DataDir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("%s is not a regular file", name)
		}
	}

	for _, path := range []string{"/", "/models", "/sites/42"} {
		response := serve(runtime, http.MethodGet, path, nil, nil)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "JieShan test app") {
			t.Fatalf("SPA %s status = %d, body = %s", path, response.Code, response.Body.String())
		}
	}

	health := serve(runtime, http.MethodGet, HealthPath, nil, nil)
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d, body = %s", health.Code, health.Body.String())
	}
	var healthBody map[string]string
	if err := json.Unmarshal(health.Body.Bytes(), &healthBody); err != nil {
		t.Fatal(err)
	}
	if healthBody["status"] != "ok" || healthBody["stack"] != "jieshan" {
		t.Fatalf("unexpected health body: %#v", healthBody)
	}

	status := serve(runtime, http.MethodGet, AuthPrefix+"/status", nil, nil)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"authenticated":false`) {
		t.Fatalf("auth status = %d, body = %s", status.Code, status.Body.String())
	}
	unauthorized := serve(runtime, http.MethodGet, InventoryAdminPrefix+"/sites", nil, nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unprotected inventory status = %d, want 401", unauthorized.Code)
	}
	unauthorizedRouting := serve(runtime, http.MethodGet, RoutingProfilesAdminPrefix, nil, nil)
	if unauthorizedRouting.Code != http.StatusUnauthorized {
		t.Fatalf("unprotected routing status = %d, want 401", unauthorizedRouting.Code)
	}

	adminHeaders := login(t, runtime, testAdminPassword)
	for name, path := range map[string]string{
		"inventory":     InventoryAdminPrefix + "/sites",
		"routing":       RoutingProfilesAdminPrefix,
		"site accounts": SiteAccountsAdminPrefix,
		"pricing":       PricingAdminPrefix + "/state",
		"request logs":  RequestLogsAdminPrefix,
		"monitor":       MonitorAdminPrefix,
		"settings":      SettingsAdminPrefix,
	} {
		response := serve(runtime, http.MethodGet, path, nil, adminHeaders)
		if response.Code != http.StatusOK {
			t.Fatalf("protected %s status = %d, body = %s", name, response.Code, response.Body.String())
		}
	}
	routingHeaders := cloneHeaders(adminHeaders)
	routingHeaders["Content-Type"] = "application/json"
	routingHeaders["If-Match"] = "*"
	missingRoutingCSRF := cloneHeaders(routingHeaders)
	delete(missingRoutingCSRF, adminauth.CSRFHeaderName)
	createRoutingProfile := []byte(`{"name":"Runtime contract"}`)
	blockedRoutingCreate := serve(runtime, http.MethodPost, RoutingProfilesAdminPrefix, createRoutingProfile, missingRoutingCSRF)
	if blockedRoutingCreate.Code != http.StatusForbidden {
		t.Fatalf("routing POST without CSRF status = %d, body = %s", blockedRoutingCreate.Code, blockedRoutingCreate.Body.String())
	}
	createdRoutingProfile := serve(runtime, http.MethodPost, RoutingProfilesAdminPrefix, createRoutingProfile, routingHeaders)
	if createdRoutingProfile.Code != http.StatusCreated || createdRoutingProfile.Header().Get("ETag") != `"1"` {
		t.Fatalf("routing POST = %d %q %s", createdRoutingProfile.Code, createdRoutingProfile.Header().Get("ETag"), createdRoutingProfile.Body.String())
	}
	settingsBody := []byte(`{"failureThreshold":3,"failureWindowMs":300000,"cooldownMs":120000,"probeIntervalMs":180000,"firstOutputTimeoutMs":15000,"streamIdleTimeoutMs":45000,"requestTimeoutMs":240000,"maxAttempts":6,"logRetentionDays":45}`)
	missingCSRFHeaders := cloneHeaders(adminHeaders)
	delete(missingCSRFHeaders, adminauth.CSRFHeaderName)
	missingCSRFHeaders["Content-Type"] = "application/json"
	missingCSRFHeaders["If-Match"] = `"1"`
	missingCSRF := serve(runtime, http.MethodPatch, SettingsAdminPrefix, settingsBody, missingCSRFHeaders)
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("settings PATCH without CSRF status = %d, body = %s", missingCSRF.Code, missingCSRF.Body.String())
	}
	settingsHeaders := cloneHeaders(adminHeaders)
	settingsHeaders["Content-Type"] = "application/json"
	settingsHeaders["If-Match"] = `"1"`
	updatedSettings := serve(runtime, http.MethodPatch, SettingsAdminPrefix, settingsBody, settingsHeaders)
	if updatedSettings.Code != http.StatusOK || updatedSettings.Header().Get("ETag") != `"2"` ||
		!strings.Contains(updatedSettings.Body.String(), `"probeIntervalMs":180000`) {
		t.Fatalf("settings PATCH = %d %q %s", updatedSettings.Code, updatedSettings.Header().Get("ETag"), updatedSettings.Body.String())
	}

	createHeaders := cloneHeaders(adminHeaders)
	createHeaders["Content-Type"] = "application/json"
	create := serve(runtime, http.MethodPost, DownstreamKeysAdminPrefix, []byte(`{"name":"runtime smoke key"}`), createHeaders)
	if create.Code != http.StatusCreated {
		t.Fatalf("create downstream key status = %d, body = %s", create.Code, create.Body.String())
	}
	var issued struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if issued.Secret == "" {
		t.Fatal("control plane did not return the one-time downstream secret")
	}

	models := serve(runtime, http.MethodGet, "/v1/models", nil, map[string]string{
		"Authorization": "Bearer " + issued.Secret,
	})
	if models.Code != http.StatusOK {
		t.Fatalf("VNext models status = %d, body = %s", models.Code, models.Body.String())
	}
	var modelBody struct {
		Object string            `json:"object"`
		Data   []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(models.Body.Bytes(), &modelBody); err != nil {
		t.Fatal(err)
	}
	if modelBody.Object != "list" || len(modelBody.Data) != 0 {
		t.Fatalf("unexpected empty model catalog response: %#v", modelBody)
	}

	missingAPI := serve(runtime, http.MethodGet, "/api/not-real", nil, nil)
	if missingAPI.Code != http.StatusNotFound || strings.Contains(missingAPI.Body.String(), "JieShan test app") {
		t.Fatalf("missing API was swallowed by SPA: %d %s", missingAPI.Code, missingAPI.Body.String())
	}
}

func TestOpenReturnsGeneratedBootstrapPasswordOnce(t *testing.T) {
	options := testRuntimeOptions(t)
	options.AdminAuth.InitialPassword = ""
	runtime, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	password := runtime.TakeBootstrapPassword()
	if len(password) < 32 {
		t.Fatalf("generated bootstrap password is unexpectedly short: %d", len(password))
	}
	if runtime.TakeBootstrapPassword() != "" {
		t.Fatal("bootstrap password was returned more than once")
	}
	_ = login(t, runtime, password)
}

func TestDownstreamSecretEndpointsRequireAdminCSRFRecentLoginAndCAS(t *testing.T) {
	now := time.Date(2026, 8, 7, 9, 0, 0, 0, time.UTC)
	options := testRuntimeOptions(t)
	options.AdminAuth.Now = func() time.Time { return now }
	options.AdminAuth.RecentReauthenticationTTL = 5 * time.Minute
	runtime, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	headers := login(t, runtime, testAdminPassword)
	createHeaders := cloneHeaders(headers)
	createHeaders["Content-Type"] = "application/json"
	createdResponse := serve(runtime, http.MethodPost, DownstreamKeysAdminPrefix,
		[]byte(`{"name":"Runtime secret lifecycle"}`), createHeaders)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create key = %d %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created struct {
		Item struct {
			ID       int64 `json:"id"`
			Revision int64 `json:"revision"`
		} `json:"item"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	path := DownstreamKeysAdminPrefix + "/" + strconv.FormatInt(created.Item.ID, 10)

	unauthenticated := serve(runtime, http.MethodPost, path+"/reveal", nil, nil)
	assertRuntimeError(t, unauthenticated, http.StatusUnauthorized, "unauthenticated")
	missingCSRFHeaders := cloneHeaders(headers)
	delete(missingCSRFHeaders, adminauth.CSRFHeaderName)
	missingCSRF := serve(runtime, http.MethodPost, path+"/reveal", nil, missingCSRFHeaders)
	assertRuntimeError(t, missingCSRF, http.StatusForbidden, "csrf_rejected")

	revealed := serve(runtime, http.MethodPost, path+"/reveal", nil, headers)
	if revealed.Code != http.StatusOK || revealed.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(revealed.Body.String(), created.Secret) {
		t.Fatalf("fresh reveal = %d %v %s", revealed.Code, revealed.Header(), revealed.Body.String())
	}

	now = now.Add(5*time.Minute + time.Millisecond)
	expiredProof := serve(runtime, http.MethodPost, path+"/reveal", nil, headers)
	assertRuntimeError(t, expiredProof, http.StatusForbidden, "recent_reauthentication_required")
	freshHeaders := login(t, runtime, testAdminPassword)
	revealedAgain := serve(runtime, http.MethodPost, path+"/reveal", nil, freshHeaders)
	if revealedAgain.Code != http.StatusOK || !strings.Contains(revealedAgain.Body.String(), created.Secret) {
		t.Fatalf("reveal after re-login = %d %s", revealedAgain.Code, revealedAgain.Body.String())
	}

	missingRotateCSRFHeaders := cloneHeaders(freshHeaders)
	delete(missingRotateCSRFHeaders, adminauth.CSRFHeaderName)
	missingRotateCSRFHeaders["If-Match"] = `"1"`
	missingRotateCSRF := serve(runtime, http.MethodPost, path+"/rotate", nil, missingRotateCSRFHeaders)
	assertRuntimeError(t, missingRotateCSRF, http.StatusForbidden, "csrf_rejected")
	missingRevision := serve(runtime, http.MethodPost, path+"/rotate", nil, freshHeaders)
	assertRuntimeError(t, missingRevision, http.StatusPreconditionRequired, "precondition_required")
	staleHeaders := cloneHeaders(freshHeaders)
	staleHeaders["If-Match"] = `"2"`
	stale := serve(runtime, http.MethodPost, path+"/rotate", nil, staleHeaders)
	assertRuntimeError(t, stale, http.StatusConflict, "revision_conflict")

	rotateHeaders := cloneHeaders(freshHeaders)
	rotateHeaders["If-Match"] = `"1"`
	rotatedResponse := serve(runtime, http.MethodPost, path+"/rotate", nil, rotateHeaders)
	if rotatedResponse.Code != http.StatusOK || rotatedResponse.Header().Get("ETag") != `"2"` ||
		rotatedResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("rotate = %d %v %s", rotatedResponse.Code, rotatedResponse.Header(), rotatedResponse.Body.String())
	}
	var rotated struct {
		Item struct {
			Revision int64 `json:"revision"`
		} `json:"item"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(rotatedResponse.Body.Bytes(), &rotated); err != nil {
		t.Fatal(err)
	}
	if rotated.Item.Revision != created.Item.Revision+1 || rotated.Secret == "" || rotated.Secret == created.Secret {
		t.Fatalf("rotated payload = %+v", rotated)
	}
	if oldAuth := serve(runtime, http.MethodGet, "/v1/models", nil, map[string]string{
		"Authorization": "Bearer " + created.Secret,
	}); oldAuth.Code != http.StatusUnauthorized {
		t.Fatalf("old secret remains active after rotation: %d %s", oldAuth.Code, oldAuth.Body.String())
	}
}

func TestOpenInjectsMonitorAndOwnsItsLifecycle(t *testing.T) {
	monitor := &monitorStub{started: make(chan struct{})}
	factoryCalled := false
	options := testRuntimeOptions(t)
	options.MonitorFactory = MonitorFactoryFunc(func(dependencies MonitorDependencies) (BackgroundService, error) {
		factoryCalled = true
		if dependencies.Store == nil || dependencies.Registry == nil || dependencies.Client == nil ||
			dependencies.Secrets == nil || dependencies.CredentialEffects == nil || dependencies.Settings == nil {
			return nil, errors.New("monitor received incomplete runtime dependencies")
		}
		for _, contract := range dependencies.Registry.Contracts() {
			if !contract.Routable() {
				return nil, errors.New("runtime protocol registry contains an incomplete adapter")
			}
		}
		if dependencies.HealthPolicy.FailureThreshold != 2 || dependencies.HealthPolicy.Cooldown != 5*time.Minute {
			return nil, errors.New("monitor did not receive normalized gateway health policy")
		}
		if snapshot := dependencies.Settings.MonitoringSnapshot(); snapshot.ProbeInterval != 5*time.Minute ||
			snapshot.HealthPolicy.FailureThreshold != 2 {
			return nil, errors.New("monitor did not receive dynamic runtime settings")
		}
		return monitor, nil
	})
	runtime, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if !factoryCalled {
		t.Fatal("monitor factory was not called")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	select {
	case <-monitor.started:
	case <-time.After(2 * time.Second):
		t.Fatal("monitor did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runtime shutdown returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not stop its monitor")
	}
}

func TestRuntimeOwnsBestEffortOperationalHistoryRetention(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	options := testRuntimeOptions(t)
	options.Retention.Interval = 20 * time.Millisecond
	options.Retention.Timeout = 100 * time.Millisecond
	options.Retention.Now = func() time.Time { return now }
	retentionFailure := make(chan struct{}, 1)
	options.Retention.Logger = slog.New(slog.NewTextHandler(&retentionFailureWriter{signal: retentionFailure}, nil))
	runtime, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	ctx := context.Background()
	siteID, err := runtime.store.CreateSite(ctx, vnextstore.SiteWrite{Name: "Retention runtime", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.store.CreateSealedSiteAccountConnection(ctx, siteID,
		vnextstore.SealedSiteAccountConnectionInput{
			AdapterKind: "ciii", Origin: "https://retention.invalid", CipherVersion: 1, Enabled: false,
		}, func(int64, int64) ([]byte, error) { return []byte("sealed"), nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.store.SaveSiteUsageRecords(ctx, siteID, "ciii", []vnextstore.SiteUsageRecordWrite{{
		DedupKey: "expired-runtime-usage", OccurredAt: now.Add(-31 * 24 * time.Hour).UnixMilli(), Model: "expired-model",
	}}, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.store.DB.ExecContext(ctx, `CREATE TRIGGER fail_runtime_retention
BEFORE DELETE ON site_usage_records
BEGIN SELECT RAISE(ABORT,'forced runtime retention failure'); END`); err != nil {
		t.Fatal(err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(runCtx) }()
	select {
	case <-retentionFailure:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime retention failure was not observed")
	}
	select {
	case err := <-done:
		t.Fatalf("retention failure stopped runtime: %v", err)
	default:
	}
	assertRuntimeTableCount(t, runtime.store, "site_usage_records", 1)

	if _, err := runtime.store.DB.ExecContext(ctx, `DROP TRIGGER fail_runtime_retention`); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		var count int
		if err := runtime.store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_usage_records`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("runtime did not retry operational history cleanup")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runtime shutdown returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not stop retention service")
	}
}

func TestRuntimeSettingsPersistAcrossOpen(t *testing.T) {
	options := testRuntimeOptions(t)
	first, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	headers := login(t, first, testAdminPassword)
	headers["Content-Type"] = "application/json"
	headers["If-Match"] = `"1"`
	body := []byte(`{"failureThreshold":4,"failureWindowMs":420000,"cooldownMs":180000,"probeIntervalMs":240000,"firstOutputTimeoutMs":20000,"streamIdleTimeoutMs":50000,"requestTimeoutMs":240000,"maxAttempts":5,"logRetentionDays":90}`)
	response := serve(first, http.MethodPatch, SettingsAdminPrefix, body, headers)
	if response.Code != http.StatusOK {
		t.Fatalf("first settings PATCH = %d %s", response.Code, response.Body.String())
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	get := serve(second, http.MethodGet, SettingsAdminPrefix, nil, login(t, second, testAdminPassword))
	if get.Code != http.StatusOK || get.Header().Get("ETag") != `"2"` ||
		!strings.Contains(get.Body.String(), `"failureThreshold":4`) ||
		!strings.Contains(get.Body.String(), `"logRetentionDays":90`) {
		t.Fatalf("reopened settings = %d %q %s", get.Code, get.Header().Get("ETag"), get.Body.String())
	}
}

func TestOpenValidatesRequiredRuntimeInputs(t *testing.T) {
	valid := testRuntimeOptions(t)
	tests := []struct {
		name   string
		mutate func(*Options)
	}{
		{name: "missing data directory", mutate: func(options *Options) { options.DataDir = "" }},
		{name: "missing web directory", mutate: func(options *Options) { options.WebDir = "" }},
		{name: "invalid web directory", mutate: func(options *Options) { options.WebDir = filepath.Join(t.TempDir(), "missing") }},
		{name: "short administrator password", mutate: func(options *Options) { options.AdminAuth.InitialPassword = "short" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := valid
			options.DataDir = t.TempDir()
			test.mutate(&options)
			if runtime, err := Open(context.Background(), options); err == nil {
				_ = runtime.Close()
				t.Fatal("Open accepted invalid runtime options")
			}
		})
	}
}

type monitorStub struct {
	started chan struct{}
}

func (monitor *monitorStub) Run(ctx context.Context) error {
	close(monitor.started)
	<-ctx.Done()
	return ctx.Err()
}

func testRuntimeOptions(t *testing.T) Options {
	t.Helper()
	secure := false
	return Options{
		DataDir: t.TempDir(),
		WebDir:  testWebDir(t),
		AdminAuth: adminauth.Options{
			InitialPassword: testAdminPassword,
			Argon2: adminauth.Argon2Params{
				MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1, SaltBytes: 16, KeyBytes: 32,
			},
			SecureCookies: &secure,
		},
	}
}

func testWebDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte("<!doctype html><title>JieShan test app</title>"), 0o600); err != nil {
		t.Fatal(err)
	}
	return directory
}

func login(t *testing.T, handler http.Handler, password string) map[string]string {
	t.Helper()
	response := serve(handler, http.MethodPost, AuthPrefix+"/login", []byte(`{"username":"admin","password":"`+password+`"}`), map[string]string{
		"Content-Type": "application/json",
		"Origin":       "http://example.com",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", response.Code, response.Body.String())
	}
	var session, csrf *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		switch cookie.Name {
		case adminauth.SessionCookieName:
			session = cookie
		case adminauth.CSRFCookieName:
			csrf = cookie
		}
	}
	if session == nil || csrf == nil {
		t.Fatalf("login cookies = %+v", response.Result().Cookies())
	}
	return map[string]string{
		"Cookie":                 session.Name + "=" + session.Value + "; " + csrf.Name + "=" + csrf.Value,
		"Origin":                 "http://example.com",
		adminauth.CSRFHeaderName: csrf.Value,
	}
}

func cloneHeaders(source map[string]string) map[string]string {
	result := make(map[string]string, len(source)+1)
	for name, value := range source {
		result[name] = value
	}
	return result
}

func assertRuntimeError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status || !strings.Contains(response.Body.String(), `"code":"`+code+`"`) {
		t.Fatalf("error response = %d %s, want %d/%s", response.Code, response.Body.String(), status, code)
	}
}

func serve(handler http.Handler, method, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertRuntimeTableCount(t *testing.T, storage *vnextstore.Store, table string, want int) {
	t.Helper()
	var count int
	if err := storage.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("%s count = %d, want %d", table, count, want)
	}
}

type retentionFailureWriter struct {
	signal chan<- struct{}
}

func (writer *retentionFailureWriter) Write(payload []byte) (int, error) {
	if bytes.Contains(payload, []byte("operational history cleanup failed")) {
		select {
		case writer.signal <- struct{}{}:
		default:
		}
	}
	return len(payload), nil
}
