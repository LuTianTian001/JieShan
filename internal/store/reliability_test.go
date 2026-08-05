package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestRecoverRunningRequestsReleasesReservationsAndIsIdempotent(t *testing.T) {
	s, keyID, routeID := newBillingTestStore(t, 1_000)
	ctx := context.Background()
	const recoveredAt = int64(10_000)

	first := requestStart("orphan-a", keyID, routeID)
	first.StartedAt = 8_000
	if err := s.StartRequestWithReservation(ctx, first, 60, true); err != nil {
		t.Fatalf("reserve first request: %v", err)
	}
	second := requestStart("orphan-b", keyID, routeID)
	second.StartedAt = 8_500
	if err := s.StartRequestWithReservation(ctx, second, 70, true); err != nil {
		t.Fatalf("reserve second request: %v", err)
	}

	recovered, err := s.RecoverRunningRequests(ctx, recoveredAt)
	if err != nil {
		t.Fatalf("RecoverRunningRequests() error = %v", err)
	}
	if recovered != 2 {
		t.Fatalf("RecoverRunningRequests() recovered = %d, want 2", recovered)
	}
	key, err := s.GetDownstreamKey(ctx, keyID)
	if err != nil {
		t.Fatal(err)
	}
	if key.ReservedMicroUSD != 0 || key.UsedMicroUSD != 0 {
		t.Fatalf("recovered key = %+v", key)
	}
	for _, id := range []string{"orphan-a", "orphan-b"} {
		item, _, err := s.GetRequestLog(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if item.Status != "failed" || item.FinishedAt == nil || *item.FinishedAt != recoveredAt {
			t.Fatalf("recovered request %q = %+v", id, item)
		}
		if item.ErrorMessage != interruptedRequestMessage {
			t.Fatalf("recovered request %q error = %q", id, item.ErrorMessage)
		}
	}
	var releaseCount int
	var releasedTotal int64
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(amount_micro_usd),0)
FROM quota_ledger WHERE entry_type='release' AND request_id IN ('orphan-a','orphan-b')`).Scan(&releaseCount, &releasedTotal); err != nil {
		t.Fatal(err)
	}
	if releaseCount != 2 || releasedTotal != 130 {
		t.Fatalf("release entries=%d total=%d", releaseCount, releasedTotal)
	}

	recovered, err = s.RecoverRunningRequests(ctx, recoveredAt+1)
	if err != nil {
		t.Fatalf("second RecoverRunningRequests() error = %v", err)
	}
	if recovered != 0 {
		t.Fatalf("second RecoverRunningRequests() recovered = %d, want 0", recovered)
	}
	var releaseCountAfter int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM quota_ledger
WHERE entry_type='release' AND request_id IN ('orphan-a','orphan-b')`).Scan(&releaseCountAfter); err != nil {
		t.Fatal(err)
	}
	if releaseCountAfter != releaseCount {
		t.Fatalf("idempotent recovery added release entries: before=%d after=%d", releaseCount, releaseCountAfter)
	}
}

func TestRecoverRunningRequestsRollsBackInconsistentReservation(t *testing.T) {
	s, keyID, routeID := newBillingTestStore(t, 100)
	ctx := context.Background()
	input := requestStart("inconsistent-orphan", keyID, routeID)
	if err := s.StartRequestWithReservation(ctx, input, 60, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, "UPDATE downstream_keys SET reserved_micro_usd=10 WHERE id=?", keyID); err != nil {
		t.Fatal(err)
	}

	_, err := s.RecoverRunningRequests(ctx, NowMS())
	if !errors.Is(err, ErrInvalidQuotaState) {
		t.Fatalf("RecoverRunningRequests() error = %v, want ErrInvalidQuotaState", err)
	}
	key, err := s.GetDownstreamKey(ctx, keyID)
	if err != nil {
		t.Fatal(err)
	}
	if key.ReservedMicroUSD != 10 {
		t.Fatalf("reservation changed after rollback: %d", key.ReservedMicroUSD)
	}
	item, _, err := s.GetRequestLog(ctx, input.ID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != "running" || item.FinishedAt != nil {
		t.Fatalf("request changed after rollback: %+v", item)
	}
	var releases int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM quota_ledger WHERE request_id=? AND entry_type='release'", input.ID).Scan(&releases); err != nil {
		t.Fatal(err)
	}
	if releases != 0 {
		t.Fatalf("release ledger persisted after rollback: %d", releases)
	}
}

func TestDeleteDownstreamKeyRejectsActiveReservation(t *testing.T) {
	s, keyID, routeID := newBillingTestStore(t, 100)
	ctx := context.Background()
	input := requestStart("active-request", keyID, routeID)
	if err := s.StartRequestWithReservation(ctx, input, 60, true); err != nil {
		t.Fatal(err)
	}

	err := s.DeleteDownstreamKey(ctx, keyID)
	if !errors.Is(err, ErrDownstreamKeyHasReservations) {
		t.Fatalf("DeleteDownstreamKey() error = %v, want ErrDownstreamKeyHasReservations", err)
	}
	if !strings.Contains(err.Error(), "60 micro-USD") {
		t.Fatalf("DeleteDownstreamKey() error is not explicit: %v", err)
	}
	if _, err := s.GetDownstreamKey(ctx, keyID); err != nil {
		t.Fatalf("key was deleted despite reservation: %v", err)
	}

	finish := RequestFinish{Status: "failed", DurationMS: 1, FinishedAt: NowMS()}
	if err := s.FinishRequestAndSettle(ctx, input.ID, keyID, 60, 0, finish); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDownstreamKey(ctx, keyID); err != nil {
		t.Fatalf("DeleteDownstreamKey() after release error = %v", err)
	}
	if _, err := s.GetDownstreamKey(ctx, keyID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetDownstreamKey() after delete error = %v, want sql.ErrNoRows", err)
	}
}

func TestDeleteOldLogsAlsoDeletesOrphanedQuotaLedger(t *testing.T) {
	s, keyID, routeID := newBillingTestStore(t, 1_000)
	ctx := context.Background()

	old := requestStart("old-finished", keyID, routeID)
	old.StartedAt = 1_000
	if err := s.StartRequestWithReservation(ctx, old, 10, true); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRequestAndSettle(ctx, old.ID, keyID, 10, 0, RequestFinish{Status: "failed", DurationMS: 10, FinishedAt: 1_500}); err != nil {
		t.Fatal(err)
	}
	recent := requestStart("recent-finished", keyID, routeID)
	recent.StartedAt = 3_000
	if err := s.StartRequestWithReservation(ctx, recent, 20, true); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRequestAndSettle(ctx, recent.ID, keyID, 20, 0, RequestFinish{Status: "failed", DurationMS: 10, FinishedAt: 3_500}); err != nil {
		t.Fatal(err)
	}
	running := requestStart("old-running", keyID, routeID)
	running.StartedAt = 500
	if err := s.StartRequestWithReservation(ctx, running, 5, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO quota_ledger(downstream_key_id,request_id,entry_type,amount_micro_usd,created_at)
VALUES (?,?,?,?,?)`, keyID, "already-missing", "release", 1, 100); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteOldLogs(ctx, 2_000); err != nil {
		t.Fatalf("DeleteOldLogs() error = %v", err)
	}
	assertRowCount(t, s, "SELECT COUNT(*) FROM request_logs WHERE id='old-finished'", 0)
	assertRowCount(t, s, "SELECT COUNT(*) FROM quota_ledger WHERE request_id='old-finished'", 0)
	assertRowCount(t, s, "SELECT COUNT(*) FROM request_logs WHERE id='recent-finished'", 1)
	assertRowCount(t, s, "SELECT COUNT(*) FROM quota_ledger WHERE request_id='recent-finished'", 2)
	assertRowCount(t, s, "SELECT COUNT(*) FROM request_logs WHERE id='old-running'", 1)
	assertRowCount(t, s, "SELECT COUNT(*) FROM quota_ledger WHERE request_id='old-running'", 1)
	assertRowCount(t, s, "SELECT COUNT(*) FROM quota_ledger WHERE request_id='already-missing'", 0)
}

func assertRowCount(t *testing.T, s *Store, query string, want int) {
	t.Helper()
	var got int
	if err := s.DB.QueryRowContext(context.Background(), query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("query %q count = %d, want %d", query, got, want)
	}
}
