package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/secrets"
	"github.com/LuTianTian001/JieShan/internal/store"
	"github.com/LuTianTian001/JieShan/internal/upstream"
)

type upstreamRecorder struct {
	mu    sync.Mutex
	hits  int
	order *[]string
	name  string
	serve func(http.ResponseWriter, *http.Request, int)
}

func (u *upstreamRecorder) handler(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	u.hits++
	hit := u.hits
	if u.order != nil {
		*u.order = append(*u.order, u.name)
	}
	u.mu.Unlock()
	u.serve(w, r, hit)
}

func (u *upstreamRecorder) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits
}

type gatewayFixture struct {
	store   *store.Store
	gateway *Gateway
	apiKey  string
	keyID   int64
	model   string
	route   store.Route
}

func newGatewayFixture(t *testing.T, baseURLs ...string) *gatewayFixture {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	cipher, err := secrets.LoadOrCreate(t.TempDir(), "")
	if err != nil {
		t.Fatalf("create cipher: %v", err)
	}
	encrypted, err := cipher.Encrypt("upstream-secret")
	if err != nil {
		t.Fatalf("encrypt upstream secret: %v", err)
	}

	modelIDs := make([]int64, 0, len(baseURLs))
	for index, baseURL := range baseURLs {
		upstreamID, createErr := s.CreateUpstream(ctx, store.UpstreamWrite{
			Name:          fmt.Sprintf("upstream-%d", index+1),
			Kind:          "compatible",
			BaseURL:       baseURL,
			Enabled:       true,
			CustomHeaders: json.RawMessage(`{}`),
			SecretCipher:  encrypted,
		})
		if createErr != nil {
			t.Fatalf("create upstream %d: %v", index+1, createErr)
		}
		modelName := fmt.Sprintf("provider-model-%d", index+1)
		if _, applyErr := s.ApplyDiscoveredModels(ctx, upstreamID, []string{modelName}); applyErr != nil {
			t.Fatalf("apply upstream %d models: %v", index+1, applyErr)
		}
		models, listErr := s.ListUpstreamModels(ctx, upstreamID)
		if listErr != nil || len(models) != 1 {
			t.Fatalf("list upstream %d models: models=%+v err=%v", index+1, models, listErr)
		}
		modelIDs = append(modelIDs, models[0].ID)
	}

	routeID, err := s.CreateRoute(ctx, store.RouteWrite{
		PublicModel:            "public-chat",
		Enabled:                true,
		MonitorEnabled:         true,
		MonitorIntervalSeconds: 300,
		CooldownSeconds:        300,
		FailureThreshold:       2,
		FailureWindowSeconds:   300,
		TargetModelIDs:         modelIDs,
	})
	if err != nil {
		t.Fatalf("create route: %v", err)
	}
	route, err := s.GetRoute(ctx, routeID)
	if err != nil {
		t.Fatalf("get route: %v", err)
	}

	settings, err := s.GetSettings(ctx)
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	settings.RequestDeadlineSeconds = 5
	settings.MaxAttempts = len(baseURLs)
	if _, err := s.UpdateSettings(ctx, settings); err != nil {
		t.Fatalf("update settings: %v", err)
	}

	const rawKey = "js_gateway_integration_key"
	keyID, err := s.CreateDownstreamKey(ctx, store.DownstreamKeyWrite{
		Name:          "integration",
		Enabled:       true,
		AllowedModels: []string{"public-chat"},
	}, rawKey[:12], rawKey)
	if err != nil {
		t.Fatalf("create downstream key: %v", err)
	}

	client := upstream.NewClient(s, cipher, 2*time.Second, upstream.ClientOptions{AllowPrivateUpstreams: true})
	return &gatewayFixture{store: s, gateway: New(s, client), apiKey: rawKey, keyID: keyID, model: "public-chat", route: route}
}

func (f *gatewayFixture) makeMetered(t *testing.T, model string, quota int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.store.DB.ExecContext(ctx, `UPDATE routes SET public_model=?,updated_at=? WHERE id=?`, model, store.NowMS(), f.route.ID); err != nil {
		t.Fatalf("rename route for metered test: %v", err)
	}
	rawKey := "js_gateway_metered_" + strings.ReplaceAll(model, ".", "_")
	keyID, err := f.store.CreateDownstreamKey(ctx, store.DownstreamKeyWrite{
		Name: "metered", Enabled: true, QuotaMicroUSD: &quota, AllowedModels: []string{model},
	}, rawKey[:12], rawKey)
	if err != nil {
		t.Fatalf("create metered key: %v", err)
	}
	f.apiKey, f.keyID, f.model = rawKey, keyID, model
}

func (f *gatewayFixture) chat(t *testing.T, stream bool) *httptest.ResponseRecorder {
	t.Helper()
	req := f.chatRequest(t, stream)
	recorder := httptest.NewRecorder()
	f.gateway.ChatCompletions(recorder, req)
	return recorder
}

func (f *gatewayFixture) chatRequest(t *testing.T, stream bool) *http.Request {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model":  f.model,
		"stream": stream,
		"messages": []map[string]string{{
			"role": "user", "content": "hello",
		}},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+f.apiKey)
	return req
}

func successResponse(w http.ResponseWriter, model string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "chat-success", "object": "chat.completion", "model": model,
		"choices": []map[string]any{{"index": 0, "message": map[string]string{"role": "assistant", "content": "ok"}}},
		"usage":   map[string]any{"prompt_tokens": 3, "completion_tokens": 1},
	})
}

func TestChatCompletionsUsesStrictTargetOrderForFailover(t *testing.T) {
	var order []string
	first := &upstreamRecorder{name: "first", order: &order, serve: func(w http.ResponseWriter, _ *http.Request, _ int) {
		http.Error(w, `{"error":{"message":"temporarily unavailable"}}`, http.StatusServiceUnavailable)
	}}
	second := &upstreamRecorder{name: "second", order: &order, serve: func(w http.ResponseWriter, _ *http.Request, _ int) {
		successResponse(w, "provider-model-2")
	}}
	firstServer := httptest.NewServer(http.HandlerFunc(first.handler))
	defer firstServer.Close()
	secondServer := httptest.NewServer(http.HandlerFunc(second.handler))
	defer secondServer.Close()

	fixture := newGatewayFixture(t, firstServer.URL, secondServer.URL)
	response := fixture.chat(t, false)
	if response.Code != http.StatusOK {
		t.Fatalf("expected failover success, status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Join(order, ",") != "first,second" {
		t.Fatalf("targets were not attempted in drag order: %v", order)
	}

	logs, err := fixture.store.ListRequestLogs(context.Background(), 10, 0)
	if err != nil || len(logs) != 1 {
		t.Fatalf("list request logs: logs=%+v err=%v", logs, err)
	}
	logItem, attempts, err := fixture.store.GetRequestLog(context.Background(), logs[0].ID)
	if err != nil {
		t.Fatalf("get request log: %v", err)
	}
	if logItem.Status != "success" || logItem.ActualModel != "provider-model-2" || logItem.SwitchCount != 1 {
		t.Fatalf("unexpected request log: %+v", logItem)
	}
	if len(attempts) != 2 || attempts[0].UpstreamName != "upstream-1" || attempts[1].UpstreamName != "upstream-2" {
		t.Fatalf("unexpected attempt timeline: %+v", attempts)
	}
}

func TestTwoIndependentFailuresOpenCooldownAndNextRequestSkipsTarget(t *testing.T) {
	var order []string
	first := &upstreamRecorder{name: "first", order: &order, serve: func(w http.ResponseWriter, _ *http.Request, _ int) {
		http.Error(w, "unavailable", http.StatusBadGateway)
	}}
	second := &upstreamRecorder{name: "second", order: &order, serve: func(w http.ResponseWriter, _ *http.Request, _ int) {
		successResponse(w, "provider-model-2")
	}}
	firstServer := httptest.NewServer(http.HandlerFunc(first.handler))
	defer firstServer.Close()
	secondServer := httptest.NewServer(http.HandlerFunc(second.handler))
	defer secondServer.Close()

	fixture := newGatewayFixture(t, firstServer.URL, secondServer.URL)
	for request := 0; request < 2; request++ {
		if response := fixture.chat(t, false); response.Code != http.StatusOK {
			t.Fatalf("request %d should fail over successfully: %d %s", request+1, response.Code, response.Body.String())
		}
	}
	route, err := fixture.store.GetRoute(context.Background(), fixture.route.ID)
	if err != nil {
		t.Fatalf("reload route: %v", err)
	}
	firstTarget := route.Targets[0]
	if firstTarget.CircuitPhase != "open" || firstTarget.CooldownUntil == nil || *firstTarget.CooldownUntil <= time.Now().UnixMilli() {
		t.Fatalf("second independent failure should open a future cooldown: %+v", firstTarget)
	}

	if response := fixture.chat(t, false); response.Code != http.StatusOK {
		t.Fatalf("request during cooldown should use next target: %d %s", response.Code, response.Body.String())
	}
	if first.count() != 2 || second.count() != 3 {
		t.Fatalf("cooling target was not skipped: first=%d second=%d order=%v", first.count(), second.count(), order)
	}
	wantOrder := "first,second,first,second,second"
	if strings.Join(order, ",") != wantOrder {
		t.Fatalf("unexpected request order: got=%v want=%s", order, wantOrder)
	}
}

func TestInvalidClient4xxDoesNotFailoverOrPenalizeTarget(t *testing.T) {
	first := &upstreamRecorder{name: "first", serve: func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"messages is required"}}`))
	}}
	second := &upstreamRecorder{name: "second", serve: func(w http.ResponseWriter, _ *http.Request, _ int) {
		successResponse(w, "provider-model-2")
	}}
	firstServer := httptest.NewServer(http.HandlerFunc(first.handler))
	defer firstServer.Close()
	secondServer := httptest.NewServer(http.HandlerFunc(second.handler))
	defer secondServer.Close()

	fixture := newGatewayFixture(t, firstServer.URL, secondServer.URL)
	response := fixture.chat(t, false)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid request should be returned unchanged: %d %s", response.Code, response.Body.String())
	}
	if first.count() != 1 || second.count() != 0 {
		t.Fatalf("invalid 4xx must not fail over: first=%d second=%d", first.count(), second.count())
	}
	route, err := fixture.store.GetRoute(context.Background(), fixture.route.ID)
	if err != nil {
		t.Fatalf("reload route: %v", err)
	}
	if route.Targets[0].CircuitPhase != "closed" || route.Targets[0].ConsecutiveFails != 0 || route.Targets[0].CooldownUntil != nil {
		t.Fatalf("invalid 4xx penalized target health: %+v", route.Targets[0])
	}
}

func TestStreamingFailsOverBeforeFirstSemanticOutput(t *testing.T) {
	var order []string
	first := &upstreamRecorder{name: "first", order: &order, serve: func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(": keepalive\n\ndata: {\"choices\":[{\"delta\":{}}]}\n\n"))
	}}
	second := &upstreamRecorder{name: "second", order: &order, serve: func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n"))
	}}
	firstServer := httptest.NewServer(http.HandlerFunc(first.handler))
	defer firstServer.Close()
	secondServer := httptest.NewServer(http.HandlerFunc(second.handler))
	defer secondServer.Close()

	fixture := newGatewayFixture(t, firstServer.URL, secondServer.URL)
	response := fixture.chat(t, true)
	if response.Code != http.StatusOK {
		t.Fatalf("expected streaming failover success: %d %s", response.Code, response.Body.String())
	}
	if strings.Join(order, ",") != "first,second" {
		t.Fatalf("unexpected stream target order: %v", order)
	}
	body := response.Body.String()
	if strings.Contains(body, "keepalive") || !strings.Contains(body, `"content":"hello"`) {
		t.Fatalf("pre-commit bytes leaked or semantic output missing: %q", body)
	}
}

func TestNonStreamingInvalidSuccessResponsesFailOver(t *testing.T) {
	cases := []struct {
		name  string
		serve func(http.ResponseWriter)
	}{
		{name: "empty", serve: func(w http.ResponseWriter) { w.WriteHeader(http.StatusOK) }},
		{name: "html", serve: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>login</html>"))
		}},
		{name: "error envelope", serve: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"error":{"message":"provider unavailable"}}`))
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var order []string
			first := &upstreamRecorder{name: "first", order: &order, serve: func(w http.ResponseWriter, _ *http.Request, _ int) { test.serve(w) }}
			second := &upstreamRecorder{name: "second", order: &order, serve: func(w http.ResponseWriter, _ *http.Request, _ int) { successResponse(w, "provider-model-2") }}
			firstServer := httptest.NewServer(http.HandlerFunc(first.handler))
			defer firstServer.Close()
			secondServer := httptest.NewServer(http.HandlerFunc(second.handler))
			defer secondServer.Close()

			fixture := newGatewayFixture(t, firstServer.URL, secondServer.URL)
			response := fixture.chat(t, false)
			if response.Code != http.StatusOK || strings.Join(order, ",") != "first,second" {
				t.Fatalf("invalid 2xx did not fail over: status=%d order=%v body=%s", response.Code, order, response.Body.String())
			}
		})
	}
}

func TestPaymentRequiredFailsOver(t *testing.T) {
	var order []string
	first := &upstreamRecorder{name: "first", order: &order, serve: func(w http.ResponseWriter, _ *http.Request, _ int) {
		http.Error(w, `{"error":{"message":"insufficient balance"}}`, http.StatusPaymentRequired)
	}}
	second := &upstreamRecorder{name: "second", order: &order, serve: func(w http.ResponseWriter, _ *http.Request, _ int) {
		successResponse(w, "provider-model-2")
	}}
	firstServer := httptest.NewServer(http.HandlerFunc(first.handler))
	defer firstServer.Close()
	secondServer := httptest.NewServer(http.HandlerFunc(second.handler))
	defer secondServer.Close()

	fixture := newGatewayFixture(t, firstServer.URL, secondServer.URL)
	response := fixture.chat(t, false)
	if response.Code != http.StatusOK || strings.Join(order, ",") != "first,second" {
		t.Fatalf("402 did not fail over: status=%d order=%v body=%s", response.Code, order, response.Body.String())
	}
}

func TestForbiddenCoolsOnlyTargetAndKeepsCredentialActive(t *testing.T) {
	first := &upstreamRecorder{serve: func(w http.ResponseWriter, _ *http.Request, _ int) {
		http.Error(w, `{"error":{"message":"permission denied"}}`, http.StatusForbidden)
	}}
	second := &upstreamRecorder{serve: func(w http.ResponseWriter, _ *http.Request, _ int) {
		successResponse(w, "provider-model-2")
	}}
	firstServer := httptest.NewServer(http.HandlerFunc(first.handler))
	defer firstServer.Close()
	secondServer := httptest.NewServer(http.HandlerFunc(second.handler))
	defer secondServer.Close()

	fixture := newGatewayFixture(t, firstServer.URL, secondServer.URL)
	for attempt := 0; attempt < 2; attempt++ {
		if response := fixture.chat(t, false); response.Code != http.StatusOK {
			t.Fatalf("request %d status=%d body=%s", attempt+1, response.Code, response.Body.String())
		}
	}
	route, err := fixture.store.GetRoute(context.Background(), fixture.route.ID)
	if err != nil {
		t.Fatal(err)
	}
	if route.Targets[0].CredentialState != "active" || route.Targets[0].CircuitPhase != "open" {
		t.Fatalf("403 should cool only the target: %+v", route.Targets[0])
	}
}

func TestAttemptTimeoutLeavesBudgetForNextTarget(t *testing.T) {
	var order []string
	first := &upstreamRecorder{name: "first", order: &order, serve: func(_ http.ResponseWriter, r *http.Request, _ int) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}}
	second := &upstreamRecorder{name: "second", order: &order, serve: func(w http.ResponseWriter, _ *http.Request, _ int) {
		successResponse(w, "provider-model-2")
	}}
	firstServer := httptest.NewServer(http.HandlerFunc(first.handler))
	defer firstServer.Close()
	secondServer := httptest.NewServer(http.HandlerFunc(second.handler))
	defer secondServer.Close()

	fixture := newGatewayFixture(t, firstServer.URL, secondServer.URL)
	settings, err := fixture.store.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.RequestDeadlineSeconds = 2
	settings.MaxAttempts = 2
	if _, err := fixture.store.UpdateSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	response := fixture.chat(t, false)
	if response.Code != http.StatusOK || strings.Join(order, ",") != "first,second" {
		t.Fatalf("timeout did not preserve failover budget: status=%d order=%v body=%s", response.Code, order, response.Body.String())
	}
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("request exhausted total deadline before failover: %s", elapsed)
	}
	logs, err := fixture.store.ListRequestLogs(context.Background(), 1, 0)
	if err != nil || len(logs) != 1 {
		t.Fatalf("logs=%+v err=%v", logs, err)
	}
	_, attempts, err := fixture.store.GetRequestLog(context.Background(), logs[0].ID)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts=%+v err=%v", attempts, err)
	}
	if attempts[0].CreatedAt-logs[0].StartedAt > 250 {
		t.Fatalf("attempt timestamp is completion time, not start time: request=%d attempt=%d", logs[0].StartedAt, attempts[0].CreatedAt)
	}
}

type failingStreamWriter struct {
	header http.Header
	status int
}

func (w *failingStreamWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *failingStreamWriter) WriteHeader(status int) { w.status = status }
func (w *failingStreamWriter) Write([]byte) (int, error) {
	return 0, errors.New("downstream disconnected")
}
func (w *failingStreamWriter) Flush() {}

func TestDownstreamWriteFailureDoesNotPenalizeTarget(t *testing.T) {
	stream := &upstreamRecorder{serve: func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n"))
	}}
	server := httptest.NewServer(http.HandlerFunc(stream.handler))
	defer server.Close()

	fixture := newGatewayFixture(t, server.URL)
	writer := &failingStreamWriter{}
	fixture.gateway.ChatCompletions(writer, fixture.chatRequest(t, true))
	route, err := fixture.store.GetRoute(context.Background(), fixture.route.ID)
	if err != nil {
		t.Fatal(err)
	}
	if route.Targets[0].CircuitPhase != "closed" || route.Targets[0].ConsecutiveFails != 0 {
		t.Fatalf("downstream failure penalized upstream: %+v", route.Targets[0])
	}
}

func TestBuildFailureEntersTargetHealthState(t *testing.T) {
	first := &upstreamRecorder{serve: func(w http.ResponseWriter, _ *http.Request, _ int) { successResponse(w, "provider-model-1") }}
	second := &upstreamRecorder{serve: func(w http.ResponseWriter, _ *http.Request, _ int) { successResponse(w, "provider-model-2") }}
	firstServer := httptest.NewServer(http.HandlerFunc(first.handler))
	defer firstServer.Close()
	secondServer := httptest.NewServer(http.HandlerFunc(second.handler))
	defer secondServer.Close()

	fixture := newGatewayFixture(t, firstServer.URL, secondServer.URL)
	if _, err := fixture.store.DB.Exec(`UPDATE upstream_credentials SET secret_cipher=x'00' WHERE id=?`, fixture.route.Targets[0].CredentialID); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if response := fixture.chat(t, false); response.Code != http.StatusOK {
			t.Fatalf("request %d status=%d body=%s", attempt+1, response.Code, response.Body.String())
		}
	}
	route, err := fixture.store.GetRoute(context.Background(), fixture.route.ID)
	if err != nil {
		t.Fatal(err)
	}
	if route.Targets[0].CircuitPhase != "open" || first.count() != 0 || second.count() != 2 {
		t.Fatalf("build failures were not cooled: target=%+v first=%d second=%d", route.Targets[0], first.count(), second.count())
	}
}

func TestProbeUsesMinimalCompatiblePayload(t *testing.T) {
	var payload map[string]any
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode probe: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if _, exists := payload["max_tokens"]; exists {
			http.Error(w, "max_tokens unsupported", http.StatusBadRequest)
			return
		}
		successResponse(w, "provider-model-1")
	}))
	defer upstreamServer.Close()

	fixture := newGatewayFixture(t, upstreamServer.URL)
	cells, err := fixture.gateway.ProbeRoute(context.Background(), fixture.route.ID, nil)
	if err != nil || len(cells) != 1 || cells[0].Status != "healthy" {
		t.Fatalf("probe result=%+v err=%v payload=%+v", cells, err, payload)
	}
}

func TestParseUsageRemovesCachedAndReasoningSubsets(t *testing.T) {
	_, parsed := parseUsage([]byte(`{
  "model":"gpt-5.6-nano",
  "usage":{
    "prompt_tokens":100,
    "completion_tokens":50,
    "prompt_tokens_details":{"cached_tokens":40},
    "completion_tokens_details":{"reasoning_tokens":20}
  }
}`), "fallback")
	if !parsed.complete() {
		t.Fatalf("usage was not recognized: %+v", parsed)
	}
	if *parsed.Input != 60 || *parsed.CacheRead != 40 || *parsed.Output != 30 || *parsed.Reasoning != 20 {
		t.Fatalf("usage was double counted: %+v", parsed)
	}
}

func TestMeteredFailureReleasesReservation(t *testing.T) {
	failed := &upstreamRecorder{serve: func(w http.ResponseWriter, _ *http.Request, _ int) {
		http.Error(w, `{"error":{"message":"unavailable"}}`, http.StatusServiceUnavailable)
	}}
	server := httptest.NewServer(http.HandlerFunc(failed.handler))
	defer server.Close()

	fixture := newGatewayFixture(t, server.URL)
	fixture.makeMetered(t, "gpt-5.6-nano", 10_000)
	response := fixture.chat(t, false)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	key, err := fixture.store.GetDownstreamKey(context.Background(), fixture.keyID)
	if err != nil {
		t.Fatal(err)
	}
	if key.ReservedMicroUSD != 0 || key.UsedMicroUSD != 0 {
		t.Fatalf("failed gateway request leaked reservation: %+v", key)
	}
	var reserves, releases int
	if err := fixture.store.DB.QueryRow(`SELECT COUNT(*) FROM quota_ledger WHERE downstream_key_id=? AND entry_type='reserve'`, fixture.keyID).Scan(&reserves); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.DB.QueryRow(`SELECT COUNT(*) FROM quota_ledger WHERE downstream_key_id=? AND entry_type='release'`, fixture.keyID).Scan(&releases); err != nil {
		t.Fatal(err)
	}
	if reserves != 1 || releases != 1 {
		t.Fatalf("reserve entries=%d release entries=%d", reserves, releases)
	}
}

func TestMeteredStreamWithoutUsageUsesConservativeSettlement(t *testing.T) {
	stream := &upstreamRecorder{serve: func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"model\":\"gpt-5.6-nano\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n"))
	}}
	server := httptest.NewServer(http.HandlerFunc(stream.handler))
	defer server.Close()

	fixture := newGatewayFixture(t, server.URL)
	fixture.makeMetered(t, "gpt-5.6-nano", 10_000)
	response := fixture.chat(t, true)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	key, err := fixture.store.GetDownstreamKey(context.Background(), fixture.keyID)
	if err != nil {
		t.Fatal(err)
	}
	if key.ReservedMicroUSD != 0 || key.UsedMicroUSD <= 0 {
		t.Fatalf("missing stream usage was free or left reserved: %+v", key)
	}
	logs, err := fixture.store.ListRequestLogs(context.Background(), 10, 0)
	if err != nil || len(logs) != 1 {
		t.Fatalf("logs=%+v err=%v", logs, err)
	}
	if logs[0].CostMicroUSD <= 0 || !strings.Contains(logs[0].ErrorMessage, "conservative reservation estimate") || logs[0].InputTokens == nil || logs[0].OutputTokens == nil {
		t.Fatalf("fallback settlement was not auditable: %+v", logs[0])
	}
}
