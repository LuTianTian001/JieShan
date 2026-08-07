package siteadminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/secretbox"
	"github.com/LuTianTian001/JieShan/internal/vnext/siteadmin"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

func TestStoreHandlerRefreshesBalanceAndPersistsSearchableSourceUsageWithoutLeakingSecrets(t *testing.T) {
	ctx := context.Background()
	storage, err := vnextstore.Open(ctx, filepath.Join(t.TempDir(), "vnext.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	siteID, err := storage.CreateSite(ctx, vnextstore.SiteWrite{Name: "Relay account", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	box, err := secretbox.New(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	adapter := &accountAdapterFake{now: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)}
	registry := siteadmin.NewRegistry()
	if err := registry.Register(adapter); err != nil {
		t.Fatal(err)
	}
	handler, err := NewStoreHandler(storage, box, registry)
	if err != nil {
		t.Fatal(err)
	}

	secret := "account-access-token-that-must-not-leak"
	created := performRequest(handler, http.MethodPut, "/api/vnext/site-accounts/sites/"+intString(siteID), `{
"adapterKind":"test-account","origin":"https://relay.example","secrets":{"accessToken":"`+secret+`"}}
`, "")
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	if strings.Contains(created.Body.String(), secret) || strings.Contains(created.Body.String(), "accessToken") {
		t.Fatalf("create response leaked account secret: %s", created.Body.String())
	}
	var createdBody struct {
		Revision         int64 `json:"revision"`
		SecretConfigured bool  `json:"secretConfigured"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdBody); err != nil {
		t.Fatal(err)
	}
	if createdBody.Revision != 1 || !createdBody.SecretConfigured {
		t.Fatalf("created body = %+v", createdBody)
	}
	var ciphertext []byte
	if err := storage.DB.QueryRowContext(ctx, `SELECT secrets_cipher FROM site_account_connections WHERE site_id=?`, siteID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte(secret)) {
		t.Fatal("database stored site account token in plaintext")
	}

	balance := performRequest(handler, http.MethodPost,
		"/api/vnext/site-accounts/sites/"+intString(siteID)+"/balance/refresh", "", "")
	if balance.Code != http.StatusOK || !strings.Contains(balance.Body.String(), `"availableValue":"19.7500"`) {
		t.Fatalf("balance status = %d, body = %s", balance.Code, balance.Body.String())
	}
	if adapter.receivedAccessToken != secret {
		t.Fatalf("adapter received access token = %q", adapter.receivedAccessToken)
	}

	sync := performRequest(handler, http.MethodPost,
		"/api/vnext/site-accounts/sites/"+intString(siteID)+"/usage/sync", `{"limit":100}`, "")
	if sync.Code != http.StatusOK || !strings.Contains(sync.Body.String(), `"inserted":1`) {
		t.Fatalf("sync status = %d, body = %s", sync.Code, sync.Body.String())
	}
	syncAgain := performRequest(handler, http.MethodPost,
		"/api/vnext/site-accounts/sites/"+intString(siteID)+"/usage/sync", `{"limit":100}`, "")
	if syncAgain.Code != http.StatusOK || !strings.Contains(syncAgain.Body.String(), `"deduplicated":1`) {
		t.Fatalf("second sync status = %d, body = %s", syncAgain.Code, syncAgain.Body.String())
	}

	usage := performRequest(handler, http.MethodGet,
		"/api/vnext/site-accounts/sites/"+intString(siteID)+"/usage?search=req-source-1", "", "")
	if usage.Code != http.StatusOK || !strings.Contains(usage.Body.String(), `"requestId":"req-source-1"`) ||
		!strings.Contains(usage.Body.String(), `"cacheReadTokens":12`) {
		t.Fatalf("usage status = %d, body = %s", usage.Code, usage.Body.String())
	}
	if strings.Contains(usage.Body.String(), secret) {
		t.Fatal("usage response leaked account secret")
	}
}

type accountAdapterFake struct {
	now                 time.Time
	receivedAccessToken string
}

func (*accountAdapterFake) Kind() string { return "test-account" }

func (*accountAdapterFake) Capabilities() siteadmin.Capabilities {
	return siteadmin.Capabilities{SessionRefresh: true, Balance: true, Usage: true}
}

func (adapter *accountAdapterFake) RefreshSession(context.Context, siteadmin.Connection) (siteadmin.SessionUpdate, error) {
	return siteadmin.SessionUpdate{Changed: false}, nil
}

func (adapter *accountAdapterFake) ReadBalance(_ context.Context, connection siteadmin.Connection) (siteadmin.BalanceSnapshot, *siteadmin.SessionUpdate, error) {
	adapter.receivedAccessToken = connection.Secrets.AccessToken
	used := siteadmin.Amount{Value: "4.25", Unit: "USD"}
	return siteadmin.BalanceSnapshot{
		AccountID: "owner-1", AccountName: "Owner", Available: siteadmin.Amount{Value: "19.7500", Unit: "USD"},
		Used: &used, CapturedAt: adapter.now,
	}, nil, nil
}

func (adapter *accountAdapterFake) ReadUsage(_ context.Context, connection siteadmin.Connection, _ siteadmin.UsageQuery) (siteadmin.UsagePage, *siteadmin.SessionUpdate, error) {
	adapter.receivedAccessToken = connection.Secrets.AccessToken
	input, output, cacheRead, total := int64(80), int64(20), int64(12), int64(100)
	duration := int64(823)
	status := http.StatusOK
	charge := siteadmin.Amount{Value: "0.002500", Unit: "USD"}
	return siteadmin.UsagePage{
		FetchedAt: adapter.now,
		Records: []siteadmin.UsageRecord{{
			RemoteID: "source-1", RequestID: "req-source-1", OccurredAt: adapter.now.Add(-time.Minute),
			Model: "claude-sonnet", Status: "succeeded", HTTPStatus: &status,
			Tokens: siteadmin.TokenUsage{Input: &input, Output: &output, CacheRead: &cacheRead, Total: &total},
			Charge: &charge, DurationMS: &duration, APIKeyName: "primary",
		}},
	}, nil, nil
}

func performRequest(handler http.Handler, method, path, body, ifMatch string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func intString(value int64) string {
	return strconv.FormatInt(value, 10)
}

var _ siteadmin.SessionRefresher = (*accountAdapterFake)(nil)
var _ siteadmin.BalanceReader = (*accountAdapterFake)(nil)
var _ siteadmin.UsageReader = (*accountAdapterFake)(nil)
