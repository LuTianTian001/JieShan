package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/LuTianTian001/JieShan/internal/health"
	"github.com/LuTianTian001/JieShan/internal/redact"
)

func (s *Store) AcquireTargetPermit(ctx context.Context, targetID, nowMS int64, lease time.Duration) (bool, error) {
	return s.acquireTargetPermit(ctx, targetID, nowMS, lease, false)
}

func (s *Store) AcquireProbePermit(ctx context.Context, targetID, nowMS int64, lease time.Duration) (bool, error) {
	return s.acquireTargetPermit(ctx, targetID, nowMS, lease, true)
}

func (s *Store) acquireTargetPermit(ctx context.Context, targetID, nowMS int64, lease time.Duration, ignoreCapability bool) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO target_health(target_id,updated_at) VALUES (?,?) ON CONFLICT(target_id) DO NOTHING`, targetID, nowMS); err != nil {
		return false, err
	}
	var phase, capability string
	var cooldown, leased sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT circuit_phase,capability_state,cooldown_until,half_open_lease_until FROM target_health WHERE target_id=?`, targetID).
		Scan(&phase, &capability, &cooldown, &leased); err != nil {
		return false, err
	}
	if capability == "unsupported" && !ignoreCapability {
		return false, tx.Commit()
	}
	if phase == "closed" {
		return true, tx.Commit()
	}
	if phase == "open" && cooldown.Valid && cooldown.Int64 > nowMS {
		return false, tx.Commit()
	}
	if leased.Valid && leased.Int64 > nowMS {
		return false, tx.Commit()
	}
	result, err := tx.ExecContext(ctx, `UPDATE target_health SET circuit_phase='half_open',half_open_lease_until=?,updated_at=?
WHERE target_id=? AND (half_open_lease_until IS NULL OR half_open_lease_until<=?)`, nowMS+lease.Milliseconds(), nowMS, targetID, nowMS)
	if err != nil {
		return false, err
	}
	changed, _ := result.RowsAffected()
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return changed == 1, nil
}

func (s *Store) RecordTargetSuccess(ctx context.Context, target RouteTarget, nowMS int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO target_health(target_id,circuit_phase,consecutive_failures,last_success_at,capability_state,updated_at)
VALUES (?,'closed',0,?,'supported',?)
ON CONFLICT(target_id) DO UPDATE SET circuit_phase='closed',consecutive_failures=0,last_success_at=excluded.last_success_at,
cooldown_until=NULL,half_open_lease_until=NULL,last_error_class=NULL,last_error_message=NULL,capability_state='supported',updated_at=excluded.updated_at`,
		target.ID, nowMS, nowMS); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE upstream_credentials SET runtime_state='active',updated_at=? WHERE id=?", nowMS, target.CredentialID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RecordTargetFailure(ctx context.Context, target RouteTarget, decision health.Decision, incidentID, message string, nowMS int64, retryAfter time.Duration) error {
	message = redact.String(message)
	return retrySQLiteBusy(ctx, func() error {
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if decision.InvalidateCredential {
			if _, err := tx.ExecContext(ctx, "UPDATE upstream_credentials SET runtime_state='invalid',updated_at=? WHERE id=?", nowMS, target.CredentialID); err != nil {
				return err
			}
		}
		if decision.UnsupportedModel {
			_, err = tx.ExecContext(ctx, `INSERT INTO target_health(target_id,capability_state,last_error_class,last_error_message,last_incident_id,updated_at)
VALUES (?,'unsupported',?,?,?,?)
ON CONFLICT(target_id) DO UPDATE SET capability_state='unsupported',last_error_class=excluded.last_error_class,
last_error_message=excluded.last_error_message,last_incident_id=excluded.last_incident_id,updated_at=excluded.updated_at`,
				target.ID, string(decision.Class), message, incidentID, nowMS)
			if err != nil {
				return err
			}
			return tx.Commit()
		}
		if !decision.PenalizeTarget {
			_, err = tx.ExecContext(ctx, `INSERT INTO target_health(target_id,last_error_class,last_error_message,last_incident_id,updated_at)
VALUES (?,?,?,?,?)
ON CONFLICT(target_id) DO UPDATE SET last_error_class=excluded.last_error_class,last_error_message=excluded.last_error_message,
last_incident_id=excluded.last_incident_id,updated_at=excluded.updated_at`,
				target.ID, string(decision.Class), message, incidentID, nowMS)
			if err != nil {
				return err
			}
			return tx.Commit()
		}

		threshold := targetRouteFailureThreshold(target)
		windowMS := int64(time.Duration(targetRouteFailureWindow(target)) * time.Second / time.Millisecond)
		cooldown := time.Duration(targetRouteCooldown(target)) * time.Second
		if retryAfter > cooldown {
			cooldown = retryAfter
		}
		if cooldown < time.Second {
			cooldown = time.Second
		}
		immediate := retryAfter > 0
		initialOpen := immediate || threshold <= 1
		initialPhase := "closed"
		initialFailures := 1
		var initialCooldown any
		if initialOpen {
			initialPhase = "open"
			initialFailures = 0
			initialCooldown = nowMS + cooldown.Milliseconds()
		}
		immediateInt := boolInt(immediate)
		cooldownUntil := nowMS + cooldown.Milliseconds()
		_, err = tx.ExecContext(ctx, `INSERT INTO target_health(
target_id,circuit_phase,consecutive_failures,last_failure_at,cooldown_until,half_open_lease_until,
last_error_class,last_error_message,last_incident_id,updated_at)
VALUES (?,?,?,?,?,NULL,?,?,?,?)
ON CONFLICT(target_id) DO UPDATE SET
circuit_phase=CASE WHEN target_health.circuit_phase='half_open' OR ?=1 OR
  (CASE WHEN target_health.last_failure_at IS NOT NULL AND ?>=target_health.last_failure_at AND ?-target_health.last_failure_at<=?
    THEN target_health.consecutive_failures+1 ELSE 1 END)>=?
  THEN 'open' ELSE 'closed' END,
consecutive_failures=CASE WHEN target_health.circuit_phase='half_open' OR ?=1 OR
  (CASE WHEN target_health.last_failure_at IS NOT NULL AND ?>=target_health.last_failure_at AND ?-target_health.last_failure_at<=?
    THEN target_health.consecutive_failures+1 ELSE 1 END)>=?
  THEN 0 ELSE
  (CASE WHEN target_health.last_failure_at IS NOT NULL AND ?>=target_health.last_failure_at AND ?-target_health.last_failure_at<=?
    THEN target_health.consecutive_failures+1 ELSE 1 END) END,
last_failure_at=excluded.last_failure_at,
cooldown_until=CASE WHEN target_health.circuit_phase='half_open' OR ?=1 OR
  (CASE WHEN target_health.last_failure_at IS NOT NULL AND ?>=target_health.last_failure_at AND ?-target_health.last_failure_at<=?
    THEN target_health.consecutive_failures+1 ELSE 1 END)>=?
  THEN ? ELSE NULL END,
half_open_lease_until=NULL,last_error_class=excluded.last_error_class,last_error_message=excluded.last_error_message,
last_incident_id=excluded.last_incident_id,updated_at=excluded.updated_at
WHERE COALESCE(target_health.last_incident_id,'')<>excluded.last_incident_id`,
			target.ID, initialPhase, initialFailures, nowMS, initialCooldown, string(decision.Class), message, incidentID, nowMS,
			immediateInt, nowMS, nowMS, windowMS, threshold,
			immediateInt, nowMS, nowMS, windowMS, threshold, nowMS, nowMS, windowMS,
			immediateInt, nowMS, nowMS, windowMS, threshold, cooldownUntil)
		if err != nil {
			return err
		}
		return tx.Commit()
	})
}

func retrySQLiteBusy(ctx context.Context, operation func() error) error {
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		err = operation()
		if err == nil || !isSQLiteBusy(err) {
			return err
		}
		delay := time.Duration(1<<attempt) * 10 * time.Millisecond
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(err, ctx.Err())
		case <-timer.C:
		}
	}
	return err
}

func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "sqlite_busy") || strings.Contains(message, "database is locked") || strings.Contains(message, "database table is locked")
}

// Route-specific health parameters are copied onto transient targets by the gateway.
// Negative sentinel values are never emitted; defaults protect imported rows.
func targetRouteCooldown(target RouteTarget) int {
	if target.CooldownSeconds <= 0 {
		return 300
	}
	return target.CooldownSeconds
}

func targetRouteFailureThreshold(target RouteTarget) int {
	if target.FailureThreshold < 2 {
		return 2
	}
	return target.FailureThreshold
}

func targetRouteFailureWindow(target RouteTarget) int {
	if target.FailureWindowSeconds <= 0 {
		return 300
	}
	return target.FailureWindowSeconds
}

func nullableInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func (s *Store) InsertProbeResult(ctx context.Context, routeID, targetID int64, status string, latencyMS *int64, class, message string, checkedAt int64) error {
	message = redact.String(message)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO probe_results(route_id,target_id,status,latency_ms,error_class,error_message,checked_at)
	VALUES (?,?,?,?,?,?,?)`, routeID, targetID, status, latencyMS, nullableString(class), nullableString(message), checkedAt); err != nil {
		return err
	}
	_, _ = tx.ExecContext(ctx, `DELETE FROM probe_results WHERE target_id=? AND id NOT IN (
SELECT id FROM probe_results WHERE target_id=? ORDER BY checked_at DESC,id DESC LIMIT 500)`, targetID, targetID)
	return tx.Commit()
}
