package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestConcurrentQuotaReservationAdmitsOnlyOneRequest(t *testing.T) {
	s := newTestStore(t)
	quota := int64(100)
	fixture := createAccountingFixture(t, s, &quota)

	type outcome struct {
		result RequestStartResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			result, err := s.StartRequestWithQuotaReservation(
				context.Background(), accountingRequestStart(fixture, fmt.Sprintf("concurrent-%d", index), 100),
			)
			outcomes <- outcome{result: result, err: err}
		}(index)
	}
	close(start)
	wait.Wait()
	close(outcomes)

	admitted := 0
	rejected := 0
	for outcome := range outcomes {
		switch {
		case outcome.err == nil && !outcome.result.AlreadyStarted:
			admitted++
		case errors.Is(outcome.err, ErrQuotaExceeded):
			rejected++
		default:
			t.Fatalf("unexpected reservation outcome: result=%+v err=%v", outcome.result, outcome.err)
		}
	}
	if admitted != 1 || rejected != 1 {
		t.Fatalf("admitted=%d rejected=%d, want 1/1", admitted, rejected)
	}

	key, err := s.GetDownstreamKey(context.Background(), fixture.keyID)
	if err != nil {
		t.Fatal(err)
	}
	if key.UsedNanoUSD != 0 || key.ReservedNanoUSD != 100 {
		t.Fatalf("key accounting = used %d reserved %d", key.UsedNanoUSD, key.ReservedNanoUSD)
	}
	assertTableCount(t, s, "request_logs", 1)
	assertTableCount(t, s, "quota_ledger", 1)
}

func TestRequestAccountingIsIdempotentAndFreezesPriceSnapshot(t *testing.T) {
	s := newTestStore(t)
	quota := int64(500)
	fixture := createAccountingFixture(t, s, &quota)
	ctx := context.Background()
	start := accountingRequestStart(fixture, "request-idempotent", 100)

	first, err := s.StartRequestWithQuotaReservation(ctx, start)
	if err != nil || first.AlreadyStarted {
		t.Fatalf("first start = %+v, %v", first, err)
	}
	replayed, err := s.StartRequestWithQuotaReservation(ctx, start)
	if err != nil || !replayed.AlreadyStarted {
		t.Fatalf("replayed start = %+v, %v", replayed, err)
	}
	conflictingStart := start
	conflictingStart.PriceSKU = "different-sku"
	if _, err := s.StartRequestWithQuotaReservation(ctx, conflictingStart); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("conflicting start error = %v", err)
	}

	attempt := accountingAttempt(fixture, start.ID, 0)
	if err := s.RecordRequestAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRequestAttempt(ctx, attempt); err != nil {
		t.Fatalf("identical attempt replay: %v", err)
	}
	conflictingAttempt := attempt
	conflictingAttempt.DurationMS++
	if err := s.RecordRequestAttempt(ctx, conflictingAttempt); !errors.Is(err, ErrAttemptConflict) {
		t.Fatalf("conflicting attempt error = %v", err)
	}

	if _, err := s.DB.ExecContext(ctx, `UPDATE published_models
SET official_price_sku='new-route-sku',revision=revision+1 WHERE id=?`, fixture.publishedModelID); err != nil {
		t.Fatal(err)
	}
	settlement := accountingSettlement(0, 70)
	settled, err := s.SettleRequest(ctx, start.ID, settlement)
	if err != nil || settled.AlreadySettled || settled.ChargedNanoUSD != 70 || settled.QuotaCapped {
		t.Fatalf("first settlement = %+v, %v", settled, err)
	}
	replayedSettlement, err := s.SettleRequest(ctx, start.ID, settlement)
	if err != nil || !replayedSettlement.AlreadySettled || replayedSettlement.ChargedNanoUSD != 70 {
		t.Fatalf("replayed settlement = %+v, %v", replayedSettlement, err)
	}
	conflictingSettlement := settlement
	conflictingSettlement.OfficialCostNanoUSD++
	if _, err := s.SettleRequest(ctx, start.ID, conflictingSettlement); !errors.Is(err, ErrSettlementConflict) {
		t.Fatalf("conflicting settlement error = %v", err)
	}
	conflictingCacheWrite := settlement
	changedCacheWrite := *settlement.CacheWriteTokens + 1
	conflictingCacheWrite.CacheWriteTokens = &changedCacheWrite
	if _, err := s.SettleRequest(ctx, start.ID, conflictingCacheWrite); !errors.Is(err, ErrSettlementConflict) {
		t.Fatalf("conflicting generic cache-write settlement error = %v", err)
	}
	if err := s.RecordRequestAttempt(ctx, attempt); err != nil {
		t.Fatalf("identical attempt replay after settlement: %v", err)
	}
	lateAttempt := attempt
	lateAttempt.AttemptIndex = 1
	if err := s.RecordRequestAttempt(ctx, lateAttempt); !errors.Is(err, ErrRequestNotRunning) {
		t.Fatalf("new attempt after settlement error = %v", err)
	}

	request, err := s.GetRequestLog(ctx, start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if request.PriceCatalogVersion != start.PriceCatalogVersion || request.PriceSKU != start.PriceSKU ||
		request.CacheWriteTokens == nil || *request.CacheWriteTokens != *settlement.CacheWriteTokens ||
		request.CacheWrite5MTokens != nil || request.CacheWrite1HTokens != nil ||
		request.OfficialCostNanoUSD != 70 || request.ChargedNanoUSD != 70 || request.Status != "success" {
		t.Fatalf("settled request snapshot = %+v", request)
	}
	key, err := s.GetDownstreamKey(ctx, fixture.keyID)
	if err != nil {
		t.Fatal(err)
	}
	if key.UsedNanoUSD != 70 || key.ReservedNanoUSD != 0 {
		t.Fatalf("key accounting = used %d reserved %d", key.UsedNanoUSD, key.ReservedNanoUSD)
	}
	ledger, err := s.ListQuotaLedger(ctx, start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger) != 2 || ledger[0].EventType != "reserve" || ledger[1].EventType != "settle" ||
		ledger[0].ReservedDeltaNanoUSD != 100 || ledger[1].ReservedDeltaNanoUSD != -100 ||
		ledger[1].UsedDeltaNanoUSD != 70 {
		t.Fatalf("ledger = %+v", ledger)
	}
	for _, entry := range ledger {
		if entry.PriceCatalogVersion != start.PriceCatalogVersion || entry.PriceSKU != start.PriceSKU {
			t.Fatalf("ledger price snapshot changed: %+v", entry)
		}
	}
}

func TestUnavailableMeteringReleasesReservationWithoutRecordingZeroUsage(t *testing.T) {
	s := newTestStore(t)
	quota := int64(500)
	fixture := createAccountingFixture(t, s, &quota)
	ctx := context.Background()
	start := accountingRequestStart(fixture, "request-usage-unavailable", 100)
	if _, err := s.StartRequestWithQuotaReservation(ctx, start); err != nil {
		t.Fatal(err)
	}
	attempt := accountingAttempt(fixture, start.ID, 0)
	attempt.ResponseModel = "reported-model"
	if err := s.RecordRequestAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}

	status := 200
	finalAttempt := 0
	settled, err := s.SettleRequest(ctx, start.ID, RequestSettlement{
		Status: "success", MeteringStatus: "unavailable", MeteringErrorCode: "usage_unavailable",
		FinalAttemptIndex: &finalAttempt, HTTPStatus: &status, DurationMS: 100,
		OfficialCostNanoUSD: 0, FinishedAt: 1_100,
	})
	if err != nil || settled.ChargedNanoUSD != 0 || settled.QuotaCapped {
		t.Fatalf("settlement = %+v, error = %v", settled, err)
	}

	request, err := s.GetRequestLog(ctx, start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if request.Status != "success" || request.MeteringStatus != "unavailable" ||
		request.MeteringErrorCode != "usage_unavailable" || request.InputTokens != nil ||
		request.OutputTokens != nil || request.CacheReadTokens != nil || request.CacheWriteTokens != nil ||
		request.CacheWrite5MTokens != nil || request.CacheWrite1HTokens != nil || request.ReasoningTokens != nil ||
		request.OfficialCostNanoUSD != 0 || request.ChargedNanoUSD != 0 {
		t.Fatalf("request log = %+v", request)
	}
	attempts, err := s.ListRequestAttempts(ctx, start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Status != "success" || attempts[0].ResponseModel != "reported-model" {
		t.Fatalf("attempts = %+v", attempts)
	}
	key, err := s.GetDownstreamKey(ctx, fixture.keyID)
	if err != nil {
		t.Fatal(err)
	}
	if key.UsedNanoUSD != 0 || key.ReservedNanoUSD != 0 {
		t.Fatalf("key accounting = used %d reserved %d", key.UsedNanoUSD, key.ReservedNanoUSD)
	}
	ledger, err := s.ListQuotaLedger(ctx, start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger) != 2 || ledger[0].EventType != "reserve" || ledger[0].ReservedDeltaNanoUSD != 100 ||
		ledger[1].EventType != "settle" || ledger[1].ReservedDeltaNanoUSD != -100 || ledger[1].UsedDeltaNanoUSD != 0 {
		t.Fatalf("ledger = %+v", ledger)
	}
}

func TestUnavailableMeteringCannotChargeOrPersistInventedUsage(t *testing.T) {
	s := newTestStore(t)
	quota := int64(500)
	fixture := createAccountingFixture(t, s, &quota)
	ctx := context.Background()
	start := accountingRequestStart(fixture, "request-invalid-unmetered-charge", 100)
	if _, err := s.StartRequestWithQuotaReservation(ctx, start); err != nil {
		t.Fatal(err)
	}

	inputTokens := int64(3)
	invalid := []RequestSettlement{
		{
			Status: "success", MeteringStatus: "unavailable", MeteringErrorCode: "usage_unavailable",
			DurationMS: 100, OfficialCostNanoUSD: 25, FinishedAt: 1_100,
		},
		{
			Status: "success", MeteringStatus: "unavailable", MeteringErrorCode: "usage_unavailable",
			DurationMS: 100, InputTokens: &inputTokens, FinishedAt: 1_100,
		},
		{
			Status: "success", MeteringStatus: "unavailable", DurationMS: 100, FinishedAt: 1_100,
		},
	}
	for index, settlement := range invalid {
		if _, err := s.SettleRequest(ctx, start.ID, settlement); err == nil {
			t.Fatalf("invalid settlement %d unexpectedly succeeded", index)
		}
	}
	key, err := s.GetDownstreamKey(ctx, fixture.keyID)
	if err != nil {
		t.Fatal(err)
	}
	if key.UsedNanoUSD != 0 || key.ReservedNanoUSD != 100 {
		t.Fatalf("invalid settlement changed quota: used=%d reserved=%d", key.UsedNanoUSD, key.ReservedNanoUSD)
	}

	if _, err := s.SettleRequest(ctx, start.ID, RequestSettlement{
		Status: "success", MeteringStatus: "unavailable", MeteringErrorCode: "usage_unavailable",
		DurationMS: 100, FinishedAt: 1_100,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverInterruptedRequestsReleasesReservationWithoutCharging(t *testing.T) {
	s := newTestStore(t)
	quota := int64(500)
	fixture := createAccountingFixture(t, s, &quota)
	ctx := context.Background()
	start := accountingRequestStart(fixture, "request-interrupted", 120)
	if _, err := s.StartRequestWithQuotaReservation(ctx, start); err != nil {
		t.Fatal(err)
	}
	attempt := accountingAttempt(fixture, start.ID, 0)
	if err := s.RecordRequestAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}

	finishedAt := start.StartedAt + 30_000
	recovered, err := s.RecoverInterruptedRequests(ctx, finishedAt)
	if err != nil || recovered != 1 {
		t.Fatalf("recovery = %d, error = %v", recovered, err)
	}
	request, err := s.GetRequestLog(ctx, start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if request.Status != "failed" || request.ErrorCode != "runtime_interrupted" || request.ChargedNanoUSD != 0 ||
		request.FinalAttemptIndex == nil || *request.FinalAttemptIndex != 0 {
		t.Fatalf("recovered request = %+v", request)
	}
	key, err := s.GetDownstreamKey(ctx, fixture.keyID)
	if err != nil {
		t.Fatal(err)
	}
	if key.ReservedNanoUSD != 0 || key.UsedNanoUSD != 0 {
		t.Fatalf("recovered key accounting = used %d reserved %d", key.UsedNanoUSD, key.ReservedNanoUSD)
	}
	ledger, err := s.ListQuotaLedger(ctx, start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger) != 2 || ledger[1].EventType != "settle" || ledger[1].ReservedDeltaNanoUSD != -120 || ledger[1].UsedDeltaNanoUSD != 0 {
		t.Fatalf("recovery ledger = %+v", ledger)
	}
	if recovered, err = s.RecoverInterruptedRequests(ctx, finishedAt+1); err != nil || recovered != 0 {
		t.Fatalf("idempotent recovery = %d, error = %v", recovered, err)
	}
}

func TestSettlementCapsChargeAtRemainingQuota(t *testing.T) {
	s := newTestStore(t)
	quota := int64(50)
	fixture := createAccountingFixture(t, s, &quota)
	ctx := context.Background()
	start := accountingRequestStart(fixture, "request-capped", 30)
	if _, err := s.StartRequestWithQuotaReservation(ctx, start); err != nil {
		t.Fatal(err)
	}

	settled, err := s.SettleRequest(ctx, start.ID, RequestSettlement{
		Status: "success", DurationMS: 25, OfficialCostNanoUSD: 80, FinishedAt: start.StartedAt + 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if settled.ChargedNanoUSD != 50 || !settled.QuotaCapped {
		t.Fatalf("settlement = %+v", settled)
	}
	request, err := s.GetRequestLog(ctx, start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if request.OfficialCostNanoUSD != 80 || request.ChargedNanoUSD != 50 || !request.QuotaCapped {
		t.Fatalf("request costs = %+v", request)
	}
	key, err := s.GetDownstreamKey(ctx, fixture.keyID)
	if err != nil {
		t.Fatal(err)
	}
	if key.UsedNanoUSD != 50 || key.ReservedNanoUSD != 0 {
		t.Fatalf("key accounting = used %d reserved %d", key.UsedNanoUSD, key.ReservedNanoUSD)
	}
}

func TestHourlyQuotaAndMultiplierAreAppliedAtomically(t *testing.T) {
	s := newTestStore(t)
	totalQuota := int64(1_000)
	fixture := createAccountingFixture(t, s, &totalQuota)
	ctx := context.Background()
	key, err := s.GetDownstreamKey(ctx, fixture.keyID)
	if err != nil {
		t.Fatal(err)
	}
	hourlyQuota := int64(100)
	key, err = s.UpdateDownstreamKey(ctx, fixture.keyID, DownstreamKeyUpdate{
		ExpectedRevision:     key.Revision,
		Name:                 key.Name,
		Enabled:              true,
		QuotaNanoUSD:         &totalQuota,
		HourlyQuotaNanoUSD:   &hourlyQuota,
		BillingMultiplierBPS: 15_000,
	})
	if err != nil {
		t.Fatal(err)
	}

	start := accountingRequestStart(fixture, "hourly-multiplier", 40)
	start.StartedAt = NowMS()
	if _, err := s.StartRequestWithQuotaReservation(ctx, start); err != nil {
		t.Fatal(err)
	}
	key, err = s.GetDownstreamKey(ctx, fixture.keyID)
	if err != nil {
		t.Fatal(err)
	}
	if key.ReservedNanoUSD != 60 || key.ReservedThisHourNanoUSD != 60 ||
		key.UsedNanoUSD != 0 || key.UsedThisHourNanoUSD != 0 {
		t.Fatalf("scaled reservation was not reflected in both quota windows: %+v", key)
	}

	// Editing the multiplier after admission must not change the in-flight
	// request. Its frozen 1.5x snapshot still owns settlement.
	key, err = s.UpdateDownstreamKey(ctx, fixture.keyID, DownstreamKeyUpdate{
		ExpectedRevision:     key.Revision,
		Name:                 key.Name,
		Enabled:              true,
		QuotaNanoUSD:         &totalQuota,
		HourlyQuotaNanoUSD:   &hourlyQuota,
		BillingMultiplierBPS: 20_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	settled, err := s.SettleRequest(ctx, start.ID, RequestSettlement{
		Status: "success", DurationMS: 20, OfficialCostNanoUSD: 30, FinishedAt: start.StartedAt + 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if settled.ChargedNanoUSD != 45 || settled.QuotaCapped {
		t.Fatalf("settlement = %+v, want frozen 1.5x charge", settled)
	}
	key, err = s.GetDownstreamKey(ctx, fixture.keyID)
	if err != nil {
		t.Fatal(err)
	}
	if key.UsedNanoUSD != 45 || key.UsedThisHourNanoUSD != 45 ||
		key.ReservedNanoUSD != 0 || key.ReservedThisHourNanoUSD != 0 {
		t.Fatalf("settled quota windows = %+v", key)
	}
	request, err := s.GetRequestLog(ctx, start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if request.BillingMultiplierBPS != 15_000 || request.OfficialCostNanoUSD != 30 || request.ChargedNanoUSD != 45 {
		t.Fatalf("frozen billing snapshot = %+v", request)
	}
}

func TestHourlyQuotaCapsUnderreservedChargeAfterMultiplier(t *testing.T) {
	s := newTestStore(t)
	totalQuota := int64(1_000)
	fixture := createAccountingFixture(t, s, &totalQuota)
	ctx := context.Background()
	key, err := s.GetDownstreamKey(ctx, fixture.keyID)
	if err != nil {
		t.Fatal(err)
	}
	hourlyQuota := int64(100)
	if _, err := s.UpdateDownstreamKey(ctx, fixture.keyID, DownstreamKeyUpdate{
		ExpectedRevision: key.Revision, Name: key.Name, Enabled: true,
		QuotaNanoUSD: &totalQuota, HourlyQuotaNanoUSD: &hourlyQuota, BillingMultiplierBPS: 15_000,
	}); err != nil {
		t.Fatal(err)
	}

	start := accountingRequestStart(fixture, "hourly-underreserved-multiplier", 40)
	start.StartedAt = NowMS()
	started, err := s.StartRequestWithQuotaReservation(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	if started.ReservationNanoUSD != 60 || started.BillingMultiplierBPS != 15_000 {
		t.Fatalf("scaled admission = %+v", started)
	}
	settled, err := s.SettleRequest(ctx, start.ID, RequestSettlement{
		Status: "success", DurationMS: 20, OfficialCostNanoUSD: 80, FinishedAt: start.StartedAt + 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if settled.ChargedNanoUSD != 100 || !settled.QuotaCapped {
		t.Fatalf("hourly capped settlement = %+v", settled)
	}
	key, err = s.GetDownstreamKey(ctx, fixture.keyID)
	if err != nil {
		t.Fatal(err)
	}
	if key.UsedNanoUSD != 100 || key.UsedThisHourNanoUSD != 100 ||
		key.ReservedNanoUSD != 0 || key.ReservedThisHourNanoUSD != 0 {
		t.Fatalf("quota windows after capped settlement = %+v", key)
	}
	request, err := s.GetRequestLog(ctx, start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if request.BillingMultiplierBPS != 15_000 || request.OfficialCostNanoUSD != 80 ||
		request.ChargedNanoUSD != 100 || !request.QuotaCapped {
		t.Fatalf("capped billing audit snapshot = %+v", request)
	}
}

func TestHourlyQuotaRejectsConcurrentReservationsWithoutLeakingGlobalQuota(t *testing.T) {
	s := newTestStore(t)
	totalQuota := int64(1_000)
	fixture := createAccountingFixture(t, s, &totalQuota)
	ctx := context.Background()
	key, err := s.GetDownstreamKey(ctx, fixture.keyID)
	if err != nil {
		t.Fatal(err)
	}
	hourlyQuota := int64(100)
	if _, err := s.UpdateDownstreamKey(ctx, fixture.keyID, DownstreamKeyUpdate{
		ExpectedRevision:     key.Revision,
		Name:                 key.Name,
		Enabled:              true,
		QuotaNanoUSD:         &totalQuota,
		HourlyQuotaNanoUSD:   &hourlyQuota,
		BillingMultiplierBPS: DefaultBillingMultiplierBPS,
	}); err != nil {
		t.Fatal(err)
	}

	type outcome struct{ err error }
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			request := accountingRequestStart(fixture, fmt.Sprintf("hourly-concurrent-%d", index), 100)
			request.StartedAt = NowMS()
			_, err := s.StartRequestWithQuotaReservation(ctx, request)
			outcomes <- outcome{err: err}
		}(index)
	}
	close(start)
	wait.Wait()
	close(outcomes)
	admitted, rejected := 0, 0
	for outcome := range outcomes {
		switch {
		case outcome.err == nil:
			admitted++
		case errors.Is(outcome.err, ErrQuotaExceeded):
			rejected++
		default:
			t.Fatalf("unexpected hourly reservation error: %v", outcome.err)
		}
	}
	if admitted != 1 || rejected != 1 {
		t.Fatalf("admitted=%d rejected=%d, want 1/1", admitted, rejected)
	}
	key, err = s.GetDownstreamKey(ctx, fixture.keyID)
	if err != nil {
		t.Fatal(err)
	}
	if key.ReservedNanoUSD != 100 || key.ReservedThisHourNanoUSD != 100 {
		t.Fatalf("rejected hourly admission leaked global reservation: %+v", key)
	}
}

func TestAccountingFailuresRollbackWholeTransaction(t *testing.T) {
	s := newTestStore(t)
	quota := int64(100)
	fixture := createAccountingFixture(t, s, &quota)
	ctx := context.Background()
	start := accountingRequestStart(fixture, "request-rollback", 40)

	if _, err := s.DB.ExecContext(ctx, `CREATE TRIGGER fail_reserve_ledger
BEFORE INSERT ON quota_ledger WHEN NEW.event_type='reserve'
BEGIN SELECT RAISE(ABORT,'forced reserve ledger failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRequestWithQuotaReservation(ctx, start); err == nil {
		t.Fatal("start unexpectedly survived a ledger failure")
	}
	if _, err := s.GetRequestLog(ctx, start.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rolled-back request lookup = %v", err)
	}
	key, err := s.GetDownstreamKey(ctx, fixture.keyID)
	if err != nil {
		t.Fatal(err)
	}
	if key.UsedNanoUSD != 0 || key.ReservedNanoUSD != 0 || key.LastUsedAt != nil {
		t.Fatalf("failed start leaked key mutation: %+v", key)
	}
	assertTableCount(t, s, "quota_ledger", 0)
	if _, err := s.DB.ExecContext(ctx, `DROP TRIGGER fail_reserve_ledger`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRequestWithQuotaReservation(ctx, start); err != nil {
		t.Fatal(err)
	}

	if _, err := s.DB.ExecContext(ctx, `CREATE TRIGGER fail_settle_ledger
BEFORE INSERT ON quota_ledger WHEN NEW.event_type='settle'
BEGIN SELECT RAISE(ABORT,'forced settle ledger failure'); END`); err != nil {
		t.Fatal(err)
	}
	settlement := RequestSettlement{
		Status: "success", DurationMS: 20, OfficialCostNanoUSD: 25, FinishedAt: start.StartedAt + 20,
	}
	if _, err := s.SettleRequest(ctx, start.ID, settlement); err == nil {
		t.Fatal("settlement unexpectedly survived a ledger failure")
	}
	request, err := s.GetRequestLog(ctx, start.ID)
	if err != nil {
		t.Fatal(err)
	}
	if request.Status != "running" || request.FinishedAt != nil || request.ChargedNanoUSD != 0 {
		t.Fatalf("failed settlement mutated request: %+v", request)
	}
	key, err = s.GetDownstreamKey(ctx, fixture.keyID)
	if err != nil {
		t.Fatal(err)
	}
	if key.UsedNanoUSD != 0 || key.ReservedNanoUSD != 40 {
		t.Fatalf("failed settlement mutated key: %+v", key)
	}
	ledger, err := s.ListQuotaLedger(ctx, start.ID)
	if err != nil || len(ledger) != 1 || ledger[0].EventType != "reserve" {
		t.Fatalf("ledger after failed settlement = %+v, %v", ledger, err)
	}
	if _, err := s.DB.ExecContext(ctx, `DROP TRIGGER fail_settle_ledger`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SettleRequest(ctx, start.ID, settlement); err != nil {
		t.Fatalf("settlement after trigger removal: %v", err)
	}
}

type accountingFixture struct {
	keyID                       int64
	publishedModelID            int64
	publishedModelRevision      int64
	effectiveRoutingProfileID   int64
	effectiveRoutingProfileName string
	sourceRoutingProfileID      int64
	sourceRoutingProfileName    string
	publishedModelTargetID      int64
	publishedTargetRevision     int64
	providerTargetID            int64
	providerRevision            int64
	siteID                      int64
	endpointID                  int64
	credentialID                int64
}

func createAccountingFixture(t *testing.T, s *Store, quota *int64) accountingFixture {
	t.Helper()
	ctx := context.Background()
	siteID := mustCreateSite(t, s, "Accounting upstream")
	endpointID := mustCreateEndpoint(t, s, siteID, "Accounting endpoint", "https://accounting.example/v1")
	credentialID := mustCreateCredential(t, s, siteID, "Accounting credential")
	mustReplaceBindings(t, s, siteID, endpointID, []int64{credentialID})
	providerTargetID := mustCreateProviderTarget(t, s, siteID, endpointID, "accounting-model")
	keyID, err := s.ImportDigestOnlyDownstreamKey(ctx, DownstreamKeyWrite{
		Name: "Accounting client", KeyPrefix: "js_accounting", KeyDigest: DigestDownstreamKey("accounting-secret"),
		Enabled: true, QuotaNanoUSD: quota,
	})
	if err != nil {
		t.Fatal(err)
	}
	model, err := s.CreatePublishedModel(ctx, PublishedModelWrite{
		PublicName: "public-model", OfficialPriceSKU: "official-sku", Enabled: true,
	}, []int64{providerTargetID})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := s.GetDefaultRoutingProfile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := s.GetProviderModelTarget(ctx, providerTargetID)
	if err != nil {
		t.Fatal(err)
	}
	return accountingFixture{
		keyID: keyID, publishedModelID: model.ID, publishedModelRevision: model.Revision,
		effectiveRoutingProfileID: profile.ID, effectiveRoutingProfileName: profile.Name,
		sourceRoutingProfileID: profile.ID, sourceRoutingProfileName: profile.Name,
		publishedModelTargetID: model.Targets[0].ID, publishedTargetRevision: model.Targets[0].Revision,
		providerTargetID: providerTargetID,
		providerRevision: provider.Revision, siteID: siteID, endpointID: endpointID, credentialID: credentialID,
	}
}

func accountingRequestStart(fixture accountingFixture, id string, reservation int64) RequestStart {
	return RequestStart{
		ID: id, DownstreamKeyID: fixture.keyID,
		PublishedModelID: fixture.publishedModelID, PublishedModelRevision: fixture.publishedModelRevision,
		EffectiveRoutingProfileID:   fixture.effectiveRoutingProfileID,
		EffectiveRoutingProfileName: fixture.effectiveRoutingProfileName,
		SourceRoutingProfileID:      fixture.sourceRoutingProfileID,
		SourceRoutingProfileName:    fixture.sourceRoutingProfileName,
		RouteRevision:               fixture.publishedModelRevision,
		RouteCandidates: []RequestRouteCandidateWrite{{
			Position:                     0,
			PublishedModelTargetID:       fixture.publishedModelTargetID,
			PublishedModelTargetRevision: fixture.publishedTargetRevision,
			ProviderModelTargetID:        fixture.providerTargetID,
			ProviderModelTargetRevision:  fixture.providerRevision,
			SiteID:                       fixture.siteID,
			SiteName:                     "Accounting upstream",
			EndpointID:                   fixture.endpointID,
			EndpointName:                 "Accounting endpoint",
			SourceModel:                  "accounting-model",
			WireProtocol:                 "openai",
			APISurface:                   "openai.chat_completions",
			Credentials: []RequestRouteCredentialSnapshot{{
				ID: fixture.credentialID, Name: "Accounting credential", Position: 0, RuntimeState: "active",
			}},
			InitialEligibility: "eligible",
			InitialReason:      "ready",
		}},
		PublicModel: "public-model",
		APISurface:  "openai.chat_completions", ReasoningEffort: "medium", Stream: true,
		PriceCatalogVersion: "catalog-2026-08-06", PriceSKU: "official-sku",
		ReservationNanoUSD: reservation, StartedAt: 1_000,
	}
}

func accountingAttempt(fixture accountingFixture, requestID string, index int) RequestAttemptWrite {
	status := 200
	firstToken := int64(20)
	return RequestAttemptWrite{
		RequestID: requestID, AttemptIndex: index,
		PublishedModelTargetID:       fixture.publishedModelTargetID,
		PublishedModelTargetRevision: fixture.publishedTargetRevision,
		ProviderModelTargetID:        fixture.providerTargetID, ProviderModelTargetRevision: fixture.providerRevision,
		SiteID: fixture.siteID, EndpointID: fixture.endpointID, CredentialID: fixture.credentialID,
		SiteName: "Accounting upstream", EndpointName: "Accounting endpoint",
		CredentialName: "Accounting credential", SourceModel: "accounting-model",
		WireProtocol: "openai", APISurface: "openai.chat_completions", Status: "success",
		HTTPStatus: &status, FirstTokenMS: &firstToken, DurationMS: 100, StartedAt: 1_000, FinishedAt: 1_100,
	}
}

func accountingSettlement(finalAttemptIndex int, officialCost int64) RequestSettlement {
	status := 200
	firstToken := int64(20)
	inputTokens := int64(11)
	outputTokens := int64(7)
	cacheReadTokens := int64(3)
	cacheWriteTokens := int64(5)
	reasoningTokens := int64(2)
	return RequestSettlement{
		Status: "success", FinalAttemptIndex: &finalAttemptIndex, HTTPStatus: &status,
		FirstTokenMS: &firstToken, DurationMS: 100, InputTokens: &inputTokens, OutputTokens: &outputTokens,
		CacheReadTokens: &cacheReadTokens, CacheWriteTokens: &cacheWriteTokens, ReasoningTokens: &reasoningTokens,
		OfficialCostNanoUSD: officialCost, FinishedAt: 1_100,
	}
}

func assertTableCount(t *testing.T, s *Store, table string, want int) {
	t.Helper()
	var count int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("%s count = %d, want %d", table, count, want)
	}
}
