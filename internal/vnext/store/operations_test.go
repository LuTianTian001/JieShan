package store

import (
	"context"
	"testing"
)

func TestSummarizeMeteringDegradationGroupsRecentRequests(t *testing.T) {
	storage := newTestStore(t)
	quota := int64(1_000)
	fixture := createAccountingFixture(t, storage, &quota)
	ctx := context.Background()

	writeUnavailableRequest(t, ctx, storage, fixture, "recent-a", "usage_unavailable", 10_000, 11_000)
	writeUnavailableRequest(t, ctx, storage, fixture, "recent-b", "usage_unavailable", 11_000, 12_000)
	writeUnavailableRequest(t, ctx, storage, fixture, "old", "pricing_settlement_failed", 8_000, 9_000)

	items, err := storage.SummarizeMeteringDegradation(ctx, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Code != "usage_unavailable" || items[0].AffectedRequests != 2 ||
		items[0].Since != 11_000 || items[0].LastSeenAt != 12_000 {
		t.Fatalf("metering degradation summary = %+v", items)
	}
}

func writeUnavailableRequest(
	t *testing.T,
	ctx context.Context,
	storage *Store,
	fixture accountingFixture,
	requestID string,
	code string,
	startedAt int64,
	finishedAt int64,
) {
	t.Helper()
	start := accountingRequestStart(fixture, requestID, 100)
	start.StartedAt = startedAt
	if _, err := storage.StartRequestWithQuotaReservation(ctx, start); err != nil {
		t.Fatal(err)
	}
	attempt := accountingAttempt(fixture, requestID, 0)
	attempt.StartedAt = startedAt
	attempt.FinishedAt = finishedAt
	attempt.DurationMS = finishedAt - startedAt
	if err := storage.RecordRequestAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	settlement := accountingSettlement(0, 0)
	settlement.MeteringStatus = "unavailable"
	settlement.MeteringErrorCode = code
	settlement.FinishedAt = finishedAt
	settlement.DurationMS = finishedAt - startedAt
	settlement.InputTokens = nil
	settlement.OutputTokens = nil
	settlement.CacheReadTokens = nil
	settlement.CacheWriteTokens = nil
	settlement.CacheWrite5MTokens = nil
	settlement.CacheWrite1HTokens = nil
	settlement.ReasoningTokens = nil
	if _, err := storage.SettleRequest(ctx, requestID, settlement); err != nil {
		t.Fatal(err)
	}
}
