package accountadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCiiiRefreshRotatesOnceAcrossConcurrentRequests(t *testing.T) {
	const (
		oldAccess  = "test-old-access"
		oldRefresh = "test-old-refresh"
		newAccess  = "test-new-access"
		newRefresh = "test-new-refresh"
	)
	var refreshCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case ciiiProfilePath:
			switch request.Header.Get("Authorization") {
			case "Bearer " + oldAccess:
				writer.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(writer, `{"code":"UNAUTHORIZED","message":"expired"}`)
			case "Bearer " + newAccess:
				fmt.Fprint(writer, `{"code":0,"data":{"id":319,"username":"demo","status":"active","balance":12.5,"frozen_balance":"0.25"}}`)
			default:
				writer.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(writer, `{"code":"UNAUTHORIZED","message":"bad test credential"}`)
			}
		case ciiiRefreshPath:
			refreshCalls.Add(1)
			var payload map[string]string
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Errorf("decode refresh body: %v", err)
			}
			if payload["refresh_token"] != oldRefresh {
				t.Errorf("unexpected refresh credential")
			}
			time.Sleep(20 * time.Millisecond)
			fmt.Fprintf(writer, `{"code":0,"data":{"access_token":%q,"refresh_token":%q,"expires_in":3600,"token_type":"Bearer"}}`, newAccess, newRefresh)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	adapter, err := New(KindCiii, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	connection := Connection{
		Origin: server.URL,
		Credentials: Credentials{
			AccessToken:  oldAccess,
			RefreshToken: oldRefresh,
		},
	}

	type result struct {
		snapshot Snapshot
		rotated  *Credentials
		err      error
	}
	results := make(chan result, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			snapshot, rotated, snapshotErr := adapter.Snapshot(context.Background(), connection)
			results <- result{snapshot: snapshot, rotated: rotated, err: snapshotErr}
		}()
	}
	wait.Wait()
	close(results)
	for value := range results {
		if value.err != nil {
			t.Fatalf("snapshot failed: %v", value.err)
		}
		if value.snapshot.Balance != "12.5" || value.snapshot.Frozen != "0.25" {
			t.Fatalf("unexpected snapshot: %+v", value.snapshot)
		}
		if value.rotated == nil || value.rotated.AccessToken != newAccess || value.rotated.RefreshToken != newRefresh {
			t.Fatalf("rotated credentials were not returned")
		}
	}
	if calls := refreshCalls.Load(); calls != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls)
	}
}

func TestOneAPISubscriptionsAreUnsupportedWithoutHTTPRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		http.Error(writer, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()

	adapter, err := New(KindOneAPI, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = adapter.Subscriptions(context.Background(), Connection{Origin: server.URL})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("unsupported operation made an HTTP request")
	}
}

func TestNewAPIUsageParsing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case newOneStatusPath:
			fmt.Fprint(writer, `{"success":true,"message":"","data":{"quota_per_unit":500000}}`)
		case newOneLogPath:
			if got := request.Header.Get("Authorization"); got != "Bearer test-management-token" {
				t.Errorf("Authorization = %q", got)
			}
			if request.URL.Query().Get("p") != "2" || request.URL.Query().Get("page_size") != "25" {
				t.Errorf("unexpected pagination query: %s", request.URL.RawQuery)
			}
			fmt.Fprint(writer, `{
				"success":true,
				"message":"",
				"data":{
					"page":2,
					"page_size":25,
					"total":26,
					"items":[{
						"id":7,
						"request_id":"req-test",
						"model_name":"claude-test",
						"quota":125000,
						"prompt_tokens":100,
						"completion_tokens":20,
						"use_time":3,
						"is_stream":true,
						"created_at":1700000000,
						"token_name":"downstream-a",
						"group":"default",
						"other":"{\"reasoning_effort\":\"high\",\"frt\":321,\"model_ratio\":2,\"group_ratio\":1.5,\"cache_tokens\":11,\"cache_creation_tokens\":4}"
					}]
				}
			}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	adapter, err := New(KindNewAPI, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	page, rotated, err := adapter.Usage(context.Background(), Connection{
		Origin:      server.URL,
		Credentials: Credentials{AccessToken: "test-management-token"},
	}, UsageQuery{Page: 2, PageSize: 25, Model: "claude-test"})
	if err != nil {
		t.Fatal(err)
	}
	if rotated != nil {
		t.Fatalf("New API unexpectedly rotated credentials")
	}
	if page.Total != 26 || page.Pages != 2 || page.HasMore || len(page.Items) != 1 {
		t.Fatalf("unexpected page: %+v", page)
	}
	item := page.Items[0]
	if page.Unit != "quota" || page.QuotaPerUnit != "500000" || item.Quota != "125000" || item.ActualCost != "" {
		t.Fatalf("usage quota was not preserved: page=%+v item=%+v", page, item)
	}
	if item.ReasoningEffort != "high" || item.FirstTokenMS != 321 {
		t.Fatalf("unexpected usage item: %+v", item)
	}
	if item.CacheReadTokens != 11 || item.CacheCreationTokens != 4 || item.DurationMS != 3000 {
		t.Fatalf("usage details were not normalized: %+v", item)
	}
}

func TestCiiiLegacyProfileAndSubscriptionFallbacks(t *testing.T) {
	var authMeCalls atomic.Int32
	var activeSubscriptionCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case ciiiAuthMePath:
			authMeCalls.Add(1)
			writer.WriteHeader(http.StatusNotFound)
			fmt.Fprint(writer, `{"code":"NOT_FOUND","message":"not supported"}`)
		case ciiiProfilePath:
			fmt.Fprint(writer, `{"code":0,"data":{"id":1,"username":"legacy","balance":"9"}}`)
		case ciiiActiveSubscriptionsPath:
			activeSubscriptionCalls.Add(1)
			writer.WriteHeader(http.StatusNotFound)
			fmt.Fprint(writer, `{"code":"NOT_FOUND","message":"not supported"}`)
		case ciiiSubscriptionsPath:
			fmt.Fprint(writer, `{"code":0,"data":[{"id":3,"status":"active","group":{"id":4,"name":"Claude","daily_limit_usd":"5"},"daily_usage_usd":"1.25"}]}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	adapter, err := New(KindCiii, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	connection := Connection{Origin: server.URL, Credentials: Credentials{AccessToken: "test-access"}}
	snapshot, _, err := adapter.Snapshot(context.Background(), connection)
	if err != nil || snapshot.Username != "legacy" {
		t.Fatalf("legacy profile fallback failed: snapshot=%+v err=%v", snapshot, err)
	}
	subscriptions, _, err := adapter.Subscriptions(context.Background(), connection)
	if err != nil || len(subscriptions) != 1 || subscriptions[0].Daily.Used != "1.25" {
		t.Fatalf("legacy subscription fallback failed: subscriptions=%+v err=%v", subscriptions, err)
	}
	if authMeCalls.Load() != 1 || activeSubscriptionCalls.Load() != 1 {
		t.Fatalf("preferred endpoints were not attempted first")
	}
}

func TestHTTP200BusinessFailureIsErrorAndCredentialIsRedacted(t *testing.T) {
	const secret = "test-secret-that-must-not-leak"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Path == newOneStatusPath {
			fmt.Fprint(writer, `{"success":true,"message":"","data":{"quota_per_unit":500000}}`)
			return
		}
		fmt.Fprintf(writer, `{"success":false,"message":"credential %s rejected"}`, secret)
	}))
	defer server.Close()

	adapter, err := New(KindNewAPI, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = adapter.Snapshot(context.Background(), Connection{
		Origin:      server.URL,
		Credentials: Credentials{AccessToken: secret},
	})
	if err == nil {
		t.Fatal("HTTP 200 business failure returned nil error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked a credential: %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("error did not preserve a useful redaction marker: %v", err)
	}
}

func TestRequestTransportErrorPreservesDeadlineAndRedactsCredential(t *testing.T) {
	const secret = "test-transport-secret"
	transportErr := fmt.Errorf("dial with credential %s: %w", secret, context.DeadlineExceeded)
	adapter := &adapter{doer: doerFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportErr
	})}

	_, _, err := adapter.request(
		context.Background(),
		Connection{Origin: "https://api.example.com"},
		http.MethodGet,
		"/v1/account",
		nil,
		nil,
		nil,
		Credentials{AccessToken: secret},
	)
	if err == nil {
		t.Fatal("request() error = nil, want transport error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("request() error = %v, want context deadline in error chain", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("request() error leaked credential: %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("request() error did not preserve a redaction marker: %v", err)
	}
}

type doerFunc func(*http.Request) (*http.Response, error)

func (fn doerFunc) Do(request *http.Request) (*http.Response, error) {
	return fn(request)
}
