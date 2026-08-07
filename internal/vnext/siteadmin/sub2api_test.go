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

func TestSub2APIRefreshesSessionAndReadsBalanceAndUsage(t *testing.T) {
	fixedNow := time.Date(2026, time.August, 7, 10, 0, 0, 0, time.UTC)
	var refreshCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case sub2APIRefreshPath:
			refreshCalls.Add(1)
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["refresh_token"] != "refresh-old" {
				t.Errorf("refresh payload = %#v, err=%v", body, err)
			}
			_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"access_token":"access-new","refresh_token":"refresh-new","expires_in":3600,"token_type":"Bearer"}}`))
		case sub2APISelfPath:
			if r.Header.Get("Authorization") != "Bearer access-new" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"code":401,"message":"expired access-old refresh-old"}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"id":17,"email":"owner@example.com","username":"owner","balance":12.3456,"frozen_balance":1.25}}`))
		case sub2APIUsagePath:
			if r.Header.Get("Authorization") != "Bearer access-new" {
				t.Errorf("usage authorization = %q", r.Header.Get("Authorization"))
			}
			if r.URL.Query().Get("page") != "1" || r.URL.Query().Get("page_size") != "50" ||
				r.URL.Query().Get("model") != "model-a" || r.URL.Query().Get("api_key_id") != "44" {
				t.Errorf("usage query = %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"items":[{"id":101,"request_id":"req-1","model":"model-a","input_tokens":12,"output_tokens":3,"cache_creation_tokens":2,"cache_read_tokens":4,"actual_cost":0.00125,"duration_ms":640,"created_at":"2026-08-07T09:59:00Z","api_key":{"id":44,"name":"key-a"}}],"total":51,"page":1,"page_size":50,"pages":2}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	adapter, err := NewSub2APIAdapter(server.Client())
	if err != nil {
		t.Fatal(err)
	}
	adapter.now = func() time.Time { return fixedNow }
	if err := ValidateAdapter(adapter); err != nil {
		t.Fatalf("adapter capability contract is invalid: %v", err)
	}
	connection := Connection{Origin: server.URL, Secrets: Secrets{AccessToken: "access-old", RefreshToken: "refresh-old"}}
	snapshot, update, err := adapter.ReadBalance(t.Context(), connection)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Available != (Amount{Value: "12.3456", Unit: "USD"}) || snapshot.AccountName != "owner" {
		t.Fatalf("balance = %#v", snapshot)
	}
	if update == nil || update.Secrets.AccessToken != "access-new" || update.Secrets.RefreshToken != "refresh-new" {
		t.Fatalf("session update = %#v", update)
	}

	page, usageUpdate, err := adapter.ReadUsage(t.Context(), connection, UsageQuery{Limit: 50, Model: "model-a", APIKey: "44"})
	if err != nil {
		t.Fatal(err)
	}
	if usageUpdate == nil || usageUpdate.Secrets.AccessToken != "access-new" || refreshCalls.Load() != 1 {
		t.Fatalf("usage update = %#v, refresh calls=%d", usageUpdate, refreshCalls.Load())
	}
	if len(page.Records) != 1 || !page.HasMore || page.NextCursor != "2" {
		t.Fatalf("usage page = %#v", page)
	}
	record := page.Records[0]
	if record.RemoteID != "101" || record.RequestID != "req-1" || record.APIKeyName != "key-a" ||
		record.Tokens.Total == nil || *record.Tokens.Total != 15 || record.Tokens.CacheWrite == nil || *record.Tokens.CacheWrite != 2 ||
		record.Charge == nil || *record.Charge != (Amount{Value: "0.00125", Unit: "USD"}) {
		t.Fatalf("usage record = %#v", record)
	}
}

func TestSub2APIErrorsDoNotEchoRemoteSecrets(t *testing.T) {
	const leaked = "credential-that-must-not-leak"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"` + leaked + `","message":"Bearer ` + leaked + `"}`))
	}))
	defer server.Close()

	adapter, err := NewSub2APIAdapter(server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = adapter.ReadBalance(t.Context(), Connection{
		Origin: server.URL, Secrets: Secrets{AccessToken: leaked},
	})
	if err == nil || strings.Contains(err.Error(), leaked) {
		t.Fatalf("unredacted error = %v", err)
	}
}

func TestSub2APIIsASeparateRegisteredAdapterKind(t *testing.T) {
	registry := NewRegistry()
	adapter, err := NewSub2APIAdapter(roundTripDoer(func(*http.Request) (*http.Response, error) { return nil, nil }))
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(adapter); err != nil {
		t.Fatal(err)
	}
	resolved, err := registry.Lookup("sub2api")
	if err != nil || resolved != adapter || resolved.Kind() == "ciii" {
		t.Fatalf("resolved adapter=%v err=%v", resolved, err)
	}
}
