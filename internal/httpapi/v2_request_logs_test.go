package httpapi

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/LuTianTian001/JieShan/internal/store"
)

func TestRequestLogV2EndpointsPageFilterAndSummarize(t *testing.T) {
	fixture := newAPIContractFixture(t)
	response, body := fixture.request(t, http.MethodGet, "/api/v2/request-logs", nil, nil)
	requireStatus(t, response, body, http.StatusUnauthorized)

	response, body = fixture.request(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"password": "correct horse battery staple",
	}, nil)
	requireStatus(t, response, body, http.StatusOK)

	insertV2HTTPLog(t, fixture, "api-z", "gpt-5", "provider-a", "success", true, 80, 100, 3_000, 1)
	insertV2HTTPLog(t, fixture, "api-a", "claude", "provider-b", "failed", false, 0, 200, 2_000, 2)

	response, body = fixture.request(t, http.MethodGet, "/api/v2/request-logs?limit=1", nil, nil)
	requireStatus(t, response, body, http.StatusOK)
	if got := response.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	page := decodeContract[store.RequestLogPage](t, body)
	if len(page.Items) != 1 || page.Items[0].ID != "api-z" || !page.HasMore || page.NextCursor == nil {
		t.Fatalf("first page = %+v", page)
	}

	response, body = fixture.request(t, http.MethodGet, "/api/v2/request-logs/api-z", nil, nil)
	requireStatus(t, response, body, http.StatusOK)
	if got := response.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("detail Cache-Control = %q", got)
	}
	detail := decodeContract[store.RequestLogDetail](t, body)
	if detail.ID != "api-z" || detail.Surface != "chat_completions" || detail.RoutingProfileName != store.DefaultRoutingProfileName || len(detail.Attempts) != 1 {
		t.Fatalf("detail = %+v", detail)
	}
	if detail.StartedAt != 3_000 || detail.Attempts[0].CreatedAt != 3_000 {
		t.Fatalf("detail timestamps = %+v", detail)
	}

	path := fmt.Sprintf("/api/v2/request-logs?limit=1&beforeTime=%d&beforeId=%s", page.NextCursor.BeforeTime, page.NextCursor.BeforeID)
	response, body = fixture.request(t, http.MethodGet, path, nil, nil)
	requireStatus(t, response, body, http.StatusOK)
	page = decodeContract[store.RequestLogPage](t, body)
	if len(page.Items) != 1 || page.Items[0].ID != "api-a" || page.HasMore || page.NextCursor != nil {
		t.Fatalf("second page = %+v", page)
	}

	response, body = fixture.request(t, http.MethodGet, "/api/v2/request-logs?status=SUCCESS&model=GPT-5&stream=true&switched=false", nil, nil)
	requireStatus(t, response, body, http.StatusOK)
	filtered := decodeContract[store.RequestLogPage](t, body)
	if len(filtered.Items) != 1 || filtered.Items[0].ID != "api-z" {
		t.Fatalf("filtered page = %+v", filtered)
	}

	response, body = fixture.request(t, http.MethodGet, "/api/v2/request-logs/summary", nil, nil)
	requireStatus(t, response, body, http.StatusOK)
	summary := decodeContract[store.RequestLogSummary](t, body)
	if summary.Count != 2 || summary.SuccessRate != 50 || summary.CostMicroUSD != 300 || summary.SwitchRate != 50 {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.P50TTFTMS == nil || *summary.P50TTFTMS != 80 || summary.P95TTFTMS == nil || *summary.P95TTFTMS != 80 {
		t.Fatalf("summary TTFT = %+v", summary)
	}

	response, body = fixture.request(t, http.MethodGet, "/api/v2/request-logs/summary?status=success&model=gpt-5&stream=true&switched=false", nil, nil)
	requireStatus(t, response, body, http.StatusOK)
	filteredSummary := decodeContract[store.RequestLogSummary](t, body)
	if filteredSummary.Count != int64(len(filtered.Items)) || filteredSummary.Count != 1 || filteredSummary.SuccessRate != 100 || filteredSummary.CostMicroUSD != 100 {
		t.Fatalf("filtered summary = %+v", filteredSummary)
	}
}

func TestRequestLogV2EndpointsRejectInvalidQuery(t *testing.T) {
	fixture := newAPIContractFixture(t)
	response, body := fixture.request(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"password": "correct horse battery staple",
	}, nil)
	requireStatus(t, response, body, http.StatusOK)

	for _, path := range []string{
		"/api/v2/request-logs?limit=0",
		"/api/v2/request-logs?stream=maybe",
		"/api/v2/request-logs?beforeTime=1000",
		"/api/v2/request-logs?site=1&siteId=2",
		"/api/v2/request-logs/summary?downstreamKey=-1",
	} {
		response, body = fixture.request(t, http.MethodGet, path, nil, nil)
		requireStatus(t, response, body, http.StatusBadRequest)
	}
}

func insertV2HTTPLog(t *testing.T, fixture *apiContractFixture, id, requestedModel, actualModel, status string, stream bool, ttft, cost, startedAt int64, attempts int) {
	t.Helper()
	var firstToken any
	if ttft > 0 {
		firstToken = ttft
	}
	_, err := fixture.store.DB.Exec(`INSERT INTO request_logs(
id,requested_model,actual_model,status,http_status,is_stream,first_token_ms,duration_ms,cost_micro_usd,started_at,finished_at)
VALUES (?,?,?,?,200,?,?,?,?,?,?)`, id, requestedModel, actualModel, status, boolToInt(stream), firstToken, int64(500), cost, startedAt, startedAt+500)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < attempts; index++ {
		_, err := fixture.store.DB.Exec(`INSERT INTO request_attempts(request_id,attempt_index,status,created_at)
VALUES (?,?,?,?)`, id, index, "success", startedAt+int64(index))
		if err != nil {
			t.Fatal(err)
		}
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
