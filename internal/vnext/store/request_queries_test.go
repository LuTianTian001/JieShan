package store

import (
	"context"
	"testing"
)

func TestRequestLogQueriesExposeStableFiltersSummaryAndAttemptTimeline(t *testing.T) {
	storage := newTestStore(t)
	ctx := context.Background()
	quota := int64(10_000)
	fixture := createAccountingFixture(t, storage, &quota)

	startOne := accountingRequestStart(fixture, "req-log-one", 100)
	startOne.StartedAt = 1_000
	if _, err := storage.StartRequestWithQuotaReservation(ctx, startOne); err != nil {
		t.Fatal(err)
	}
	firstAttempt := accountingAttempt(fixture, startOne.ID, 0)
	firstAttempt.Status = "failed"
	firstAttempt.HTTPStatus = intPointer(503)
	firstAttempt.FailureKind = "upstream_server"
	firstAttempt.ErrorCode = "http_503"
	firstAttempt.SwitchReason = "retryable_upstream"
	firstAttempt.FirstTokenMS = nil
	firstAttempt.DurationMS = 80
	firstAttempt.StartedAt = 1_000
	firstAttempt.FinishedAt = 1_080
	if err := storage.RecordRequestAttempt(ctx, firstAttempt); err != nil {
		t.Fatal(err)
	}
	secondAttempt := accountingAttempt(fixture, startOne.ID, 1)
	secondAttempt.SiteName = "Final relay"
	secondAttempt.EndpointName = "Final endpoint"
	secondAttempt.CredentialName = "Final key"
	secondAttempt.SourceModel = "final-model"
	secondAttempt.StartedAt = 1_081
	secondAttempt.FinishedAt = 1_201
	secondAttempt.DurationMS = 120
	if err := storage.RecordRequestAttempt(ctx, secondAttempt); err != nil {
		t.Fatal(err)
	}
	settlementOne := accountingSettlement(1, 70)
	settlementOne.DurationMS = 201
	settlementOne.FinishedAt = 1_201
	if _, err := storage.SettleRequest(ctx, startOne.ID, settlementOne); err != nil {
		t.Fatal(err)
	}

	startTwo := accountingRequestStart(fixture, "req-log-two", 80)
	startTwo.StartedAt = 2_000
	if _, err := storage.StartRequestWithQuotaReservation(ctx, startTwo); err != nil {
		t.Fatal(err)
	}
	attemptTwo := accountingAttempt(fixture, startTwo.ID, 0)
	attemptTwo.StartedAt = 2_000
	attemptTwo.FinishedAt = 2_100
	if err := storage.RecordRequestAttempt(ctx, attemptTwo); err != nil {
		t.Fatal(err)
	}
	settlementTwo := accountingSettlement(0, 50)
	settlementTwo.FinishedAt = 2_100
	if _, err := storage.SettleRequest(ctx, startTwo.ID, settlementTwo); err != nil {
		t.Fatal(err)
	}

	page, err := storage.ListRequestLogs(ctx, RequestLogFilter{Limit: 1, Search: "Accounting upstream"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != startTwo.ID || !page.HasMore || page.NextBeforeStartedAt == nil {
		t.Fatalf("first page = %+v", page)
	}
	if page.Items[0].FinalAttempt == nil || page.Items[0].FinalAttempt.AttemptIndex != 0 ||
		page.Items[0].FinalAttempt.SiteName != attemptTwo.SiteName || page.Items[0].FinalAttempt.Status != attemptTwo.Status {
		t.Fatalf("first page final attempt = %+v", page.Items[0].FinalAttempt)
	}
	secondPage, err := storage.ListRequestLogs(ctx, RequestLogFilter{
		Limit: 1, Search: "Accounting upstream", BeforeStartedAt: page.NextBeforeStartedAt, BeforeID: page.NextBeforeID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(secondPage.Items) != 1 || secondPage.Items[0].ID != startOne.ID || secondPage.HasMore {
		t.Fatalf("second page = %+v", secondPage)
	}
	if secondPage.Items[0].FinalAttempt == nil || secondPage.Items[0].FinalAttempt.AttemptIndex != 1 ||
		secondPage.Items[0].FinalAttempt.SiteName != "Final relay" ||
		secondPage.Items[0].FinalAttempt.EndpointName != "Final endpoint" ||
		secondPage.Items[0].FinalAttempt.SourceModel != "final-model" ||
		secondPage.Items[0].FinalAttempt.Status != "success" {
		t.Fatalf("second page final attempt = %+v", secondPage.Items[0].FinalAttempt)
	}

	summary, err := storage.SummarizeRequestLogs(ctx, RequestLogFilter{SiteID: &fixture.siteID})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Requests != 2 || summary.Succeeded != 2 || summary.SuccessBasisPoints != 10_000 ||
		summary.TotalAttempts != 3 || summary.RequestsWithSwitches != 1 || summary.P95DurationMS == nil || *summary.P95DurationMS != 201 {
		t.Fatalf("summary = %+v", summary)
	}
	detail, err := storage.GetRequestLogDetail(ctx, startOne.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Attempts) != 2 || detail.Attempts[0].SwitchReason != "retryable_upstream" || len(detail.Ledger) != 2 {
		t.Fatalf("detail = %+v", detail)
	}
}
