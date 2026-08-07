package siteadmin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCiiiRefreshesBalanceAndReadsUsageMetadata(t *testing.T) {
	fixedNow := time.Date(2026, time.August, 6, 8, 0, 0, 0, time.UTC)
	var refreshCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case ciiiRefreshPath:
			refreshCalls.Add(1)
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["refresh_token"] != "refresh-old" {
				t.Errorf("unexpected refresh payload %#v err=%v", body, err)
			}
			_, _ = w.Write([]byte(`{"access_token":"access-new","refresh_token":"refresh-new","expires_in":3600}`))
		case ciiiAuthMePath:
			if r.Header.Get("Authorization") != "Bearer access-new" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"code":401,"message":"expired access-old refresh-old"}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"id":319,"email":"operator@example.com","balance":"12.345600","used":"1.25","currency":"USD"}}`))
		case ciiiUsagePath:
			if got := r.URL.Query().Get("page"); got != "1" {
				t.Errorf("unexpected page %q", got)
			}
			if got := r.URL.Query().Get("page_size"); got != "50" {
				t.Errorf("unexpected page size %q", got)
			}
			if got := r.URL.Query().Get("model"); got != "model-a" {
				t.Errorf("unexpected model filter %q", got)
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":"usage-1","request_id":"req-1","model":"model-a","input_tokens":12,"output_tokens":3,"cache_read_tokens":4,"reasoning_tokens":2,"actual_cost":"0.00125","currency":"USD","status_code":200,"duration_ms":640,"created_at":"2026-08-06T07:59:00Z","content":"must never be imported"}],"total":51,"page":1,"page_size":50,"pages":2}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	adapter, err := NewCiiiAdapter(server.Client())
	if err != nil {
		t.Fatal(err)
	}
	adapter.now = func() time.Time { return fixedNow }
	if err := ValidateAdapter(adapter); err != nil {
		t.Fatalf("adapter capability contract is invalid: %v", err)
	}
	connection := Connection{
		Origin: server.URL,
		Secrets: Secrets{
			AccessToken:  "access-old",
			RefreshToken: "refresh-old",
		},
	}
	snapshot, update, err := adapter.ReadBalance(t.Context(), connection)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Available != (Amount{Value: "12.345600", Unit: "USD"}) || snapshot.Used == nil || snapshot.Used.Value != "1.25" {
		t.Fatalf("unexpected balance snapshot %#v", snapshot)
	}
	if update == nil || update.Secrets.AccessToken != "access-new" || update.Secrets.RefreshToken != "refresh-new" {
		t.Fatalf("expected rotated session, got %#v", update)
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("expected one refresh, got %d", refreshCalls.Load())
	}

	page, usageUpdate, err := adapter.ReadUsage(t.Context(), connection, UsageQuery{Limit: 50, Model: "model-a"})
	if err != nil {
		t.Fatal(err)
	}
	if usageUpdate == nil || usageUpdate.Secrets.AccessToken != "access-new" {
		t.Fatalf("expected cached rotated session to be returned, got %#v", usageUpdate)
	}
	if len(page.Records) != 1 || page.NextCursor != "2" || !page.HasMore {
		t.Fatalf("unexpected usage page %#v", page)
	}
	record := page.Records[0]
	if record.RemoteID != "usage-1" || record.Model != "model-a" || record.Charge == nil || record.Charge.Value != "0.00125" {
		t.Fatalf("unexpected usage record %#v", record)
	}
	if record.Tokens.Input == nil || *record.Tokens.Input != 12 || record.Tokens.Reasoning == nil || *record.Tokens.Reasoning != 2 {
		t.Fatalf("unexpected token metadata %#v", record.Tokens)
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("usage should reuse the rotated session, refresh calls=%d", refreshCalls.Load())
	}
}

func TestCiiiErrorsDoNotEchoRemoteSecrets(t *testing.T) {
	const leaked = "access-token-that-must-not-leak"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"` + leaked + `","message":"Bearer ` + leaked + `"}`))
	}))
	defer server.Close()
	adapter, err := NewCiiiAdapter(server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = adapter.ReadBalance(t.Context(), Connection{Origin: server.URL, Secrets: Secrets{AccessToken: leaked}})
	if err == nil {
		t.Fatal("expected remote rejection")
	}
	if strings.Contains(err.Error(), leaked) || strings.Contains(err.Error(), "Bearer") {
		t.Fatalf("error leaked remote secret: %v", err)
	}
}

func TestCiiiRejectsUsageRowsWithoutTimestamps(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":"bad-row"}],"page":1,"page_size":10}}`))
	}))
	defer server.Close()
	adapter, err := NewCiiiAdapter(server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = adapter.ReadUsage(t.Context(), Connection{Origin: server.URL, Secrets: Secrets{AccessToken: "access"}}, UsageQuery{Limit: 10})
	if err == nil || !strings.Contains(err.Error(), "timestamp") {
		t.Fatalf("expected malformed timestamp error, got %v", err)
	}
}

func TestCiiiOriginKeepsConfiguredBasePath(t *testing.T) {
	value, err := siteAdminURL("https://example.com/panel", ciiiUsagePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if value != "https://example.com/panel/api/v1/usage" {
		t.Fatalf("unexpected URL %q", value)
	}
}
