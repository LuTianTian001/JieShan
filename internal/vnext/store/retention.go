package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// RetentionCleanupResult describes physical history removed by one retention
// pass. Ledger-backed request rows are counted separately because they remain
// as compact accounting audit anchors after their attempt detail expires.
type RetentionCleanupResult struct {
	CutoffAt                    time.Time
	RequestAttemptsDeleted      int64
	RequestLogsDeleted          int64
	LedgerProtectedRequestLogs  int64
	HourlyUsageBucketsDeleted   int64
	ModelProbeResultsDeleted    int64
	ModelProbeRunsDeleted       int64
	SiteUsageRecordsDeleted     int64
	SiteBalanceSnapshotsDeleted int64
}

// PruneOperationalHistory deletes expired operational detail without deleting
// quota-ledger audit state or work that is still running. Request logs without
// ledger entries can be removed completely. A ledger-backed request log must
// remain because quota_ledger intentionally references it with ON DELETE
// RESTRICT, but its high-volume attempt detail is safe to expire.
func (s *Store) PruneOperationalHistory(ctx context.Context, cutoff time.Time) (RetentionCleanupResult, error) {
	if s == nil || s.DB == nil {
		return RetentionCleanupResult{}, errors.New("store is unavailable")
	}
	if cutoff.IsZero() {
		return RetentionCleanupResult{}, errors.New("retention cutoff is required")
	}
	cutoff = cutoff.UTC()
	cutoffMS := cutoff.UnixMilli()

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return RetentionCleanupResult{}, err
	}
	defer tx.Rollback()

	result := RetentionCleanupResult{CutoffAt: cutoff}
	if result.RequestAttemptsDeleted, err = deleteRows(ctx, tx, `DELETE FROM request_attempts
WHERE EXISTS (
  SELECT 1 FROM request_logs request
  WHERE request.id=request_attempts.request_id
    AND request.status<>'running'
    AND request.finished_at IS NOT NULL
    AND request.finished_at<?
)`, cutoffMS); err != nil {
		return RetentionCleanupResult{}, err
	}
	if result.RequestLogsDeleted, err = deleteRows(ctx, tx, `DELETE FROM request_logs
WHERE status<>'running'
  AND finished_at IS NOT NULL
  AND finished_at<?
  AND NOT EXISTS (
    SELECT 1 FROM quota_ledger ledger WHERE ledger.request_id=request_logs.id
  )`, cutoffMS); err != nil {
		return RetentionCleanupResult{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_logs request
WHERE request.status<>'running'
  AND request.finished_at IS NOT NULL
  AND request.finished_at<?
  AND EXISTS (
    SELECT 1 FROM quota_ledger ledger WHERE ledger.request_id=request.id
  )`, cutoffMS).Scan(&result.LedgerProtectedRequestLogs); err != nil {
		return RetentionCleanupResult{}, err
	}
	hourlyCutoffMS, err := hourlyWindowStart(cutoffMS)
	if err != nil {
		return RetentionCleanupResult{}, err
	}
	if result.HourlyUsageBucketsDeleted, err = deleteRows(ctx, tx, `DELETE FROM downstream_key_hourly_usage
WHERE window_started_at<? AND reserved_nano_usd=0`, hourlyCutoffMS); err != nil {
		return RetentionCleanupResult{}, err
	}

	if result.ModelProbeResultsDeleted, err = deleteRows(ctx, tx, `DELETE FROM model_probe_results
WHERE EXISTS (
  SELECT 1 FROM model_probe_runs run
  WHERE run.id=model_probe_results.run_id
    AND run.status<>'running'
    AND run.finished_at IS NOT NULL
    AND run.finished_at<?
)`, cutoffMS); err != nil {
		return RetentionCleanupResult{}, err
	}
	if result.ModelProbeRunsDeleted, err = deleteRows(ctx, tx, `DELETE FROM model_probe_runs
WHERE status<>'running'
  AND finished_at IS NOT NULL
  AND finished_at<?`, cutoffMS); err != nil {
		return RetentionCleanupResult{}, err
	}

	if result.SiteUsageRecordsDeleted, err = deleteRows(ctx, tx,
		`DELETE FROM site_usage_records WHERE occurred_at<?`, cutoffMS); err != nil {
		return RetentionCleanupResult{}, err
	}
	if result.SiteBalanceSnapshotsDeleted, err = deleteRows(ctx, tx, `DELETE FROM site_balance_snapshots
WHERE captured_at<?
  AND id<>(
    SELECT latest.id FROM site_balance_snapshots latest
    WHERE latest.site_account_connection_id=site_balance_snapshots.site_account_connection_id
    ORDER BY latest.captured_at DESC,latest.id DESC
    LIMIT 1
  )`, cutoffMS); err != nil {
		return RetentionCleanupResult{}, err
	}

	if err := tx.Commit(); err != nil {
		return RetentionCleanupResult{}, err
	}
	return result, nil
}

func deleteRows(ctx context.Context, tx *sql.Tx, query string, args ...any) (int64, error) {
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
