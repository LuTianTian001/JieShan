package adminauth_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/adminauth"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const testPassword = "correct horse battery staple"

type mutableClock struct {
	now time.Time
}

func (clock *mutableClock) Now() time.Time { return clock.now }

func TestBootstrapLoginPersistsOnlyHashesAndSessionSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "admin-auth.db")
	clock := &mutableClock{now: time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)}
	storage, err := vnextstore.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	service, bootstrap, err := adminauth.NewService(ctx, storage, testOptions(clock, testPassword))
	if err != nil {
		t.Fatal(err)
	}
	if !bootstrap.Created || bootstrap.GeneratedPassword != "" {
		t.Fatalf("bootstrap = %+v", bootstrap)
	}
	handler, err := adminauth.NewHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	login := loginRequest(t, handler, "http://panel.example", testPassword, "198.51.100.10:1234")
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}
	cookies := authCookies(t, login)
	sessionCookie, csrfCookie := cookies[adminauth.SessionCookieName], cookies[adminauth.CSRFCookieName]
	if !sessionCookie.HttpOnly || csrfCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteStrictMode ||
		csrfCookie.SameSite != http.SameSiteStrictMode || sessionCookie.Secure || csrfCookie.Secure {
		t.Fatalf("cookies are not hardened correctly: session=%+v csrf=%+v", sessionCookie, csrfCookie)
	}
	var passwordHash string
	if err := storage.DB.QueryRowContext(ctx, `SELECT password_hash FROM admin_users WHERE id=1`).Scan(&passwordHash); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(passwordHash, testPassword) || !strings.HasPrefix(passwordHash, "$argon2id$") {
		t.Fatalf("password hash leaked plaintext or is not Argon2id: %q", passwordHash)
	}
	var storedSessionHash, storedCSRFHash []byte
	if err := storage.DB.QueryRowContext(ctx, `SELECT token_hash,csrf_hash FROM admin_sessions`).Scan(&storedSessionHash, &storedCSRFHash); err != nil {
		t.Fatal(err)
	}
	wantSessionHash := sha256.Sum256([]byte(sessionCookie.Value))
	wantCSRFHash := sha256.Sum256([]byte(csrfCookie.Value))
	if !bytes.Equal(storedSessionHash, wantSessionHash[:]) || !bytes.Equal(storedCSRFHash, wantCSRFHash[:]) ||
		bytes.Contains(storedSessionHash, []byte(sessionCookie.Value)) || bytes.Contains(storedCSRFHash, []byte(csrfCookie.Value)) {
		t.Fatalf("stored session material is not digest-only")
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := vnextstore.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restartedService, restartedBootstrap, err := adminauth.NewService(ctx, reopened, testOptions(clock, "a different initial password"))
	if err != nil {
		t.Fatal(err)
	}
	if restartedBootstrap.Created || restartedBootstrap.GeneratedPassword != "" {
		t.Fatalf("restart bootstrap = %+v", restartedBootstrap)
	}
	restartedHandler, err := adminauth.NewHandler(restartedService)
	if err != nil {
		t.Fatal(err)
	}
	status := authRequest(restartedHandler, http.MethodGet, "http://panel.example/api/vnext/auth/status", nil, "", cookies, "")
	if status.Code != http.StatusOK || !jsonBool(t, status, "authenticated") {
		t.Fatalf("restart status=%d body=%s", status.Code, status.Body.String())
	}
}

func TestGeneratedBootstrapPasswordIsReturnedOnce(t *testing.T) {
	ctx := context.Background()
	storage, err := vnextstore.Open(ctx, filepath.Join(t.TempDir(), "generated-bootstrap.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	clock := &mutableClock{now: time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)}
	options := testOptions(clock, "")
	service, bootstrap, err := adminauth.NewService(ctx, storage, options)
	if err != nil {
		t.Fatal(err)
	}
	if !bootstrap.Created || len(bootstrap.GeneratedPassword) < 24 {
		t.Fatalf("bootstrap = %+v", bootstrap)
	}
	_, second, err := adminauth.NewService(ctx, storage, options)
	if err != nil {
		t.Fatal(err)
	}
	if second.Created || second.GeneratedPassword != "" {
		t.Fatalf("second bootstrap = %+v", second)
	}
	handler, _ := adminauth.NewHandler(service)
	login := loginRequest(t, handler, "http://panel.example", bootstrap.GeneratedPassword, "198.51.100.20:1234")
	if login.Code != http.StatusOK {
		t.Fatalf("generated password login status=%d body=%s", login.Code, login.Body.String())
	}
}

func TestGeneratedPasswordPersistenceFailurePreventsAdministratorCommit(t *testing.T) {
	ctx := context.Background()
	storage, err := vnextstore.Open(ctx, filepath.Join(t.TempDir(), "persistence-failure.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	clock := &mutableClock{now: time.Date(2026, time.August, 7, 9, 0, 0, 0, time.UTC)}
	options := testOptions(clock, "")
	var generated string
	options.PersistGeneratedPassword = func(password string) error {
		generated = password
		return errors.New("forced password file write failure")
	}
	service, bootstrap, err := adminauth.NewService(ctx, storage, options)
	if err == nil || service != nil || bootstrap.Created || bootstrap.GeneratedPassword != "" {
		t.Fatalf("NewService() = service %v bootstrap %+v error %v", service, bootstrap, err)
	}
	if len(generated) < 24 || strings.Contains(err.Error(), generated) {
		t.Fatalf("generated password persistence error leaked or omitted generation evidence: length=%d error=%q", len(generated), err)
	}
	var count int
	if err := storage.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_users`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("administrator rows after password persistence failure = %d", count)
	}
}

func TestLoginRateLimitExpiresAndValidLoginRecovers(t *testing.T) {
	ctx := context.Background()
	storage, err := vnextstore.Open(ctx, filepath.Join(t.TempDir(), "login-limit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	clock := &mutableClock{now: time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)}
	options := testOptions(clock, testPassword)
	options.MaxLoginFailures = 2
	options.LoginWindow = time.Minute
	options.LoginLockout = 2 * time.Minute
	service, _, err := adminauth.NewService(ctx, storage, options)
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := adminauth.NewHandler(service)
	first := loginRequest(t, handler, "http://panel.example", "wrong password value", "203.0.113.8:5000")
	second := loginRequest(t, handler, "http://panel.example", "wrong password value", "203.0.113.8:5001")
	blocked := loginRequest(t, handler, "http://panel.example", testPassword, "203.0.113.8:5002")
	if first.Code != http.StatusUnauthorized || second.Code != http.StatusTooManyRequests ||
		blocked.Code != http.StatusTooManyRequests || blocked.Header().Get("Retry-After") == "" {
		t.Fatalf("rate statuses first=%d second=%d blocked=%d retry=%q", first.Code, second.Code, blocked.Code, blocked.Header().Get("Retry-After"))
	}
	clock.now = clock.now.Add(2*time.Minute + time.Second)
	recovered := loginRequest(t, handler, "http://panel.example", testPassword, "203.0.113.8:5003")
	if recovered.Code != http.StatusOK {
		t.Fatalf("recovered status=%d body=%s", recovered.Code, recovered.Body.String())
	}
}

func TestAdminMiddlewareAndLogoutRequireOriginAndBoundCSRF(t *testing.T) {
	ctx := context.Background()
	storage, err := vnextstore.Open(ctx, filepath.Join(t.TempDir(), "csrf.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	clock := &mutableClock{now: time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)}
	service, _, err := adminauth.NewService(ctx, storage, testOptions(clock, testPassword))
	if err != nil {
		t.Fatal(err)
	}
	authHandler, _ := adminauth.NewHandler(service)
	login := loginRequest(t, authHandler, "http://panel.example", testPassword, "192.0.2.44:9000")
	cookies := authCookies(t, login)
	csrf := cookies[adminauth.CSRFCookieName].Value
	protected := service.WrapAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := adminauth.PrincipalFromContext(r.Context())
		if !ok || principal.Username != adminauth.AdminUsername {
			t.Fatalf("principal = %+v, ok=%v", principal, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	read := authRequest(protected, http.MethodGet, "http://panel.example/api/vnext/inventory/sites", nil, "", cookies, "")
	missingOrigin := authRequest(protected, http.MethodPost, "http://panel.example/api/vnext/inventory/sites", nil, "", cookies, csrf)
	wrongOrigin := authRequest(protected, http.MethodPost, "http://panel.example/api/vnext/inventory/sites", nil, "http://evil.example", cookies, csrf)
	missingCSRF := authRequest(protected, http.MethodPost, "http://panel.example/api/vnext/inventory/sites", nil, "http://panel.example", cookies, "")
	valid := authRequest(protected, http.MethodPost, "http://panel.example/api/vnext/inventory/sites", nil, "http://panel.example", cookies, csrf)
	if read.Code != http.StatusNoContent || missingOrigin.Code != http.StatusForbidden ||
		wrongOrigin.Code != http.StatusForbidden || missingCSRF.Code != http.StatusForbidden || valid.Code != http.StatusNoContent {
		t.Fatalf("middleware statuses read=%d missingOrigin=%d wrongOrigin=%d missingCSRF=%d valid=%d",
			read.Code, missingOrigin.Code, wrongOrigin.Code, missingCSRF.Code, valid.Code)
	}

	badLogout := authRequest(authHandler, http.MethodPost, "http://panel.example/api/vnext/auth/logout", nil, "http://panel.example", cookies, "wrong-token")
	if badLogout.Code != http.StatusForbidden {
		t.Fatalf("bad logout status=%d body=%s", badLogout.Code, badLogout.Body.String())
	}
	logout := authRequest(authHandler, http.MethodPost, "http://panel.example/api/vnext/auth/logout", nil, "http://panel.example", cookies, csrf)
	if logout.Code != http.StatusNoContent || len(logout.Result().Cookies()) != 2 {
		t.Fatalf("logout status=%d body=%s cookies=%+v", logout.Code, logout.Body.String(), logout.Result().Cookies())
	}
	after := authRequest(protected, http.MethodGet, "http://panel.example/api/vnext/inventory/sites", nil, "", cookies, "")
	if after.Code != http.StatusUnauthorized {
		t.Fatalf("post-logout status=%d body=%s", after.Code, after.Body.String())
	}
}

func TestPasswordChangeValidatesInputAndRevokesOtherSessions(t *testing.T) {
	ctx := context.Background()
	storage, err := vnextstore.Open(ctx, filepath.Join(t.TempDir(), "password-change.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	clock := &mutableClock{now: time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)}
	service, _, err := adminauth.NewService(ctx, storage, testOptions(clock, testPassword))
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := adminauth.NewHandler(service)
	currentLogin := loginRequest(t, handler, "http://panel.example", testPassword, "192.0.2.10:1000")
	otherLogin := loginRequest(t, handler, "http://panel.example", testPassword, "192.0.2.11:1000")
	currentCookies := authCookies(t, currentLogin)
	otherCookies := authCookies(t, otherLogin)
	csrf := currentCookies[adminauth.CSRFCookieName].Value
	newPassword := "replacement administrator password"

	missingCSRF := passwordRequest(t, handler, currentCookies, "", testPassword, newPassword, newPassword)
	wrongCurrent := passwordRequest(t, handler, currentCookies, csrf, "wrong current password", newPassword, newPassword)
	mismatch := passwordRequest(t, handler, currentCookies, csrf, testPassword, newPassword, "different confirmation value")
	tooShort := passwordRequest(t, handler, currentCookies, csrf, testPassword, "short", "short")
	unchanged := passwordRequest(t, handler, currentCookies, csrf, testPassword, testPassword, testPassword)
	if missingCSRF.Code != http.StatusForbidden || wrongCurrent.Code != http.StatusUnauthorized ||
		mismatch.Code != http.StatusBadRequest || tooShort.Code != http.StatusBadRequest || unchanged.Code != http.StatusBadRequest {
		t.Fatalf("password validation statuses csrf=%d current=%d confirmation=%d short=%d unchanged=%d",
			missingCSRF.Code, wrongCurrent.Code, mismatch.Code, tooShort.Code, unchanged.Code)
	}
	if jsonErrorCode(t, wrongCurrent) != "invalid_current_password" ||
		jsonErrorCode(t, mismatch) != "password_confirmation_mismatch" ||
		jsonErrorCode(t, tooShort) != "password_too_short" || jsonErrorCode(t, unchanged) != "password_unchanged" {
		t.Fatal("password validation returned an unexpected error contract")
	}

	changed := passwordRequest(t, handler, currentCookies, csrf, testPassword, newPassword, newPassword)
	if changed.Code != http.StatusNoContent || changed.Body.Len() != 0 {
		t.Fatalf("password change status=%d body=%s", changed.Code, changed.Body.String())
	}
	staleCreatedAt := clock.now.UnixMilli()
	staleSession := adminauth.Session{
		TokenHash: sha256.Sum256([]byte("stale-session-token")), AdminUserID: 1, AdminUsername: adminauth.AdminUsername,
		CSRFHash: sha256.Sum256([]byte("stale-csrf-token")), CreatedAt: staleCreatedAt,
		LastSeenAt: staleCreatedAt, ExpiresAt: clock.now.Add(time.Hour).UnixMilli(),
	}
	if err := storage.CreateAdminSession(ctx, staleSession, 1); !errors.Is(err, adminauth.ErrAdminRevisionConflict) {
		t.Fatalf("stale credential session error = %v", err)
	}
	var sessionCount int
	if err := storage.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_sessions`).Scan(&sessionCount); err != nil || sessionCount != 1 {
		t.Fatalf("session count after password change=%d err=%v", sessionCount, err)
	}
	currentStatus := authRequest(handler, http.MethodGet, "http://panel.example/api/vnext/auth/status", nil, "", currentCookies, "")
	otherStatus := authRequest(handler, http.MethodGet, "http://panel.example/api/vnext/auth/status", nil, "", otherCookies, "")
	if currentStatus.Code != http.StatusOK || !jsonBool(t, currentStatus, "authenticated") ||
		otherStatus.Code != http.StatusOK || jsonBool(t, otherStatus, "authenticated") {
		t.Fatalf("post-change sessions current=%s other=%s", currentStatus.Body.String(), otherStatus.Body.String())
	}

	oldLogin := loginRequest(t, handler, "http://panel.example", testPassword, "192.0.2.12:1000")
	newLogin := loginRequest(t, handler, "http://panel.example", newPassword, "192.0.2.13:1000")
	if oldLogin.Code != http.StatusUnauthorized || newLogin.Code != http.StatusOK {
		t.Fatalf("post-change login statuses old=%d new=%d", oldLogin.Code, newLogin.Code)
	}
	var passwordHash string
	var revision int64
	if err := storage.DB.QueryRowContext(ctx, `SELECT password_hash,revision FROM admin_users WHERE id=1`).Scan(&passwordHash, &revision); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(passwordHash, newPassword) || !strings.HasPrefix(passwordHash, "$argon2id$") || revision != 2 {
		t.Fatal("changed administrator credential was not stored as the expected versioned Argon2id verifier")
	}
}

func TestExpiredSessionIsRejectedDeletedAndCookiesCleared(t *testing.T) {
	ctx := context.Background()
	storage, err := vnextstore.Open(ctx, filepath.Join(t.TempDir(), "expired.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	clock := &mutableClock{now: time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)}
	options := testOptions(clock, testPassword)
	options.SessionTTL = 5 * time.Minute
	options.SessionTouchInterval = time.Minute
	service, _, err := adminauth.NewService(ctx, storage, options)
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := adminauth.NewHandler(service)
	login := loginRequest(t, handler, "http://panel.example", testPassword, "198.51.100.30:1000")
	cookies := authCookies(t, login)
	clock.now = clock.now.Add(5 * time.Minute)
	status := authRequest(handler, http.MethodGet, "http://panel.example/api/vnext/auth/status", nil, "", cookies, "")
	if status.Code != http.StatusOK || jsonBool(t, status, "authenticated") || len(status.Result().Cookies()) != 2 {
		t.Fatalf("expired status=%d body=%s cookies=%+v", status.Code, status.Body.String(), status.Result().Cookies())
	}
	var count int
	if err := storage.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("session count=%d err=%v", count, err)
	}
}

func TestAuthHandlerMethodJSONOriginAndSecureCookieContracts(t *testing.T) {
	ctx := context.Background()
	storage, err := vnextstore.Open(ctx, filepath.Join(t.TempDir(), "contracts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	clock := &mutableClock{now: time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)}
	options := testOptions(clock, testPassword)
	options.AllowedOrigins = []string{"https://panel.example"}
	options.SecureCookies = nil
	service, _, err := adminauth.NewService(ctx, storage, options)
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := adminauth.NewHandler(service)

	method := authRequest(handler, http.MethodPost, "https://panel.example/api/vnext/auth/status", nil, "https://panel.example", nil, "")
	loginMethod := authRequest(handler, http.MethodGet, "https://panel.example/api/vnext/auth/login", nil, "", nil, "")
	unknown := authRequest(handler, http.MethodGet, "https://panel.example/api/vnext/auth/unknown", nil, "", nil, "")
	if method.Code != http.StatusMethodNotAllowed || method.Header().Get("Allow") != http.MethodGet ||
		loginMethod.Code != http.StatusMethodNotAllowed || unknown.Code != http.StatusNotFound {
		t.Fatalf("method contracts status=%d allow=%q login=%d unknown=%d", method.Code, method.Header().Get("Allow"), loginMethod.Code, unknown.Code)
	}

	wrongType := rawAuthRequest(handler, http.MethodPost, "https://panel.example/api/vnext/auth/login", `{"username":"admin"}`, "text/plain", "https://panel.example")
	malformed := rawAuthRequest(handler, http.MethodPost, "https://panel.example/api/vnext/auth/login", `{`, "application/json", "https://panel.example")
	unknownField := rawAuthRequest(handler, http.MethodPost, "https://panel.example/api/vnext/auth/login", `{"username":"admin","password":"x","role":"owner"}`, "application/json", "https://panel.example")
	trailing := rawAuthRequest(handler, http.MethodPost, "https://panel.example/api/vnext/auth/login", `{"username":"admin","password":"x"}{}`, "application/json", "https://panel.example")
	rejectedOrigin := rawAuthRequest(handler, http.MethodPost, "https://panel.example/api/vnext/auth/login", `{"username":"admin","password":"x"}`, "application/json", "https://evil.example")
	if wrongType.Code != http.StatusUnsupportedMediaType || malformed.Code != http.StatusBadRequest ||
		unknownField.Code != http.StatusBadRequest || trailing.Code != http.StatusBadRequest || rejectedOrigin.Code != http.StatusForbidden {
		t.Fatalf("JSON/origin statuses type=%d malformed=%d unknown=%d trailing=%d origin=%d",
			wrongType.Code, malformed.Code, unknownField.Code, trailing.Code, rejectedOrigin.Code)
	}

	login := loginRequest(t, handler, "https://panel.example", testPassword, "192.0.2.90:1200")
	if login.Code != http.StatusOK {
		t.Fatalf("secure login status=%d body=%s", login.Code, login.Body.String())
	}
	for name, cookie := range authCookies(t, login) {
		if !cookie.Secure {
			t.Fatalf("cookie %s is not Secure: %+v", name, cookie)
		}
	}
}

func testOptions(clock *mutableClock, password string) adminauth.Options {
	secure := false
	return adminauth.Options{
		InitialPassword: password,
		Argon2: adminauth.Argon2Params{
			MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1, SaltBytes: 16, KeyBytes: 32,
		},
		SessionTTL: 24 * time.Hour, SessionTouchInterval: time.Minute,
		MaxLoginFailures: 5, LoginWindow: 5 * time.Minute, LoginLockout: 15 * time.Minute,
		AllowedOrigins: []string{"http://panel.example"}, SecureCookies: &secure, Now: clock.Now,
	}
}

func loginRequest(t *testing.T, handler http.Handler, origin, password, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"username": adminauth.AdminUsername, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, origin+"/api/vnext/auth/login", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", origin)
	request.RemoteAddr = remoteAddr
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func rawAuthRequest(handler http.Handler, method, target, body, contentType, origin string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func authRequest(handler http.Handler, method, target string, body []byte, origin string, cookies map[string]*http.Cookie, csrf string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	if csrf != "" {
		request.Header.Set(adminauth.CSRFHeaderName, csrf)
	}
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func passwordRequest(
	t *testing.T,
	handler http.Handler,
	cookies map[string]*http.Cookie,
	csrf string,
	currentPassword string,
	newPassword string,
	confirmPassword string,
) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{
		"currentPassword": currentPassword,
		"newPassword":     newPassword,
		"confirmPassword": confirmPassword,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://panel.example/api/vnext/auth/password", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://panel.example")
	if csrf != "" {
		request.Header.Set(adminauth.CSRFHeaderName, csrf)
	}
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func authCookies(t *testing.T, response *httptest.ResponseRecorder) map[string]*http.Cookie {
	t.Helper()
	result := make(map[string]*http.Cookie)
	for _, cookie := range response.Result().Cookies() {
		copy := *cookie
		result[cookie.Name] = &copy
	}
	if result[adminauth.SessionCookieName] == nil || result[adminauth.CSRFCookieName] == nil {
		t.Fatalf("authentication cookies missing from status=%d body=%s cookies=%+v", response.Code, response.Body.String(), response.Result().Cookies())
	}
	return result
}

func jsonBool(t *testing.T, response *httptest.ResponseRecorder, key string) bool {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", response.Body.String(), err)
	}
	value, ok := body[key].(bool)
	if !ok {
		t.Fatalf("response %q missing bool %s", response.Body.String(), key)
	}
	return value
}

func jsonErrorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	return body.Error.Code
}
