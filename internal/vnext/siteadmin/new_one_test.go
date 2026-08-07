package siteadmin

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewAPIReadsExactQuotaAndNormalizedRawUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer management-token" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch request.URL.Path {
		case newOneSelfPath:
			writeTestJSON(writer, `{"success":true,"data":{"id":7,"username":"owner","quota":"12345.50","used_quota":"55.25"}}`)
		case newOneUsagePath:
			if request.URL.Query().Get("p") != "1" || request.URL.Query().Get("page_size") != "2" ||
				request.URL.Query().Get("model_name") != "model-a" {
				t.Fatalf("usage query = %s", request.URL.RawQuery)
			}
			writeTestJSON(writer, `{"success":true,"data":{"page":1,"page_size":2,"total":3,"items":[{"id":99,"request_id":"req-upstream","model_name":"model-a","prompt_tokens":12,"completion_tokens":3,"quota":"42.5","use_time":2,"created_at":1786000000,"token_name":"key-a","other":"{\"upstream_model_name\":\"source-a\",\"cache_tokens\":4,\"reasoning_tokens\":2}"}]}}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	adapter, err := NewNewAPIAdapter(server.Client())
	if err != nil {
		t.Fatal(err)
	}
	connection := Connection{Origin: server.URL, Secrets: Secrets{AccessToken: "management-token"}}
	balance, update, err := adapter.ReadBalance(t.Context(), connection)
	if err != nil {
		t.Fatal(err)
	}
	if update != nil || balance.Available != (Amount{Value: "12345.50", Unit: "quota"}) ||
		balance.Used == nil || balance.Used.Value != "55.25" || balance.AccountName != "owner" {
		t.Fatalf("balance = %+v, update = %+v", balance, update)
	}
	page, update, err := adapter.ReadUsage(t.Context(), connection, UsageQuery{Limit: 2, Model: "model-a"})
	if err != nil {
		t.Fatal(err)
	}
	if update != nil || !page.HasMore || page.NextCursor != "2" || len(page.Records) != 1 {
		t.Fatalf("usage page = %+v, update = %+v", page, update)
	}
	record := page.Records[0]
	if record.RequestID != "req-upstream" || record.UpstreamModel != "source-a" || record.DurationMS == nil || *record.DurationMS != 2000 ||
		record.Tokens.CacheRead == nil || *record.Tokens.CacheRead != 4 || record.Tokens.Reasoning == nil || *record.Tokens.Reasoning != 2 ||
		record.Charge == nil || *record.Charge != (Amount{Value: "42.5", Unit: "quota"}) {
		t.Fatalf("usage record = %+v", record)
	}
}

func TestOneAPIUsesDirectAuthorizationAndZeroBasedCursor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "direct-token" || request.URL.Query().Get("p") != "0" {
			t.Fatalf("authorization = %q, query = %s", request.Header.Get("Authorization"), request.URL.RawQuery)
		}
		writeTestJSON(writer, `{"success":true,"data":[{"id":1,"model_name":"model-b","prompt_tokens":1,"completion_tokens":2,"elapsed_time":17,"created_at":"2026-08-06T12:00:00Z"}]}`)
	}))
	defer server.Close()

	adapter, err := NewOneAPIAdapter(server.Client())
	if err != nil {
		t.Fatal(err)
	}
	page, _, err := adapter.ReadUsage(t.Context(), Connection{
		Origin: server.URL, Secrets: Secrets{Authorization: "direct-token"},
	}, UsageQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 || page.Records[0].DurationMS == nil || *page.Records[0].DurationMS != 17 ||
		page.Records[0].Tokens.Total == nil || *page.Records[0].Tokens.Total != 3 {
		t.Fatalf("usage page = %+v", page)
	}
}

func TestNewOneUsageRejectsRowsWithoutARealTimestamp(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(writer, `{"success":true,"data":{"items":[{"id":1,"created_at":"not-a-time"}]}}`)
	}))
	defer server.Close()
	adapter, err := NewNewAPIAdapter(server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = adapter.ReadUsage(t.Context(), Connection{
		Origin: server.URL, Secrets: Secrets{AccessToken: "token"},
	}, UsageQuery{Limit: 10, From: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)})
	if err == nil {
		t.Fatal("usage row without a valid timestamp should be rejected")
	}
}

func writeTestJSON(writer http.ResponseWriter, body string) {
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write([]byte(body))
}
