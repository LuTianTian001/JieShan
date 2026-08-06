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

const (
	RouteSiteTargetHealthSuccess = "success"
	RouteSiteTargetHealthFailure = "failure"
)

func (s *Store) AcquireRouteSiteTargetPermit(ctx context.Context, targetID, nowMS int64, lease time.Duration, probe bool) (bool, error) {
	if targetID <= 0 {
		return false, errors.New("route site target ID must be positive")
	}
	if lease < time.Second {
		lease = time.Second
	}
	var permitted bool
	err := retrySQLiteBusy(ctx, func() error {
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, `INSERT INTO route_site_target_health(target_id,updated_at)
SELECT id,? FROM route_site_targets WHERE id=? ON CONFLICT(target_id) DO NOTHING`, nowMS, targetID); err != nil {
			return err
		}
		var phase, capability string
		var cooldown, leased sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT circuit_phase,capability_state,cooldown_until,half_open_lease_until
FROM route_site_target_health WHERE target_id=?`, targetID).Scan(&phase, &capability, &cooldown, &leased); err != nil {
			return err
		}
		if capability == "unsupported" && !probe {
			return tx.Commit()
		}
		if phase == "closed" {
			permitted = true
			return tx.Commit()
		}
		if phase == "open" && cooldown.Valid && cooldown.Int64 > nowMS {
			return tx.Commit()
		}
		if leased.Valid && leased.Int64 > nowMS {
			return tx.Commit()
		}
		result, err := tx.ExecContext(ctx, `UPDATE route_site_target_health
SET circuit_phase='half_open',half_open_lease_until=?,updated_at=?
WHERE target_id=? AND circuit_phase!='closed' AND (half_open_lease_until IS NULL OR half_open_lease_until<=?)`,
			nowMS+lease.Milliseconds(), nowMS, targetID, nowMS)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		permitted = changed == 1
		return tx.Commit()
	})
	return permitted, err
}

func (s *Store) RecordRouteSiteTargetSuccess(ctx context.Context, targetID, nowMS int64) error {
	return s.applyRouteSiteTargetHealthEvent(ctx, targetID, RouteSiteTargetHealthEvent{
		Kind: RouteSiteTargetHealthSuccess, OccurredAt: nowMS,
	})
}

func (s *Store) RecordRouteSiteTargetFailure(ctx context.Context, targetID int64, decision health.Decision, incidentID, message string, nowMS int64, retryAfter time.Duration) error {
	return s.applyRouteSiteTargetHealthEvent(ctx, targetID, RouteSiteTargetHealthEvent{
		Kind: RouteSiteTargetHealthFailure, Decision: decision, IncidentID: strings.TrimSpace(incidentID),
		ErrorMessage: redact.String(message), OccurredAt: nowMS, RetryAfter: retryAfter,
	})
}

func (s *Store) applyRouteSiteTargetHealthEvent(ctx context.Context, targetID int64, event RouteSiteTargetHealthEvent) error {
	if targetID <= 0 {
		return errors.New("route site target ID must be positive")
	}
	if event.OccurredAt <= 0 {
		event.OccurredAt = NowMS()
	}
	return retrySQLiteBusy(ctx, func() error {
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		current, policy, err := getRouteSiteTargetHealthPolicyTx(ctx, tx, targetID)
		if err != nil {
			return err
		}
		next, changed, err := ReduceRouteSiteTargetHealth(current, policy, event)
		if err != nil {
			return err
		}
		if !changed {
			return tx.Commit()
		}
		if err := upsertRouteSiteTargetHealthTx(ctx, tx, next); err != nil {
			return err
		}
		return tx.Commit()
	})
}

func ReduceRouteSiteTargetHealth(current RouteSiteTargetHealth, policy PublishedModelHealthPolicy, event RouteSiteTargetHealthEvent) (RouteSiteTargetHealth, bool, error) {
	if current.TargetID <= 0 {
		return current, false, errors.New("route site target ID must be positive")
	}
	if event.OccurredAt <= 0 {
		return current, false, errors.New("health event time must be positive")
	}
	if current.CircuitPhase == "" {
		current.CircuitPhase = "closed"
	}
	if current.CapabilityState == "" {
		current.CapabilityState = "unknown"
	}
	if event.Kind == RouteSiteTargetHealthFailure && event.IncidentID != "" && event.IncidentID == current.LastIncidentID {
		return current, false, nil
	}
	next := current
	next.UpdatedAt = event.OccurredAt
	switch event.Kind {
	case RouteSiteTargetHealthSuccess:
		next.CircuitPhase = "closed"
		next.ConsecutiveFailures = 0
		next.LastSuccessAt = copyInt64(event.OccurredAt)
		next.CooldownUntil = nil
		next.HalfOpenLeaseUntil = nil
		next.CapabilityState = "supported"
		next.LastErrorClass = ""
		next.LastErrorMessage = ""
		return next, true, nil
	case RouteSiteTargetHealthFailure:
	default:
		return current, false, errors.New("unknown route site target health event")
	}

	next.LastErrorClass = string(event.Decision.Class)
	next.LastErrorMessage = redact.String(event.ErrorMessage)
	next.LastIncidentID = event.IncidentID
	if event.Decision.UnsupportedModel {
		next.CapabilityState = "unsupported"
		return next, true, nil
	}
	if !event.Decision.PenalizeTarget {
		return next, true, nil
	}

	policy = normalizePublishedModelHealthPolicy(policy)
	withinWindow := current.LastFailureAt != nil && event.OccurredAt >= *current.LastFailureAt &&
		event.OccurredAt-*current.LastFailureAt <= int64(policy.FailureWindowSeconds)*1000
	failures := 1
	if withinWindow {
		failures = current.ConsecutiveFailures + 1
	}
	next.LastFailureAt = copyInt64(event.OccurredAt)
	retryAfter := event.RetryAfter
	if event.Decision.RetryAfter > retryAfter {
		retryAfter = event.Decision.RetryAfter
	}
	open := current.CircuitPhase == "open" || current.CircuitPhase == "half_open" || retryAfter > 0 || failures >= policy.FailureThreshold
	if !open {
		next.CircuitPhase = "closed"
		next.ConsecutiveFailures = failures
		next.CooldownUntil = nil
		next.HalfOpenLeaseUntil = nil
		return next, true, nil
	}
	cooldown := time.Duration(policy.CooldownSeconds) * time.Second
	if retryAfter > cooldown {
		cooldown = retryAfter
	}
	if cooldown < time.Second {
		cooldown = time.Second
	}
	cooldownUntil := event.OccurredAt + cooldown.Milliseconds()
	if current.CooldownUntil != nil && *current.CooldownUntil > cooldownUntil {
		cooldownUntil = *current.CooldownUntil
	}
	next.CircuitPhase = "open"
	next.ConsecutiveFailures = 0
	next.CooldownUntil = copyInt64(cooldownUntil)
	next.HalfOpenLeaseUntil = nil
	return next, true, nil
}

func (s *Store) GetRouteSiteTargetHealth(ctx context.Context, targetID int64) (RouteSiteTargetHealth, error) {
	return scanRouteSiteTargetHealth(s.DB.QueryRowContext(ctx, routeSiteTargetHealthSelect+` WHERE t.id=?`, targetID))
}

func (s *Store) ListRouteSiteTargetHealth(ctx context.Context, publishedModelID int64) ([]RouteSiteTargetHealth, error) {
	rows, err := s.DB.QueryContext(ctx, routeSiteTargetHealthSelect+` WHERE t.published_model_id=? ORDER BY t.position,t.id`, publishedModelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]RouteSiteTargetHealth, 0)
	for rows.Next() {
		item, err := scanRouteSiteTargetHealth(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func getRouteSiteTargetHealthPolicyTx(ctx context.Context, tx *sql.Tx, targetID int64) (RouteSiteTargetHealth, PublishedModelHealthPolicy, error) {
	var current RouteSiteTargetHealth
	var policy PublishedModelHealthPolicy
	var phase, capability, errorClass, errorMessage, incident sql.NullString
	var failures sql.NullInt64
	var lastFailure, lastSuccess, cooldown, lease, updated sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT t.id,p.failure_threshold,p.failure_window_seconds,p.cooldown_seconds,
h.circuit_phase,h.consecutive_failures,h.last_failure_at,h.last_success_at,h.cooldown_until,h.half_open_lease_until,
h.capability_state,h.last_error_class,h.last_error_message,h.last_incident_id,h.updated_at
FROM route_site_targets t JOIN published_models p ON p.id=t.published_model_id
LEFT JOIN route_site_target_health h ON h.target_id=t.id WHERE t.id=?`, targetID).Scan(
		&current.TargetID, &policy.FailureThreshold, &policy.FailureWindowSeconds, &policy.CooldownSeconds,
		&phase, &failures, &lastFailure, &lastSuccess, &cooldown, &lease, &capability,
		&errorClass, &errorMessage, &incident, &updated,
	)
	if err != nil {
		return RouteSiteTargetHealth{}, PublishedModelHealthPolicy{}, err
	}
	applyRouteSiteTargetHealthNulls(&current, phase, failures, lastFailure, lastSuccess, cooldown, lease, capability, errorClass, errorMessage, incident, updated)
	return current, policy, nil
}

func upsertRouteSiteTargetHealthTx(ctx context.Context, tx *sql.Tx, item RouteSiteTargetHealth) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO route_site_target_health(
target_id,circuit_phase,consecutive_failures,last_failure_at,last_success_at,cooldown_until,half_open_lease_until,
capability_state,last_error_class,last_error_message,last_incident_id,updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(target_id) DO UPDATE SET circuit_phase=excluded.circuit_phase,consecutive_failures=excluded.consecutive_failures,
last_failure_at=excluded.last_failure_at,last_success_at=excluded.last_success_at,cooldown_until=excluded.cooldown_until,
half_open_lease_until=excluded.half_open_lease_until,capability_state=excluded.capability_state,
last_error_class=excluded.last_error_class,last_error_message=excluded.last_error_message,
last_incident_id=excluded.last_incident_id,updated_at=excluded.updated_at`,
		item.TargetID, item.CircuitPhase, item.ConsecutiveFailures, item.LastFailureAt, item.LastSuccessAt,
		item.CooldownUntil, item.HalfOpenLeaseUntil, item.CapabilityState, nullableString(item.LastErrorClass),
		nullableString(item.LastErrorMessage), nullableString(item.LastIncidentID), item.UpdatedAt)
	return err
}

const routeSiteTargetHealthSelect = `SELECT t.id,h.circuit_phase,h.consecutive_failures,h.last_failure_at,h.last_success_at,
h.cooldown_until,h.half_open_lease_until,h.capability_state,h.last_error_class,h.last_error_message,h.last_incident_id,h.updated_at
FROM route_site_targets t LEFT JOIN route_site_target_health h ON h.target_id=t.id`

func scanRouteSiteTargetHealth(row scanner) (RouteSiteTargetHealth, error) {
	var item RouteSiteTargetHealth
	var phase, capability, errorClass, errorMessage, incident sql.NullString
	var failures, lastFailure, lastSuccess, cooldown, lease, updated sql.NullInt64
	err := row.Scan(&item.TargetID, &phase, &failures, &lastFailure, &lastSuccess, &cooldown, &lease,
		&capability, &errorClass, &errorMessage, &incident, &updated)
	if err != nil {
		return RouteSiteTargetHealth{}, err
	}
	applyRouteSiteTargetHealthNulls(&item, phase, failures, lastFailure, lastSuccess, cooldown, lease, capability, errorClass, errorMessage, incident, updated)
	return item, nil
}

func applyRouteSiteTargetHealthNulls(item *RouteSiteTargetHealth, phase sql.NullString, failures, lastFailure, lastSuccess, cooldown, lease sql.NullInt64, capability, errorClass, errorMessage, incident sql.NullString, updated sql.NullInt64) {
	item.CircuitPhase = "closed"
	if phase.Valid {
		item.CircuitPhase = phase.String
	}
	item.ConsecutiveFailures = int(failures.Int64)
	item.LastFailureAt = int64Ptr(lastFailure)
	item.LastSuccessAt = int64Ptr(lastSuccess)
	item.CooldownUntil = int64Ptr(cooldown)
	item.HalfOpenLeaseUntil = int64Ptr(lease)
	item.CapabilityState = "unknown"
	if capability.Valid {
		item.CapabilityState = capability.String
	}
	item.LastErrorClass = errorClass.String
	item.LastErrorMessage = errorMessage.String
	item.LastIncidentID = incident.String
	item.UpdatedAt = updated.Int64
}

func normalizePublishedModelHealthPolicy(policy PublishedModelHealthPolicy) PublishedModelHealthPolicy {
	if policy.FailureThreshold < 2 {
		policy.FailureThreshold = 2
	}
	if policy.FailureWindowSeconds <= 0 {
		policy.FailureWindowSeconds = 300
	}
	if policy.CooldownSeconds <= 0 {
		policy.CooldownSeconds = 300
	}
	return policy
}

func copyInt64(value int64) *int64 {
	copy := value
	return &copy
}
