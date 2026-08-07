package logsapi

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

func TestHandlerListsSummarizesAndExpandsRequestTimeline(t *testing.T) {
	firstAttemptIndex := 0
	lastAttemptIndex := 1
	coolingUntil := int64(9_000)
	repository := &logsRepositoryFake{
		page: vnextstore.RequestLogPage{Items: []vnextstore.RequestLog{{
			ID: "req-1", DownstreamKeyID: 7, DownstreamKeyName: "Personal",
			PublishedModelID: 17, PublishedModelRevision: 4,
			EffectiveRoutingProfileID: 9, EffectiveRoutingProfileName: "Low latency",
			SourceRoutingProfileID: 1, SourceRoutingProfileName: "Default", RouteRevision: 8,
			PublicModel: "claude-sonnet",
			APISurface:  "anthropic.messages", ReasoningEffort: "high", Status: "success", StartedAt: 100,
			BillingMultiplierBPS: 12_500,
			MeteringStatus:       "unavailable", MeteringErrorCode: "usage_unavailable",
			FinalAttempt: &vnextstore.RequestAttempt{
				AttemptIndex: 1, ProviderModelTargetID: 41, SiteID: 5, SiteName: "Relay B",
				EndpointID: 6, EndpointName: "Messages", CredentialID: 7, CredentialName: "Primary",
				SourceModel: "claude-sonnet-4", ResponseModel: "claude-sonnet-4-20260514", Status: "success",
			},
		}}},
		summary: vnextstore.RequestLogSummary{Requests: 10, Succeeded: 9, Failed: 1, SuccessBasisPoints: 9000},
		detail: vnextstore.RequestLogDetail{
			Request: vnextstore.RequestLog{ID: "req-1", RouteRevision: 12, Status: "success", StartedAt: 100},
			RouteCandidates: []vnextstore.RequestRouteCandidate{
				{
					RequestRouteCandidateWrite: vnextstore.RequestRouteCandidateWrite{
						Position: 0, PublishedModelTargetID: 31, PublishedModelTargetRevision: 6,
						ProviderModelTargetID: 41, ProviderModelTargetRevision: 7,
						SiteID: 5, SiteName: "Relay A", EndpointID: 6, EndpointName: "Messages",
						SourceModel: "claude-sonnet-4", WireProtocol: "anthropic", APISurface: "anthropic.messages",
						Credentials: []vnextstore.RequestRouteCredentialSnapshot{
							{ID: 7, Name: "Primary", Position: 0, RuntimeState: "active"},
							{ID: 8, Name: "Backup", Position: 1, RuntimeState: "cooling", CoolingUntil: &coolingUntil},
						},
						InitialEligibility: "eligible", InitialReason: "ready",
					},
					RequestID: "req-1", Disposition: "attempted", DispositionReason: "failed",
					AttemptCount: 2, FirstAttemptIndex: &firstAttemptIndex, LastAttemptIndex: &lastAttemptIndex,
				},
				{
					RequestRouteCandidateWrite: vnextstore.RequestRouteCandidateWrite{
						Position: 1, PublishedModelTargetID: 32, PublishedModelTargetRevision: 3,
						ProviderModelTargetID: 42, ProviderModelTargetRevision: 9,
						SiteID: 15, SiteName: "Relay B", EndpointID: 16, EndpointName: "Fallback",
						SourceModel: "claude-sonnet-4-latest", WireProtocol: "openai", APISurface: "openai.chat_completions",
						InitialEligibility: "skipped", InitialReason: "target_cooling",
					},
					RequestID: "req-1", Disposition: "skipped", DispositionReason: "target_cooling",
				},
			},
			Attempts: []vnextstore.RequestAttempt{{
				RequestID: "req-1", AttemptIndex: 0, PublishedModelTargetID: 31,
				PublishedModelTargetRevision: 6, SiteName: "Relay A", SwitchReason: "retryable_upstream",
			}},
		},
	}
	handler, err := New(repository)
	if err != nil {
		t.Fatal(err)
	}

	list := request(handler, "/api/vnext/request-logs?model=claude-sonnet&status=success")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"reasoningEffort":"high"`) ||
		!strings.Contains(list.Body.String(), `"publishedModelId":17`) ||
		!strings.Contains(list.Body.String(), `"routeRevision":8`) ||
		!strings.Contains(list.Body.String(), `"effectiveRoutingProfileName":"Low latency"`) ||
		!strings.Contains(list.Body.String(), `"sourceRoutingProfileName":"Default"`) ||
		!strings.Contains(list.Body.String(), `"finalAttempt":{"id":0,"attemptIndex":1`) ||
		!strings.Contains(list.Body.String(), `"siteName":"Relay B"`) ||
		!strings.Contains(list.Body.String(), `"sourceModel":"claude-sonnet-4"`) ||
		!strings.Contains(list.Body.String(), `"responseModel":"claude-sonnet-4-20260514"`) ||
		!strings.Contains(list.Body.String(), `"meteringStatus":"unavailable"`) ||
		!strings.Contains(list.Body.String(), `"meteringErrorCode":"usage_unavailable"`) ||
		!strings.Contains(list.Body.String(), `"billingMultiplierBPS":12500`) ||
		!strings.Contains(list.Body.String(), `"status":"success"`) ||
		strings.Contains(list.Body.String(), "keyModelRoute") {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}
	if repository.lastFilter.PublicModel != "claude-sonnet" || repository.lastFilter.Status != "success" {
		t.Fatalf("filter = %+v", repository.lastFilter)
	}
	summary := request(handler, "/api/vnext/request-logs/summary")
	if summary.Code != http.StatusOK || !strings.Contains(summary.Body.String(), `"successBasisPoints":9000`) {
		t.Fatalf("summary = %d %s", summary.Code, summary.Body.String())
	}
	detail := request(handler, "/api/vnext/request-logs/req-1")
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"switchReason":"retryable_upstream"`) ||
		!strings.Contains(detail.Body.String(), `"routeRevision":12`) ||
		!strings.Contains(detail.Body.String(), `"routeCandidates":[{"position":0`) ||
		!strings.Contains(detail.Body.String(), `"initialEligibility":"eligible"`) ||
		!strings.Contains(detail.Body.String(), `"disposition":"attempted"`) ||
		!strings.Contains(detail.Body.String(), `"attemptCount":2`) ||
		!strings.Contains(detail.Body.String(), `"runtimeState":"cooling","coolingUntil":9000`) ||
		!strings.Contains(detail.Body.String(), `"publishedModelTargetId":31`) ||
		!strings.Contains(detail.Body.String(), `"publishedModelTargetRevision":6`) ||
		strings.Contains(detail.Body.String(), "keyRouteTarget") {
		t.Fatalf("detail = %d %s", detail.Code, detail.Body.String())
	}
	if strings.Index(detail.Body.String(), `"siteName":"Relay A"`) > strings.Index(detail.Body.String(), `"siteName":"Relay B"`) {
		t.Fatalf("route candidate order changed: %s", detail.Body.String())
	}
}

func TestHandlerRejectsInvalidCursorAndReturnsNotFound(t *testing.T) {
	repository := &logsRepositoryFake{detailErr: sql.ErrNoRows}
	handler, err := New(repository)
	if err != nil {
		t.Fatal(err)
	}
	invalid := request(handler, "/api/vnext/request-logs?cursor=bad")
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor status = %d", invalid.Code)
	}
	missing := request(handler, "/api/vnext/request-logs/missing")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d", missing.Code)
	}
}

type logsRepositoryFake struct {
	page       vnextstore.RequestLogPage
	summary    vnextstore.RequestLogSummary
	detail     vnextstore.RequestLogDetail
	detailErr  error
	lastFilter vnextstore.RequestLogFilter
}

func (repository *logsRepositoryFake) ListRequestLogs(_ context.Context, filter vnextstore.RequestLogFilter) (vnextstore.RequestLogPage, error) {
	repository.lastFilter = filter
	return repository.page, nil
}

func (repository *logsRepositoryFake) SummarizeRequestLogs(_ context.Context, filter vnextstore.RequestLogFilter) (vnextstore.RequestLogSummary, error) {
	repository.lastFilter = filter
	return repository.summary, nil
}

func (repository *logsRepositoryFake) GetRequestLogDetail(context.Context, string) (vnextstore.RequestLogDetail, error) {
	return repository.detail, repository.detailErr
}

func request(handler http.Handler, path string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	return response
}

var _ Repository = (*logsRepositoryFake)(nil)
