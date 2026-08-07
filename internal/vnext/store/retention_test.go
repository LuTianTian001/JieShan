package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestPruneOperationalHistoryRemovesOnlyExpiredNonAuditData(t *testing.T) {
	storage := newTestStore(t)
	ctx := context.Background()
	fixture := createAccountingFixture(t, storage, nil)
	cutoff := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	oldBilledStart := cutoff.Add(-2 * time.Hour)
	oldBilledFinish := cutoff.Add(-time.Hour)
	persistRetentionRequest(t, storage, fixture, "old-billed", oldBilledStart, &oldBilledFinish, 100, 70)

	oldFreeStart := cutoff.Add(-90 * time.Minute)
	oldFreeFinish := cutoff.Add(-30 * time.Minute)
	persistRetentionRequest(t, storage, fixture, "old-free", oldFreeStart, &oldFreeFinish, 0, 0)

	boundaryStart := cutoff.Add(-time.Second)
	persistRetentionRequest(t, storage, fixture, "boundary-billed", boundaryStart, &cutoff, 50, 30)

	runningStart := cutoff.Add(-48 * time.Hour)
	persistRetentionRequest(t, storage, fixture, "old-running", runningStart, nil, 25, 0)

	insertRetentionProbe(t, storage, fixture, "probe-old", "completed", cutoff.Add(-2*time.Hour), cutoff.Add(-time.Hour))
	insertRetentionProbe(t, storage, fixture, "probe-boundary", "completed", cutoff.Add(-time.Minute), cutoff)
	insertRetentionProbe(t, storage, fixture, "probe-running", "running", cutoff.Add(-3*time.Hour), time.Time{})

	accountSiteID, accountConnectionID := createRetentionAccount(t, storage, "Retention account")
	saveRetentionBalance(t, storage, accountSiteID, cutoff.Add(-3*time.Hour), "15.00")
	saveRetentionBalance(t, storage, accountSiteID, cutoff.Add(-2*time.Hour), "14.00")
	boundaryBalance := saveRetentionBalance(t, storage, accountSiteID, cutoff, "13.00")
	oldOnlySiteID, _ := createRetentionAccount(t, storage, "Old-only retention account")
	oldOnlyBalance := saveRetentionBalance(t, storage, oldOnlySiteID, cutoff.Add(-24*time.Hour), "7.00")
	saveRetentionUsage(t, storage, accountSiteID, cutoff, []SiteUsageRecordWrite{
		{DedupKey: "usage-old", OccurredAt: cutoff.Add(-time.Millisecond).UnixMilli(), Model: "old-model"},
		{DedupKey: "usage-boundary", OccurredAt: cutoff.UnixMilli(), Model: "boundary-model"},
	})

	result, err := storage.PruneOperationalHistory(ctx, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if !result.CutoffAt.Equal(cutoff) || result.RequestAttemptsDeleted != 2 || result.RequestLogsDeleted != 1 ||
		result.LedgerProtectedRequestLogs != 1 || result.ModelProbeResultsDeleted != 1 ||
		result.ModelProbeRunsDeleted != 1 || result.SiteUsageRecordsDeleted != 1 ||
		result.SiteBalanceSnapshotsDeleted != 2 {
		t.Fatalf("cleanup result = %+v", result)
	}

	if _, err := storage.GetRequestLog(ctx, "old-free"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired non-ledger request lookup = %v", err)
	}
	assertRetentionRequest(t, storage, "old-billed", 0, 2)
	assertRetentionRequest(t, storage, "boundary-billed", 1, 2)
	assertRetentionRequest(t, storage, "old-running", 1, 1)

	if _, err := storage.GetModelProbeRun(ctx, "probe-old"); !errors.Is(err, ErrModelProbeRunNotFound) {
		t.Fatalf("expired probe run lookup = %v", err)
	}
	assertRetentionProbeExists(t, storage, "probe-boundary", 1)
	assertRetentionProbeExists(t, storage, "probe-running", 1)

	usage, err := storage.ListSiteUsageRecords(ctx, accountSiteID, SiteUsageListFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(usage.Records) != 1 || usage.Records[0].DedupKey != "usage-boundary" {
		t.Fatalf("retained source usage = %+v", usage.Records)
	}
	latest, err := storage.GetLatestSiteBalance(ctx, accountSiteID)
	if err != nil || latest.ID != boundaryBalance.ID {
		t.Fatalf("latest retained balance = %+v, %v", latest, err)
	}
	oldOnlyLatest, err := storage.GetLatestSiteBalance(ctx, oldOnlySiteID)
	if err != nil || oldOnlyLatest.ID != oldOnlyBalance.ID {
		t.Fatalf("old-only latest balance = %+v, %v", oldOnlyLatest, err)
	}
	var accountBalanceCount int
	if err := storage.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_balance_snapshots
WHERE site_account_connection_id=?`, accountConnectionID).Scan(&accountBalanceCount); err != nil {
		t.Fatal(err)
	}
	if accountBalanceCount != 1 {
		t.Fatalf("retained account balances = %d, want 1", accountBalanceCount)
	}
	assertNoForeignKeyViolations(t, storage)
}

func TestPruneOperationalHistoryRollsBackTheWholePass(t *testing.T) {
	ctx := context.Background()
	var unavailable *Store
	if _, err := unavailable.PruneOperationalHistory(ctx, time.Now()); err == nil {
		t.Fatal("nil store unexpectedly accepted retention cleanup")
	}

	storage := newTestStore(t)
	if _, err := storage.PruneOperationalHistory(ctx, time.Time{}); err == nil {
		t.Fatal("zero cutoff unexpectedly accepted")
	}
	fixture := createAccountingFixture(t, storage, nil)
	cutoff := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	startedAt := cutoff.Add(-2 * time.Hour)
	finishedAt := cutoff.Add(-time.Hour)
	persistRetentionRequest(t, storage, fixture, "rollback-free", startedAt, &finishedAt, 0, 0)
	siteID, _ := createRetentionAccount(t, storage, "Rollback account")
	saveRetentionUsage(t, storage, siteID, cutoff, []SiteUsageRecordWrite{
		{DedupKey: "rollback-usage", OccurredAt: cutoff.Add(-time.Hour).UnixMilli(), Model: "rollback-model"},
	})

	if _, err := storage.DB.ExecContext(ctx, `CREATE TRIGGER fail_retention_usage
BEFORE DELETE ON site_usage_records
BEGIN SELECT RAISE(ABORT,'forced retention failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.PruneOperationalHistory(ctx, cutoff); err == nil {
		t.Fatal("retention cleanup unexpectedly survived a delete failure")
	}
	assertRetentionRequest(t, storage, "rollback-free", 1, 0)
	assertTableCount(t, storage, "site_usage_records", 1)

	if _, err := storage.DB.ExecContext(ctx, `DROP TRIGGER fail_retention_usage`); err != nil {
		t.Fatal(err)
	}
	result, err := storage.PruneOperationalHistory(ctx, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestAttemptsDeleted != 1 || result.RequestLogsDeleted != 1 || result.SiteUsageRecordsDeleted != 1 {
		t.Fatalf("cleanup after trigger removal = %+v", result)
	}
	assertTableCount(t, storage, "request_logs", 0)
	assertTableCount(t, storage, "request_attempts", 0)
	assertTableCount(t, storage, "site_usage_records", 0)
	assertNoForeignKeyViolations(t, storage)
}

func persistRetentionRequest(
	t *testing.T,
	storage *Store,
	fixture accountingFixture,
	id string,
	startedAt time.Time,
	finishedAt *time.Time,
	reservation, cost int64,
) {
	t.Helper()
	ctx := context.Background()
	start := accountingRequestStart(fixture, id, reservation)
	start.StartedAt = startedAt.UnixMilli()
	if _, err := storage.StartRequestWithQuotaReservation(ctx, start); err != nil {
		t.Fatal(err)
	}
	attempt := accountingAttempt(fixture, id, 0)
	attempt.StartedAt = startedAt.UnixMilli()
	attempt.FinishedAt = startedAt.Add(100 * time.Millisecond).UnixMilli()
	if err := storage.RecordRequestAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if finishedAt == nil {
		return
	}
	settlement := accountingSettlement(0, cost)
	settlement.FinishedAt = finishedAt.UnixMilli()
	settlement.DurationMS = settlement.FinishedAt - start.StartedAt
	if _, err := storage.SettleRequest(ctx, id, settlement); err != nil {
		t.Fatal(err)
	}
}

func insertRetentionProbe(
	t *testing.T,
	storage *Store,
	fixture accountingFixture,
	id, status string,
	startedAt, finishedAt time.Time,
) {
	t.Helper()
	ctx := context.Background()
	var finished any
	successCount := 0
	if status != "running" {
		finished = finishedAt.UnixMilli()
		successCount = 1
	}
	if _, err := storage.DB.ExecContext(ctx, `INSERT INTO model_probe_runs(
id,published_model_id,published_model_revision,public_model_snapshot,trigger_kind,status,target_count,
success_count,failure_count,skipped_count,started_at,finished_at)
VALUES (?,?,?,'public-model','manual',?,1,?,0,0,?,?)`, id, fixture.publishedModelID,
		fixture.publishedModelRevision, status, successCount, startedAt.UnixMilli(), finished); err != nil {
		t.Fatal(err)
	}
	resultFinishedAt := finishedAt
	if resultFinishedAt.IsZero() {
		resultFinishedAt = startedAt.Add(100 * time.Millisecond)
	}
	if _, err := storage.DB.ExecContext(ctx, `INSERT INTO model_probe_results(
run_id,published_model_id,published_model_target_id,published_model_target_revision,
provider_model_target_id,provider_model_target_revision,target_position,site_id,endpoint_id,
site_name_snapshot,endpoint_name_snapshot,source_model_snapshot,wire_protocol,api_surface,
outcome,permit_mode,permit_reason,http_status,failure_kind,error_code,latency_ms,first_output_ms,
started_at,finished_at,health_applied,health_apply_reason,health_error_code)
VALUES (?,?,?,?,?,?,0,?,?, 'Accounting upstream','Accounting endpoint','accounting-model',
'openai','openai.chat_completions','success','normal','granted',200,NULL,NULL,100,20,?,?,1,'accepted',NULL)`,
		id, fixture.publishedModelID, fixture.publishedModelTargetID, fixture.publishedTargetRevision,
		fixture.providerTargetID, fixture.providerRevision, fixture.siteID, fixture.endpointID,
		startedAt.UnixMilli(), resultFinishedAt.UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

func createRetentionAccount(t *testing.T, storage *Store, name string) (int64, int64) {
	t.Helper()
	siteID := mustCreateSite(t, storage, name)
	connection, err := storage.CreateSealedSiteAccountConnection(
		context.Background(), siteID,
		SealedSiteAccountConnectionInput{AdapterKind: "ciii", Origin: "https://retention.example", CipherVersion: 1, Enabled: true},
		func(int64, int64) ([]byte, error) { return []byte("sealed"), nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	return siteID, connection.ID
}

func saveRetentionBalance(t *testing.T, storage *Store, siteID int64, capturedAt time.Time, value string) SiteBalanceSnapshot {
	t.Helper()
	item, err := storage.SaveSiteBalanceSnapshot(context.Background(), siteID, "ciii", SiteBalanceSnapshotWrite{
		AvailableValue: value, AvailableUnit: "USD", CapturedAt: capturedAt.UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func saveRetentionUsage(t *testing.T, storage *Store, siteID int64, fetchedAt time.Time, records []SiteUsageRecordWrite) {
	t.Helper()
	if _, err := storage.SaveSiteUsageRecords(context.Background(), siteID, "ciii", records, fetchedAt.UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

func assertRetentionRequest(t *testing.T, storage *Store, requestID string, wantAttempts, wantLedger int) {
	t.Helper()
	ctx := context.Background()
	if _, err := storage.GetRequestLog(ctx, requestID); err != nil {
		t.Fatalf("request %q lookup: %v", requestID, err)
	}
	attempts, err := storage.ListRequestAttempts(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := storage.ListQuotaLedger(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != wantAttempts || len(ledger) != wantLedger {
		t.Fatalf("request %q retained attempts/ledger = %d/%d, want %d/%d",
			requestID, len(attempts), len(ledger), wantAttempts, wantLedger)
	}
}

func assertRetentionProbeExists(t *testing.T, storage *Store, runID string, wantResults int) {
	t.Helper()
	if _, err := storage.GetModelProbeRun(context.Background(), runID); err != nil {
		t.Fatalf("probe run %q lookup: %v", runID, err)
	}
	var count int
	if err := storage.DB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM model_probe_results WHERE run_id=?`, runID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != wantResults {
		t.Fatalf("probe run %q result count = %d, want %d", runID, count, wantResults)
	}
}

func assertNoForeignKeyViolations(t *testing.T, storage *Store) {
	t.Helper()
	rows, err := storage.DB.QueryContext(context.Background(), `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowID int64
		var parent string
		var constraint int
		if err := rows.Scan(&table, &rowID, &parent, &constraint); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("foreign key violation: table=%s row=%d parent=%s constraint=%d", table, rowID, parent, constraint)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
