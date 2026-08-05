package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newBillingTestStore(t *testing.T, quota int64) (*Store, int64, int64) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "billing.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := NowMS()
	result, err := s.DB.ExecContext(ctx, `INSERT INTO routes(public_model,created_at,updated_at) VALUES (?,?,?)`, "priced-model", now, now)
	if err != nil {
		t.Fatalf("insert route: %v", err)
	}
	routeID, _ := result.LastInsertId()
	keyID, err := s.CreateDownstreamKey(ctx, DownstreamKeyWrite{Name: "metered", Enabled: true, QuotaMicroUSD: &quota}, "js_test", "js_test_billing_key")
	if err != nil {
		t.Fatalf("CreateDownstreamKey() error = %v", err)
	}
	return s, keyID, routeID
}

func requestStart(id string, keyID, routeID int64) RequestStart {
	return RequestStart{ID: id, DownstreamKeyID: keyID, RouteID: routeID, RouteRevision: 1, RequestedModel: "priced-model", StartedAt: NowMS()}
}

func TestConcurrentQuotaReservationAdmitsOnlyAvailableRequest(t *testing.T) {
	s, keyID, routeID := newBillingTestStore(t, 100)
	ctx := context.Background()
	start := make(chan struct{})
	type result struct {
		id  string
		err error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, id := range []string{"request-a", "request-b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			<-start
			results <- result{id: id, err: s.StartRequestWithReservation(ctx, requestStart(id, keyID, routeID), 60, true)}
		}(id)
	}
	close(start)
	wg.Wait()
	close(results)

	successes := make([]string, 0, 1)
	quotaFailures := 0
	for item := range results {
		switch {
		case item.err == nil:
			successes = append(successes, item.id)
		case errors.Is(item.err, ErrQuotaExceeded):
			quotaFailures++
		default:
			t.Fatalf("unexpected reservation error: %v", item.err)
		}
	}
	if len(successes) != 1 || quotaFailures != 1 {
		t.Fatalf("successes=%v quotaFailures=%d", successes, quotaFailures)
	}
	key, err := s.GetDownstreamKey(ctx, keyID)
	if err != nil {
		t.Fatalf("GetDownstreamKey() error = %v", err)
	}
	if key.ReservedMicroUSD != 60 || key.UsedMicroUSD != 0 {
		t.Fatalf("key after concurrent reserve = %+v", key)
	}
	var requestCount, reserveCount int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_logs`).Scan(&requestCount); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM quota_ledger WHERE entry_type='reserve' AND amount_micro_usd=60`).Scan(&reserveCount); err != nil {
		t.Fatal(err)
	}
	if requestCount != 1 || reserveCount != 1 {
		t.Fatalf("request logs=%d reserve entries=%d", requestCount, reserveCount)
	}
}

func TestFailedRequestReleasesReservationAtomically(t *testing.T) {
	s, keyID, routeID := newBillingTestStore(t, 100)
	ctx := context.Background()
	const requestID = "failed-request"
	if err := s.StartRequestWithReservation(ctx, requestStart(requestID, keyID, routeID), 60, true); err != nil {
		t.Fatalf("StartRequestWithReservation() error = %v", err)
	}
	finish := RequestFinish{Status: "failed", HTTPStatus: 503, DurationMS: 10, ErrorMessage: "upstream unavailable", FinishedAt: NowMS()}
	if err := s.FinishRequestAndSettle(ctx, requestID, keyID, 60, 0, finish); err != nil {
		t.Fatalf("FinishRequestAndSettle() error = %v", err)
	}
	key, err := s.GetDownstreamKey(ctx, keyID)
	if err != nil {
		t.Fatal(err)
	}
	if key.ReservedMicroUSD != 0 || key.UsedMicroUSD != 0 {
		t.Fatalf("failed request did not release quota: %+v", key)
	}
	logItem, _, err := s.GetRequestLog(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if logItem.Status != "failed" || logItem.CostMicroUSD != 0 {
		t.Fatalf("failed request log = %+v", logItem)
	}
	var reserves, releases int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM quota_ledger WHERE request_id=? AND entry_type='reserve' AND amount_micro_usd=60`, requestID).Scan(&reserves); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM quota_ledger WHERE request_id=? AND entry_type='release' AND amount_micro_usd=60`, requestID).Scan(&releases); err != nil {
		t.Fatal(err)
	}
	if reserves != 1 || releases != 1 {
		t.Fatalf("reserve entries=%d release entries=%d", reserves, releases)
	}
}

func TestSuccessfulSettlementUpdatesUsageLogAndLedger(t *testing.T) {
	s, keyID, routeID := newBillingTestStore(t, 1_000)
	ctx := context.Background()
	const requestID = "successful-request"
	if err := s.StartRequestWithReservation(ctx, requestStart(requestID, keyID, routeID), 100, true); err != nil {
		t.Fatalf("StartRequestWithReservation() error = %v", err)
	}
	input, output := int64(10), int64(3)
	finish := RequestFinish{ActualModel: "priced-model", Status: "success", HTTPStatus: 200, DurationMS: 12,
		InputTokens: &input, OutputTokens: &output, CostMicroUSD: 40, PriceSnapshotJSON: `{"version":"test"}`, FinishedAt: NowMS()}
	if err := s.FinishRequestAndSettle(ctx, requestID, keyID, 100, 40, finish); err != nil {
		t.Fatalf("FinishRequestAndSettle() error = %v", err)
	}
	key, err := s.GetDownstreamKey(ctx, keyID)
	if err != nil {
		t.Fatal(err)
	}
	if key.ReservedMicroUSD != 0 || key.UsedMicroUSD != 40 {
		t.Fatalf("settled key = %+v", key)
	}
	var cost int64
	var snapshot string
	if err := s.DB.QueryRowContext(ctx, `SELECT cost_micro_usd,price_snapshot_json FROM request_logs WHERE id=?`, requestID).Scan(&cost, &snapshot); err != nil {
		t.Fatal(err)
	}
	if cost != 40 || snapshot != `{"version":"test"}` {
		t.Fatalf("cost=%d snapshot=%q", cost, snapshot)
	}
	var settled, released int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM quota_ledger WHERE request_id=? AND entry_type='settle' AND amount_micro_usd=40`, requestID).Scan(&settled); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM quota_ledger WHERE request_id=? AND entry_type='release' AND amount_micro_usd=60`, requestID).Scan(&released); err != nil {
		t.Fatal(err)
	}
	if settled != 1 || released != 1 {
		t.Fatalf("settled entries=%d released entries=%d", settled, released)
	}
}

func TestSettlementChargesAboveReservationWhenQuotaIsAvailable(t *testing.T) {
	s, keyID, routeID := newBillingTestStore(t, 1_000)
	ctx := context.Background()
	const requestID = "additional-charge"
	if err := s.StartRequestWithReservation(ctx, requestStart(requestID, keyID, routeID), 60, true); err != nil {
		t.Fatal(err)
	}
	finish := RequestFinish{Status: "success", HTTPStatus: 200, CostMicroUSD: 100, FinishedAt: NowMS()}
	if err := s.FinishRequestAndSettle(ctx, requestID, keyID, 60, 100, finish); err != nil {
		t.Fatal(err)
	}
	key, err := s.GetDownstreamKey(ctx, keyID)
	if err != nil {
		t.Fatal(err)
	}
	if key.ReservedMicroUSD != 0 || key.UsedMicroUSD != 100 {
		t.Fatalf("settled key = %+v", key)
	}
	var additional int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM quota_ledger WHERE request_id=? AND entry_type='additional' AND amount_micro_usd=40`, requestID).Scan(&additional); err != nil {
		t.Fatal(err)
	}
	if additional != 1 {
		t.Fatalf("additional ledger entries = %d", additional)
	}
}

func TestSettlementRecordsOfficialCostAndExhaustsFiniteQuota(t *testing.T) {
	s, keyID, routeID := newBillingTestStore(t, 80)
	ctx := context.Background()
	const requestID = "quota-exhausted-on-settlement"
	if err := s.StartRequestWithReservation(ctx, requestStart(requestID, keyID, routeID), 60, true); err != nil {
		t.Fatal(err)
	}
	finish := RequestFinish{Status: "success", HTTPStatus: 200, CostMicroUSD: 100, FinishedAt: NowMS()}
	if err := s.FinishRequestAndSettle(ctx, requestID, keyID, 60, 100, finish); err != nil {
		t.Fatal(err)
	}
	key, err := s.GetDownstreamKey(ctx, keyID)
	if err != nil {
		t.Fatal(err)
	}
	if key.ReservedMicroUSD != 0 || key.UsedMicroUSD != 80 {
		t.Fatalf("finite key was not exhausted safely: %+v", key)
	}
	logItem, _, err := s.GetRequestLog(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if logItem.CostMicroUSD != 100 || !strings.Contains(logItem.ErrorMessage, "remaining downstream quota") {
		t.Fatalf("request log did not retain official cost and exhaustion note: %+v", logItem)
	}
}
