package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/accountsync"
	"github.com/LuTianTian001/JieShan/internal/auth"
	"github.com/LuTianTian001/JieShan/internal/config"
	"github.com/LuTianTian001/JieShan/internal/gateway"
	"github.com/LuTianTian001/JieShan/internal/secrets"
	"github.com/LuTianTian001/JieShan/internal/store"
	"github.com/LuTianTian001/JieShan/internal/upstream"
)

type contractUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type contractDashboard struct {
	MonitoredModels  int     `json:"monitoredModels"`
	HealthyModels    int     `json:"healthyModels"`
	AttentionTargets int     `json:"attentionTargets"`
	CoolingTargets   int     `json:"coolingTargets"`
	SuccessRate24H   float64 `json:"successRate24h"`
	Requests24H      int     `json:"requests24h"`
}

type contractUpstreamModel struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Enabled      bool   `json:"enabled"`
	DiscoveredAt string `json:"discoveredAt"`
}

type contractUpstream struct {
	ID              int64                   `json:"id"`
	Name            string                  `json:"name"`
	BaseURL         string                  `json:"baseUrl"`
	Protocol        string                  `json:"protocol"`
	Enabled         bool                    `json:"enabled"`
	State           string                  `json:"state"`
	LatencyMS       *int64                  `json:"latencyMs"`
	ModelCount      int                     `json:"modelCount"`
	CredentialCount int                     `json:"credentialCount"`
	LastSyncAt      *string                 `json:"lastSyncAt"`
	Models          []contractUpstreamModel `json:"models"`
}

type contractDiscovery struct {
	UpstreamID   int64    `json:"upstreamId"`
	DiscoveredAt string   `json:"discoveredAt"`
	Added        []string `json:"added"`
	Removed      []string `json:"removed"`
	Unchanged    []string `json:"unchanged"`
	Complete     bool     `json:"complete"`
}

type contractRouteTarget struct {
	ID             int64   `json:"id"`
	UpstreamID     int64   `json:"upstreamId"`
	UpstreamName   string  `json:"upstreamName"`
	CredentialName string  `json:"credentialName"`
	SourceModel    string  `json:"sourceModel"`
	State          string  `json:"state"`
	LatencyMS      *int64  `json:"latencyMs"`
	CooldownUntil  *string `json:"cooldownUntil"`
	LastFailure    string  `json:"lastFailure"`
}

type contractRoute struct {
	ID          int64                 `json:"id"`
	Model       string                `json:"model"`
	DisplayName string                `json:"displayName"`
	Enabled     bool                  `json:"enabled"`
	Monitored   bool                  `json:"monitored"`
	Revision    int64                 `json:"revision"`
	Targets     []contractRouteTarget `json:"targets"`
}

type contractMonitor struct {
	GeneratedAt          string          `json:"generatedAt"`
	ProbeIntervalSeconds int             `json:"probeIntervalSeconds"`
	Routes               []contractRoute `json:"routes"`
}

type contractKey struct {
	ID            int64    `json:"id"`
	Name          string   `json:"name"`
	Prefix        string   `json:"prefix"`
	Enabled       bool     `json:"enabled"`
	QuotaUSD      *float64 `json:"quotaUsd"`
	SpentUSD      float64  `json:"spentUsd"`
	AllowedModels []string `json:"allowedModels"`
	RPMLimit      *int     `json:"rpmLimit"`
	ExpiresAt     *string  `json:"expiresAt"`
	LastUsedAt    *string  `json:"lastUsedAt"`
	CreatedAt     string   `json:"createdAt"`
}

type contractAttempt struct {
	ID           int64  `json:"id"`
	Sequence     int    `json:"sequence"`
	UpstreamName string `json:"upstreamName"`
	Model        string `json:"model"`
	State        string `json:"state"`
	StartedAt    string `json:"startedAt"`
	DurationMS   int64  `json:"durationMs"`
	TTFTMS       *int64 `json:"ttftMs"`
	StatusCode   *int   `json:"statusCode"`
	SwitchReason string `json:"switchReason"`
	Error        string `json:"error"`
}

type contractLog struct {
	ID              string            `json:"id"`
	StartedAt       string            `json:"startedAt"`
	KeyName         string            `json:"keyName"`
	RequestedModel  string            `json:"requestedModel"`
	ActualModel     string            `json:"actualModel"`
	Status          string            `json:"status"`
	DurationMS      int64             `json:"durationMs"`
	TTFTMS          *int64            `json:"ttftMs"`
	InputTokens     int64             `json:"inputTokens"`
	CacheTokens     int64             `json:"cacheTokens"`
	OutputTokens    int64             `json:"outputTokens"`
	ReasoningTokens int64             `json:"reasoningTokens"`
	CostUSD         float64           `json:"costUsd"`
	SwitchCount     int               `json:"switchCount"`
	ReasoningEffort string            `json:"reasoningEffort"`
	ThinkingBudget  *int64            `json:"thinkingBudget"`
	Attempts        []contractAttempt `json:"attempts"`
}

type contractSettings struct {
	ProbeIntervalSeconds  int     `json:"probeIntervalSeconds"`
	FailureThreshold      int     `json:"failureThreshold"`
	CooldownSeconds       int     `json:"cooldownSeconds"`
	RequestTimeoutSeconds int     `json:"requestTimeoutSeconds"`
	MaxAttempts           int     `json:"maxAttempts"`
	LogRetentionDays      int     `json:"logRetentionDays"`
	PriceCatalogVersion   string  `json:"priceCatalogVersion"`
	PriceCatalogUpdatedAt string  `json:"priceCatalogUpdatedAt"`
	PriceCatalogSource    string  `json:"priceCatalogSource"`
	LastBackupAt          *string `json:"lastBackupAt"`
}

type apiContractFixture struct {
	client *http.Client
	server *httptest.Server
	store  *store.Store
}

func newAPIContractFixture(t *testing.T) *apiContractFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	s, err := store.Open(ctx, filepath.Join(root, "jieshan.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cipher, err := secrets.LoadOrCreate(filepath.Join(root, "secrets"), "")
	if err != nil {
		_ = s.Close()
		t.Fatalf("create cipher: %v", err)
	}
	authService := auth.New(s, time.Hour)
	if _, err := authService.EnsureAdmin(ctx, "correct horse battery staple"); err != nil {
		_ = s.Close()
		t.Fatalf("ensure admin: %v", err)
	}
	upstreamClient := upstream.NewClient(s, cipher, 2*time.Second, upstream.ClientOptions{AllowPrivateUpstreams: true})
	gatewayService := gateway.New(s, upstreamClient)
	accountHTTP := upstream.NewHTTPClient(2*time.Second, upstream.ClientOptions{AllowPrivateUpstreams: true})
	accountHTTP.Timeout = 2 * time.Second
	accountService := accountsync.New(s, cipher, accountHTTP, nil, accountsync.DefaultInterval)
	handler := New(config.Config{WebDir: filepath.Join(root, "missing-web"), SessionTTL: time.Hour, UpstreamTimeout: 2 * time.Second}, s, authService, cipher, upstreamClient, gatewayService, accountService).Handler()
	server := httptest.NewServer(handler)
	jar, err := cookiejar.New(nil)
	if err != nil {
		server.Close()
		_ = s.Close()
		t.Fatalf("create cookie jar: %v", err)
	}
	fixture := &apiContractFixture{client: &http.Client{Jar: jar, Timeout: 5 * time.Second}, server: server, store: s}
	t.Cleanup(func() {
		server.Close()
		_ = s.Close()
	})
	return fixture
}

func (f *apiContractFixture) request(t *testing.T, method, path string, input any, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			t.Fatalf("marshal %s %s: %v", method, path, err)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, f.server.URL+path, body)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("perform %s %s: %v", method, path, err)
	}
	payload, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read %s %s response: %v", method, path, err)
	}
	return resp, payload
}

func requireStatus(t *testing.T, resp *http.Response, body []byte, want int) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("unexpected status: got=%d want=%d body=%s", resp.StatusCode, want, body)
	}
}

func decodeContract[T any](t *testing.T, body []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode response %T: %v body=%s", value, err, body)
	}
	return value
}

func requireRFC3339(t *testing.T, field, value string) {
	t.Helper()
	if value == "" {
		t.Fatalf("%s must be present", field)
	}
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		t.Fatalf("%s must be RFC3339, got %q: %v", field, value, err)
	}
}

func TestFrontendAPIContractEndToEnd(t *testing.T) {
	var modelRequests int
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			modelRequests++
			model := ""
			switch r.Header.Get("Authorization") {
			case "Bearer upstream-key-alpha":
				model = "provider-alpha"
			case "Bearer upstream-key-beta":
				model = "provider-beta"
			default:
				http.Error(w, "bad upstream credential", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []map[string]any{{"id": model}}})
		case "/v1/chat/completions":
			var input struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "chat-contract", "object": "chat.completion", "model": input.Model,
				"choices": []map[string]any{{"index": 0, "message": map[string]string{"role": "assistant", "content": "ok"}}},
				"usage": map[string]any{
					"prompt_tokens": 7, "completion_tokens": 3,
					"prompt_tokens_details":     map[string]any{"cached_tokens": 2},
					"completion_tokens_details": map[string]any{"reasoning_tokens": 1},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()

	fixture := newAPIContractFixture(t)

	t.Run("login session", func(t *testing.T) {
		resp, body := fixture.request(t, http.MethodGet, "/api/v1/me", nil, nil)
		requireStatus(t, resp, body, http.StatusUnauthorized)

		resp, body = fixture.request(t, http.MethodPost, "/api/v1/auth/login", map[string]any{"password": "wrong"}, nil)
		requireStatus(t, resp, body, http.StatusUnauthorized)

		resp, body = fixture.request(t, http.MethodPost, "/api/v1/auth/login", map[string]any{"password": "correct horse battery staple"}, nil)
		requireStatus(t, resp, body, http.StatusOK)
		login := decodeContract[struct {
			User contractUser `json:"user"`
		}](t, body)
		if login.User.ID <= 0 || login.User.Username != "admin" {
			t.Fatalf("unexpected login DTO: %+v", login)
		}
		cookies := resp.Cookies()
		if len(cookies) != 1 || cookies[0].Name != auth.CookieName || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
			t.Fatalf("session cookie is not hardened as expected: %+v", cookies)
		}

		resp, body = fixture.request(t, http.MethodGet, "/api/v1/me", nil, nil)
		requireStatus(t, resp, body, http.StatusOK)
		me := decodeContract[struct {
			User contractUser `json:"user"`
		}](t, body)
		if me.User != login.User {
			t.Fatalf("session user mismatch: login=%+v me=%+v", login.User, me.User)
		}
	})

	createUpstream := func(t *testing.T, name, apiKey string) contractUpstream {
		t.Helper()
		resp, body := fixture.request(t, http.MethodPost, "/api/v1/upstreams", map[string]any{
			"name": name, "baseUrl": provider.URL, "protocol": "compatible", "apiKey": apiKey,
		}, nil)
		requireStatus(t, resp, body, http.StatusCreated)
		envelope := decodeContract[struct {
			Item contractUpstream `json:"item"`
		}](t, body)
		item := envelope.Item
		if item.ID <= 0 || item.Name != name || item.BaseURL != provider.URL || item.Protocol != "compatible" || !item.Enabled {
			t.Fatalf("unexpected upstream DTO: %+v", item)
		}
		if item.State == "" || item.CredentialCount != 1 || item.ModelCount != 0 || item.Models == nil {
			t.Fatalf("upstream derived fields missing: %+v", item)
		}
		return item
	}

	t.Run("legacy management token is rejected", func(t *testing.T) {
		resp, body := fixture.request(t, http.MethodPost, "/api/v1/upstreams", map[string]any{
			"name": "Legacy", "baseUrl": provider.URL, "protocol": "compatible",
			"apiKey": "legacy-inference-key", "managementToken": "ambiguous-legacy-token",
		}, nil)
		requireStatus(t, resp, body, http.StatusBadRequest)
		if !bytes.Contains(body, []byte("unknown field")) {
			t.Fatalf("legacy managementToken rejection is not explicit: %s", body)
		}
	})

	discoverAndApply := func(t *testing.T, item contractUpstream, wantModel string) contractUpstream {
		t.Helper()
		resp, body := fixture.request(t, http.MethodPost, fmt.Sprintf("/api/v1/upstreams/%d/models/discover", item.ID), nil, nil)
		requireStatus(t, resp, body, http.StatusOK)
		discovery := decodeContract[contractDiscovery](t, body)
		if discovery.UpstreamID != item.ID || !discovery.Complete || len(discovery.Added) != 1 || discovery.Added[0] != wantModel || len(discovery.Removed) != 0 {
			t.Fatalf("unexpected staged discovery: %+v", discovery)
		}
		requireRFC3339(t, "discoveredAt", discovery.DiscoveredAt)

		resp, body = fixture.request(t, http.MethodGet, "/api/v1/upstreams", nil, nil)
		requireStatus(t, resp, body, http.StatusOK)
		before := decodeContract[struct {
			Items []contractUpstream `json:"items"`
		}](t, body)
		for _, upstreamItem := range before.Items {
			if upstreamItem.ID == item.ID && upstreamItem.ModelCount != 0 {
				t.Fatalf("discovery mutated models before apply: %+v", upstreamItem)
			}
		}

		resp, body = fixture.request(t, http.MethodPost, fmt.Sprintf("/api/v1/upstreams/%d/models/apply", item.ID), map[string]any{"discovery": discovery}, nil)
		requireStatus(t, resp, body, http.StatusOK)
		applied := decodeContract[struct {
			Item contractUpstream `json:"item"`
		}](t, body).Item
		if applied.ModelCount != 1 || len(applied.Models) != 1 || applied.Models[0].Name != wantModel || !applied.Models[0].Enabled {
			t.Fatalf("applied upstream does not expose models: %+v", applied)
		}
		if applied.Models[0].ID == "" {
			t.Fatalf("model id must be serialized for stable UI identity: %+v", applied.Models[0])
		}
		requireRFC3339(t, "models[0].discoveredAt", applied.Models[0].DiscoveredAt)
		if applied.LastSyncAt == nil {
			t.Fatalf("apply must expose lastSyncAt: %+v", applied)
		}
		requireRFC3339(t, "lastSyncAt", *applied.LastSyncAt)
		return applied
	}

	alpha := createUpstream(t, "Alpha", "upstream-key-alpha")
	beta := createUpstream(t, "Beta", "upstream-key-beta")
	t.Run("upstream account lifecycle", func(t *testing.T) {
		resp, body := fixture.request(t, http.MethodGet, "/api/v1/account-adapters", nil, nil)
		requireStatus(t, resp, body, http.StatusOK)
		adapters := decodeContract[struct {
			Items []accountsync.AdapterDescriptor `json:"items"`
		}](t, body).Items
		if len(adapters) != 3 {
			t.Fatalf("expected three account adapters, got %+v", adapters)
		}

		accountPath := fmt.Sprintf("/api/v1/upstreams/%d/account", alpha.ID)
		resp, body = fixture.request(t, http.MethodGet, accountPath, nil, nil)
		requireStatus(t, resp, body, http.StatusOK)
		initial := decodeContract[struct {
			Account accountsync.AccountView `json:"account"`
		}](t, body).Account
		if initial.Configured || initial.Sync.State != "unconfigured" || initial.DashboardURL != provider.URL {
			t.Fatalf("unexpected unconfigured account DTO: %+v", initial)
		}

		const managementSecret = "management-token-must-not-be-returned"
		resp, body = fixture.request(t, http.MethodPut, accountPath, map[string]any{
			"adapterKey": "new_api", "dashboardUrl": provider.URL, "enabled": true, "refreshNow": false,
			"auth": map[string]any{"kind": "api_token", "apiToken": managementSecret},
		}, nil)
		requireStatus(t, resp, body, http.StatusOK)
		if bytes.Contains(body, []byte(managementSecret)) {
			t.Fatal("account response leaked the management token")
		}
		configured := decodeContract[struct {
			Account accountsync.AccountView `json:"account"`
		}](t, body).Account
		if !configured.Configured || configured.Adapter == nil || configured.Adapter.Key != "new_api" || configured.Auth == nil || !configured.Auth.HasAPIToken {
			t.Fatalf("unexpected configured account DTO: %+v", configured)
		}

		resp, body = fixture.request(t, http.MethodGet, accountPath+"/usage?range=7d&limit=50", nil, nil)
		requireStatus(t, resp, body, http.StatusOK)
		usage := decodeContract[accountsync.UsagePageView](t, body)
		if usage.Range != "7d" || len(usage.Items) != 0 {
			t.Fatalf("unexpected empty account usage DTO: %+v", usage)
		}

		resp, body = fixture.request(t, http.MethodDelete, accountPath, nil, nil)
		requireStatus(t, resp, body, http.StatusNoContent)
		resp, body = fixture.request(t, http.MethodGet, accountPath, nil, nil)
		requireStatus(t, resp, body, http.StatusOK)
		removed := decodeContract[struct {
			Account accountsync.AccountView `json:"account"`
		}](t, body).Account
		if removed.Configured {
			t.Fatalf("account should be unconfigured after delete: %+v", removed)
		}
	})
	alpha = discoverAndApply(t, alpha, "provider-alpha")
	beta = discoverAndApply(t, beta, "provider-beta")
	if modelRequests != 2 {
		t.Fatalf("discovery should only call provider model-list endpoints once each, got %d", modelRequests)
	}
	resp, body := fixture.request(t, http.MethodGet, "/api/v1/upstreams", nil, nil)
	requireStatus(t, resp, body, http.StatusOK)
	reloadedUpstreams := decodeContract[struct {
		Items []contractUpstream `json:"items"`
	}](t, body).Items
	if len(reloadedUpstreams) != 2 {
		t.Fatalf("expected two upstreams after reload, got %+v", reloadedUpstreams)
	}
	for _, item := range reloadedUpstreams {
		if item.ModelCount != 1 || len(item.Models) != 1 {
			t.Fatalf("upstream list must retain embedded models for route editing: %+v", item)
		}
	}

	resp, body = fixture.request(t, http.MethodPost, "/api/v1/routes", map[string]any{
		"model": "public-chat", "displayName": "Public Chat", "monitored": true,
		"targets": []map[string]any{
			{"upstreamId": alpha.ID, "sourceModel": "provider-alpha"},
			{"upstreamId": beta.ID, "sourceModel": "provider-beta"},
		},
	}, nil)
	requireStatus(t, resp, body, http.StatusCreated)
	route := decodeContract[struct {
		Item contractRoute `json:"item"`
	}](t, body).Item
	if route.ID <= 0 || route.Model != "public-chat" || route.DisplayName != "Public Chat" || !route.Enabled || !route.Monitored || len(route.Targets) != 2 {
		t.Fatalf("unexpected route DTO: %+v", route)
	}
	if route.Targets[0].UpstreamID != alpha.ID || route.Targets[0].SourceModel != "provider-alpha" || route.Targets[1].UpstreamID != beta.ID {
		t.Fatalf("route targets did not preserve submitted order: %+v", route.Targets)
	}
	for _, target := range route.Targets {
		if target.CredentialName == "" || target.State == "" {
			t.Fatalf("route target display fields missing: %+v", target)
		}
	}

	initialRevision := route.Revision
	reversedIDs := []int64{route.Targets[1].ID, route.Targets[0].ID}
	resp, body = fixture.request(t, http.MethodPut, fmt.Sprintf("/api/v1/routes/%d/targets/order", route.ID), map[string]any{"targetIds": reversedIDs}, nil)
	requireStatus(t, resp, body, http.StatusOK)
	route = decodeContract[struct {
		Item contractRoute `json:"item"`
	}](t, body).Item
	if route.Revision <= initialRevision || len(route.Targets) != 2 || route.Targets[0].ID != reversedIDs[0] || route.Targets[1].ID != reversedIDs[1] {
		t.Fatalf("route reorder was not persisted: %+v", route)
	}

	resp, body = fixture.request(t, http.MethodPost, "/api/v1/keys", map[string]any{
		"name": "Personal", "quotaUsd": nil, "allowedModels": []string{"public-chat"}, "rpmLimit": 30, "expiresAt": nil,
	}, nil)
	requireStatus(t, resp, body, http.StatusCreated)
	createdKey := decodeContract[struct {
		Item   contractKey `json:"item"`
		Secret string      `json:"secret"`
	}](t, body)
	if createdKey.Secret == "" || !strings.HasPrefix(createdKey.Secret, "js_") {
		t.Fatalf("downstream secret must be shown once, got %q", createdKey.Secret)
	}
	if createdKey.Item.Name != "Personal" || createdKey.Item.Prefix == "" || createdKey.Item.QuotaUSD != nil || createdKey.Item.SpentUSD != 0 {
		t.Fatalf("unexpected downstream key DTO: %+v", createdKey.Item)
	}
	if createdKey.Item.RPMLimit == nil || *createdKey.Item.RPMLimit != 30 || len(createdKey.Item.AllowedModels) != 1 {
		t.Fatalf("downstream key limits missing: %+v", createdKey.Item)
	}
	requireRFC3339(t, "key.createdAt", createdKey.Item.CreatedAt)

	resp, body = fixture.request(t, http.MethodGet, "/api/v1/keys", nil, nil)
	requireStatus(t, resp, body, http.StatusOK)
	keys := decodeContract[struct {
		Items []contractKey `json:"items"`
	}](t, body).Items
	if len(keys) != 1 || keys[0].Prefix != createdKey.Item.Prefix || bytes.Contains(body, []byte(createdKey.Secret)) {
		t.Fatalf("key list contract or one-time secret handling is wrong: items=%+v body=%s", keys, body)
	}

	resp, body = fixture.request(t, http.MethodPost, "/v1/chat/completions", map[string]any{
		"model": "public-chat", "messages": []map[string]string{{"role": "user", "content": "hello"}},
		"reasoning_effort": "high", "thinking_budget": 128,
	}, map[string]string{"Authorization": "Bearer " + createdKey.Secret})
	requireStatus(t, resp, body, http.StatusOK)
	var chatResponse struct {
		Model string `json:"model"`
	}
	chatResponse = decodeContract[struct {
		Model string `json:"model"`
	}](t, body)
	if chatResponse.Model != "provider-beta" {
		t.Fatalf("reordered first target was not used, model=%q body=%s", chatResponse.Model, body)
	}

	resp, body = fixture.request(t, http.MethodGet, "/api/v1/logs/requests", nil, nil)
	requireStatus(t, resp, body, http.StatusOK)
	logs := decodeContract[struct {
		Items []contractLog `json:"items"`
	}](t, body).Items
	if len(logs) != 1 {
		t.Fatalf("expected one request log, got %+v", logs)
	}
	logItem := logs[0]
	if logItem.ID == "" || logItem.Status != "success" || logItem.KeyName != "Personal" || logItem.RequestedModel != "public-chat" || logItem.ActualModel != "provider-beta" {
		t.Fatalf("unexpected log DTO: %+v", logItem)
	}
	if logItem.ReasoningEffort != "high" || logItem.ThinkingBudget == nil || *logItem.ThinkingBudget != 128 || logItem.InputTokens != 5 || logItem.CacheTokens != 2 || logItem.OutputTokens != 2 || logItem.ReasoningTokens != 1 {
		t.Fatalf("reasoning or token details missing from log: %+v", logItem)
	}
	requireRFC3339(t, "log.startedAt", logItem.StartedAt)

	resp, body = fixture.request(t, http.MethodGet, "/api/v1/logs/requests/"+logItem.ID, nil, nil)
	requireStatus(t, resp, body, http.StatusOK)
	logDetail := decodeContract[contractLog](t, body)
	if logDetail.ID != logItem.ID || len(logDetail.Attempts) != 1 || logDetail.Attempts[0].Sequence != 1 || logDetail.Attempts[0].UpstreamName != "Beta" || logDetail.Attempts[0].Model != "provider-beta" || logDetail.Attempts[0].State != "success" {
		t.Fatalf("attempt timeline contract mismatch: %+v", logDetail)
	}
	requireRFC3339(t, "attempt.startedAt", logDetail.Attempts[0].StartedAt)

	resp, body = fixture.request(t, http.MethodPost, "/api/v1/keys", map[string]any{
		"name": "Metered unpriced", "quotaUsd": 1.0, "allowedModels": []string{"public-chat"}, "rpmLimit": 0, "expiresAt": nil,
	}, nil)
	requireStatus(t, resp, body, http.StatusCreated)
	meteredKey := decodeContract[struct {
		Item   contractKey `json:"item"`
		Secret string      `json:"secret"`
	}](t, body)
	resp, body = fixture.request(t, http.MethodPost, "/v1/chat/completions", map[string]any{
		"model": "public-chat", "messages": []map[string]string{{"role": "user", "content": "must be rejected"}},
	}, map[string]string{"Authorization": "Bearer " + meteredKey.Secret})
	requireStatus(t, resp, body, http.StatusBadRequest)
	if !bytes.Contains(body, []byte(`"code":"model_not_metered"`)) {
		t.Fatalf("finite key must reject unpriced model, body=%s", body)
	}
	resp, body = fixture.request(t, http.MethodPatch, fmt.Sprintf("/api/v1/keys/%d", meteredKey.Item.ID), map[string]any{"clearQuota": true}, nil)
	requireStatus(t, resp, body, http.StatusOK)
	unlimitedKey := decodeContract[struct {
		Item contractKey `json:"item"`
	}](t, body).Item
	if unlimitedKey.QuotaUSD != nil {
		t.Fatalf("clearQuota did not restore unlimited quota: %+v", unlimitedKey)
	}

	resp, body = fixture.request(t, http.MethodGet, "/api/v1/monitor/matrix", nil, nil)
	requireStatus(t, resp, body, http.StatusOK)
	monitor := decodeContract[contractMonitor](t, body)
	if monitor.ProbeIntervalSeconds <= 0 || len(monitor.Routes) == 0 {
		t.Fatalf("monitor matrix contract mismatch: %+v", monitor)
	}
	requireRFC3339(t, "monitor.generatedAt", monitor.GeneratedAt)
	monitorModels := make([]string, 0, len(monitor.Routes))
	for _, monitoredRoute := range monitor.Routes {
		monitorModels = append(monitorModels, monitoredRoute.Model)
	}
	sort.Strings(monitorModels)
	if !containsString(monitorModels, "public-chat") {
		t.Fatalf("selected monitored model is absent: %v", monitorModels)
	}

	resp, body = fixture.request(t, http.MethodGet, "/api/v1/dashboard", nil, nil)
	requireStatus(t, resp, body, http.StatusOK)
	dashboard := decodeContract[contractDashboard](t, body)
	if dashboard.MonitoredModels < 1 || dashboard.Requests24H != 1 || dashboard.SuccessRate24H != 100 {
		t.Fatalf("dashboard is not derived from the completed workflow: %+v", dashboard)
	}

	resp, body = fixture.request(t, http.MethodGet, "/api/v1/settings", nil, nil)
	requireStatus(t, resp, body, http.StatusOK)
	settings := decodeContract[contractSettings](t, body)
	if settings.ProbeIntervalSeconds <= 0 || settings.FailureThreshold < 2 || settings.CooldownSeconds <= 0 || settings.RequestTimeoutSeconds <= 0 || settings.MaxAttempts <= 0 || settings.LogRetentionDays <= 0 {
		t.Fatalf("gateway settings fields missing: %+v", settings)
	}
	if settings.PriceCatalogVersion == "" || settings.PriceCatalogSource == "" {
		t.Fatalf("price catalog metadata missing: %+v", settings)
	}
	requireRFC3339(t, "priceCatalogUpdatedAt", settings.PriceCatalogUpdatedAt)

	resp, body = fixture.request(t, http.MethodPatch, "/api/v1/settings", map[string]any{
		"probeIntervalSeconds": 180, "failureThreshold": 3, "cooldownSeconds": 420,
		"requestTimeoutSeconds": 45, "maxAttempts": 4, "logRetentionDays": 14,
	}, nil)
	requireStatus(t, resp, body, http.StatusOK)
	settings = decodeContract[contractSettings](t, body)
	if settings.ProbeIntervalSeconds != 180 || settings.FailureThreshold != 3 || settings.CooldownSeconds != 420 || settings.RequestTimeoutSeconds != 45 || settings.MaxAttempts != 4 || settings.LogRetentionDays != 14 {
		t.Fatalf("settings patch did not round-trip: %+v", settings)
	}

	resp, body = fixture.request(t, http.MethodPost, "/api/v1/auth/logout", nil, nil)
	requireStatus(t, resp, body, http.StatusNoContent)
	resp, body = fixture.request(t, http.MethodGet, "/api/v1/me", nil, nil)
	requireStatus(t, resp, body, http.StatusUnauthorized)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
