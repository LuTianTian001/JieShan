package settingsapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/settings"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

func TestSettingsAPIRequiresCASAndReturnsFrontendContract(t *testing.T) {
	ctx := context.Background()
	storage, err := vnextstore.Open(ctx, filepath.Join(t.TempDir(), "jieshan.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	now := time.Date(2026, 8, 6, 13, 0, 0, 0, time.UTC)
	service, err := settings.NewService(ctx, storage, settings.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewServiceHandler(service)
	if err != nil {
		t.Fatal(err)
	}

	response := perform(handler, http.MethodGet, APIPrefix, "", nil)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"1"` ||
		response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET status/headers = %d/%v", response.Code, response.Header())
	}
	var initial settingsResponse
	decodeResponse(t, response, &initial)
	if initial.FailureThreshold != 2 || initial.FailureWindowMS != 300000 || initial.CooldownMS != 900000 ||
		initial.ProbeIntervalMS != 900000 || initial.FirstOutputTimeoutMS != 15000 ||
		initial.StreamIdleTimeoutMS != 60000 || initial.RequestTimeoutMS != 300000 ||
		initial.MaxAttempts != 4 || initial.LogRetentionDays != 30 || initial.Revision != 1 {
		t.Fatalf("initial API settings = %+v", initial)
	}

	body := `{"failureThreshold":3,"failureWindowMs":420000,"cooldownMs":120000,` +
		`"probeIntervalMs":180000,"firstOutputTimeoutMs":15000,"streamIdleTimeoutMs":45000,` +
		`"requestTimeoutMs":240000,"maxAttempts":6,"logRetentionDays":45}`
	response = perform(handler, http.MethodPatch, APIPrefix, body, map[string]string{"Content-Type": "application/json"})
	assertAPIError(t, response, http.StatusPreconditionRequired, "precondition_required")

	response = perform(handler, http.MethodPatch, APIPrefix, body, map[string]string{
		"Content-Type": "application/json", "If-Match": `"9"`,
	})
	assertAPIError(t, response, http.StatusConflict, "revision_conflict")

	now = now.Add(time.Minute)
	response = perform(handler, http.MethodPatch, APIPrefix, body, map[string]string{
		"Content-Type": "application/json", "If-Match": `"1"`,
	})
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("PATCH status/etag = %d/%q, body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	var updated settingsResponse
	decodeResponse(t, response, &updated)
	if updated.Revision != 2 || updated.FailureThreshold != 3 || updated.ProbeIntervalMS != 180000 ||
		updated.LogRetentionDays != 45 {
		t.Fatalf("updated API settings = %+v", updated)
	}

	response = perform(handler, http.MethodPatch, APIPrefix, strings.TrimSuffix(body, "}")+`,"unknown":1}`,
		map[string]string{"Content-Type": "application/json", "If-Match": `"2"`})
	assertAPIError(t, response, http.StatusBadRequest, "invalid_request")
	response = perform(handler, http.MethodPatch, APIPrefix,
		`{"failureThreshold":1}`, map[string]string{"Content-Type": "application/json", "If-Match": `"2"`})
	assertAPIError(t, response, http.StatusBadRequest, "invalid_request")
	response = perform(handler, http.MethodPatch, APIPrefix, body,
		map[string]string{"If-Match": `"2"`})
	assertAPIError(t, response, http.StatusBadRequest, "invalid_request")
}

func TestRuntimeOverviewUsesInjectedProvider(t *testing.T) {
	ctx := context.Background()
	storage, err := vnextstore.Open(ctx, filepath.Join(t.TempDir(), "jieshan.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	service, err := settings.NewService(ctx, storage, settings.Options{})
	if err != nil {
		t.Fatal(err)
	}
	provider := staticRuntimeOverviewProvider{value: RuntimeOverview{
		Runtime: GatewayRuntimeSnapshot{
			ProcessStartedAt: 1_000, SnapshotAt: 2_000, ConfigRevision: 3,
			ConfigLoadedAt: 1_500, ActivePriceCatalogVersion: "official-usd",
			InflightRequests: 2, MaxConcurrency: 8, QueuedRequests: 1, MeteringMode: "normal",
		},
		BackgroundTasks: []BackgroundTaskHealth{{ID: "monitor", Label: "模型监控", State: "healthy"}},
	}}
	handler, err := NewServiceHandlerWithOverview(service, provider)
	if err != nil {
		t.Fatal(err)
	}

	response := perform(handler, http.MethodGet, APIPrefix+"/runtime-overview", "", nil)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("runtime overview status/headers = %d/%v, body=%s", response.Code, response.Header(), response.Body.String())
	}
	var overview RuntimeOverview
	decodeResponse(t, response, &overview)
	if overview.Runtime.ConfigRevision != 3 || overview.Runtime.MaxConcurrency != 8 ||
		len(overview.BackgroundTasks) != 1 || overview.BackgroundTasks[0].ID != "monitor" {
		t.Fatalf("runtime overview = %+v", overview)
	}

	response = perform(handler, http.MethodPost, APIPrefix+"/runtime-overview", "", nil)
	assertAPIError(t, response, http.StatusMethodNotAllowed, "method_not_allowed")

	withoutOverview, err := NewServiceHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	response = perform(withoutOverview, http.MethodGet, APIPrefix+"/runtime-overview", "", nil)
	assertAPIError(t, response, http.StatusServiceUnavailable, "runtime_overview_unavailable")
}

type staticRuntimeOverviewProvider struct {
	value RuntimeOverview
	err   error
}

func (provider staticRuntimeOverviewProvider) RuntimeOverview(context.Context) (RuntimeOverview, error) {
	return provider.value, provider.err
}

func perform(handler http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, destination any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), destination); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, response.Body.String())
	}
}

func assertAPIError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status || !strings.Contains(response.Body.String(), `"code":"`+code+`"`) {
		t.Fatalf("API error = %d %s, want %d/%s", response.Code, response.Body.String(), status, code)
	}
}
