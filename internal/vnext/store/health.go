package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
)

var (
	ErrTargetHealthNotFound        = errors.New("target health not found")
	ErrAttemptSequenceNotAllocated = errors.New("target attempt sequence was not allocated")
	ErrAttemptSequenceExhausted    = errors.New("target attempt sequence exhausted")
	ErrTargetHealthCASConflict     = errors.New("target health compare-and-swap conflict")
)

// TargetHealthSnapshot is the durable representation of routing.HealthState.
// StateVersion is a storage CAS token and is independent from ConfigRevision.
type TargetHealthSnapshot struct {
	ProviderModelTargetID int64
	State                 routing.HealthState
	StateVersion          int64
	CreatedAt             int64
	UpdatedAt             int64
}

// TargetAttemptPermit is returned before an upstream request is sent. Sequence
// is allocated durably even when the circuit denies the request, which keeps
// all later allocations strictly monotonic across restarts.
type TargetAttemptPermit struct {
	ProviderModelTargetID int64
	Sequence              uint64
	Permit                routing.Permit
	Health                TargetHealthSnapshot
}

// AcquireTargetAttempt atomically validates the physical target revision,
// allocates its next durable attempt sequence, and acquires a normal or
// half-open permit. Callers must not send the upstream request before this
// transaction commits successfully.
func (s *Store) AcquireTargetAttempt(
	ctx context.Context,
	providerModelTargetID int64,
	configRevision routing.Revision,
	policy routing.HealthPolicy,
	now time.Time,
) (TargetAttemptPermit, error) {
	if providerModelTargetID <= 0 {
		return TargetAttemptPermit{}, errors.New("provider model target ID must be positive")
	}
	if err := validateRoutingRevision(configRevision); err != nil {
		return TargetAttemptPermit{}, err
	}
	if now.IsZero() {
		return TargetAttemptPermit{}, errors.New("attempt time must be set")
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return TargetAttemptPermit{}, err
	}
	defer tx.Rollback()

	currentRevision, err := lockTargetRevisionTx(ctx, tx, providerModelTargetID)
	if err != nil {
		return TargetAttemptPermit{}, err
	}
	if err := requireCurrentTargetRevision(configRevision, currentRevision); err != nil {
		return TargetAttemptPermit{}, err
	}

	sequence, err := allocateTargetSequenceTx(ctx, tx, providerModelTargetID)
	if err != nil {
		return TargetAttemptPermit{}, err
	}
	snapshot, exists, err := loadTargetHealthTx(ctx, tx, providerModelTargetID)
	if err != nil {
		return TargetAttemptPermit{}, err
	}
	if !exists {
		state, stateErr := routing.NewHealthState(configRevision)
		if stateErr != nil {
			return TargetAttemptPermit{}, stateErr
		}
		snapshot = TargetHealthSnapshot{
			ProviderModelTargetID: providerModelTargetID,
			State:                 state,
		}
	}

	next, permit, err := routing.AcquirePermit(snapshot.State, policy, configRevision, sequence, now)
	if err != nil {
		return TargetAttemptPermit{}, err
	}
	next = canonicalHealthState(next)
	writeAt := NowMS()
	if !exists {
		snapshot, err = insertTargetHealthTx(ctx, tx, providerModelTargetID, next, writeAt)
	} else if next != snapshot.State {
		snapshot, err = updateTargetHealthCASTx(ctx, tx, snapshot, next, writeAt)
	}
	if err != nil {
		return TargetAttemptPermit{}, err
	}
	if err := tx.Commit(); err != nil {
		return TargetAttemptPermit{}, err
	}
	return TargetAttemptPermit{
		ProviderModelTargetID: providerModelTargetID,
		Sequence:              sequence,
		Permit:                permit,
		Health:                snapshot,
	}, nil
}

// ApplyTargetHealthEvent applies the pure routing reducer under the same
// target transaction used for sequence allocation. Events with an old target
// revision, an unallocated sequence, or a sequence already superseded by a
// newer completion cannot mutate durable health.
func (s *Store) ApplyTargetHealthEvent(
	ctx context.Context,
	providerModelTargetID int64,
	policy routing.HealthPolicy,
	event routing.HealthEvent,
) (TargetHealthSnapshot, routing.ApplyResult, error) {
	if providerModelTargetID <= 0 {
		return TargetHealthSnapshot{}, routing.ApplyResult{}, errors.New("provider model target ID must be positive")
	}
	if err := validateRoutingRevision(event.Revision); err != nil {
		return TargetHealthSnapshot{}, routing.ApplyResult{}, err
	}
	if event.Sequence == 0 || event.Sequence > math.MaxInt64 {
		return TargetHealthSnapshot{}, routing.ApplyResult{}, errors.New("event sequence must fit a positive SQLite INTEGER")
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return TargetHealthSnapshot{}, routing.ApplyResult{}, err
	}
	defer tx.Rollback()

	currentRevision, err := lockTargetRevisionTx(ctx, tx, providerModelTargetID)
	if err != nil {
		return TargetHealthSnapshot{}, routing.ApplyResult{}, err
	}
	snapshot, exists, err := loadTargetHealthTx(ctx, tx, providerModelTargetID)
	if err != nil {
		return TargetHealthSnapshot{}, routing.ApplyResult{}, err
	}
	if event.Revision < routing.Revision(currentRevision) {
		return snapshot, routing.ApplyResult{Reason: routing.ApplyStaleRevision}, nil
	}
	if event.Revision > routing.Revision(currentRevision) {
		return TargetHealthSnapshot{}, routing.ApplyResult{}, fmt.Errorf(
			"target %d config revision %d is ahead of stored revision %d",
			providerModelTargetID, event.Revision, currentRevision,
		)
	}

	var lastAllocated int64
	err = tx.QueryRowContext(ctx, `SELECT last_allocated_sequence FROM target_attempt_sequences
WHERE provider_model_target_id=?`, providerModelTargetID).Scan(&lastAllocated)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, routing.ApplyResult{}, ErrAttemptSequenceNotAllocated
	}
	if err != nil {
		return TargetHealthSnapshot{}, routing.ApplyResult{}, err
	}
	if event.Sequence > uint64(lastAllocated) {
		return snapshot, routing.ApplyResult{}, ErrAttemptSequenceNotAllocated
	}
	if !exists {
		state, stateErr := routing.NewHealthState(event.Revision)
		if stateErr != nil {
			return TargetHealthSnapshot{}, routing.ApplyResult{}, stateErr
		}
		snapshot = TargetHealthSnapshot{
			ProviderModelTargetID: providerModelTargetID,
			State:                 state,
		}
	}

	next, result, err := routing.ApplyHealthEvent(snapshot.State, policy, event)
	if err != nil {
		return TargetHealthSnapshot{}, routing.ApplyResult{}, err
	}
	next = canonicalHealthState(next)
	if !exists || next != snapshot.State {
		writeAt := NowMS()
		if !exists {
			snapshot, err = insertTargetHealthTx(ctx, tx, providerModelTargetID, next, writeAt)
		} else {
			snapshot, err = updateTargetHealthCASTx(ctx, tx, snapshot, next, writeAt)
		}
		if err != nil {
			return TargetHealthSnapshot{}, routing.ApplyResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return TargetHealthSnapshot{}, routing.ApplyResult{}, err
		}
	}
	return snapshot, result, nil
}

func (s *Store) GetTargetHealth(ctx context.Context, providerModelTargetID int64) (TargetHealthSnapshot, error) {
	if providerModelTargetID <= 0 {
		return TargetHealthSnapshot{}, errors.New("provider model target ID must be positive")
	}
	snapshot, err := scanTargetHealth(s.DB.QueryRowContext(ctx, targetHealthSelect+`
WHERE provider_model_target_id=?`, providerModelTargetID))
	if errors.Is(err, sql.ErrNoRows) {
		return TargetHealthSnapshot{}, ErrTargetHealthNotFound
	}
	return snapshot, err
}

const targetHealthSelect = `SELECT provider_model_target_id,config_revision,state_version,phase,capability,
consecutive_failures,failure_window_started_at,last_failure_at,last_success_at,last_failure_incident_id,
last_failure_kind,cooldown_until,last_event_sequence,last_event_at,half_open_sequence,half_open_lease_until,
created_at,updated_at FROM target_health`

func lockTargetRevisionTx(ctx context.Context, tx *sql.Tx, providerModelTargetID int64) (int64, error) {
	var revision int64
	err := tx.QueryRowContext(ctx, `UPDATE provider_model_targets SET revision=revision
WHERE id=? RETURNING revision`, providerModelTargetID).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("provider model target %d: %w", providerModelTargetID, sql.ErrNoRows)
	}
	return revision, err
}

func allocateTargetSequenceTx(ctx context.Context, tx *sql.Tx, providerModelTargetID int64) (uint64, error) {
	now := NowMS()
	var sequence int64
	err := tx.QueryRowContext(ctx, `INSERT INTO target_attempt_sequences(
provider_model_target_id,last_allocated_sequence,created_at,updated_at) VALUES (?,1,?,?)
ON CONFLICT(provider_model_target_id) DO UPDATE SET
  last_allocated_sequence=target_attempt_sequences.last_allocated_sequence+1,
  updated_at=excluded.updated_at
WHERE target_attempt_sequences.last_allocated_sequence < ?
RETURNING last_allocated_sequence`, providerModelTargetID, now, now, int64(math.MaxInt64)).Scan(&sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrAttemptSequenceExhausted
	}
	if err != nil {
		return 0, err
	}
	return uint64(sequence), nil
}

func loadTargetHealthTx(ctx context.Context, tx *sql.Tx, providerModelTargetID int64) (TargetHealthSnapshot, bool, error) {
	snapshot, err := scanTargetHealth(tx.QueryRowContext(ctx, targetHealthSelect+`
WHERE provider_model_target_id=?`, providerModelTargetID))
	if errors.Is(err, sql.ErrNoRows) {
		return TargetHealthSnapshot{}, false, nil
	}
	return snapshot, err == nil, err
}

func scanTargetHealth(row scanner) (TargetHealthSnapshot, error) {
	var snapshot TargetHealthSnapshot
	var configRevision, lastEventSequence, halfOpenSequence int64
	var phase, capability string
	var failureWindowStartedAt, lastFailureAt, lastSuccessAt sql.NullInt64
	var cooldownUntil, lastEventAt, halfOpenLeaseUntil sql.NullInt64
	var lastFailureIncidentID, lastFailureKind sql.NullString
	err := row.Scan(
		&snapshot.ProviderModelTargetID,
		&configRevision,
		&snapshot.StateVersion,
		&phase,
		&capability,
		&snapshot.State.ConsecutiveFailures,
		&failureWindowStartedAt,
		&lastFailureAt,
		&lastSuccessAt,
		&lastFailureIncidentID,
		&lastFailureKind,
		&cooldownUntil,
		&lastEventSequence,
		&lastEventAt,
		&halfOpenSequence,
		&halfOpenLeaseUntil,
		&snapshot.CreatedAt,
		&snapshot.UpdatedAt,
	)
	if err != nil {
		return TargetHealthSnapshot{}, err
	}
	snapshot.State.Revision = routing.Revision(configRevision)
	snapshot.State.Phase = routing.CircuitPhase(phase)
	snapshot.State.Capability = routing.CapabilityState(capability)
	snapshot.State.FailureWindowStartedAt = timeFromNullMillis(failureWindowStartedAt)
	snapshot.State.LastFailureAt = timeFromNullMillis(lastFailureAt)
	snapshot.State.LastSuccessAt = timeFromNullMillis(lastSuccessAt)
	snapshot.State.CooldownUntil = timeFromNullMillis(cooldownUntil)
	snapshot.State.LastEventSequence = uint64(lastEventSequence)
	snapshot.State.LastEventAt = timeFromNullMillis(lastEventAt)
	snapshot.State.HalfOpenSequence = uint64(halfOpenSequence)
	snapshot.State.HalfOpenLeaseUntil = timeFromNullMillis(halfOpenLeaseUntil)
	snapshot.State.LastFailureIncidentID = lastFailureIncidentID.String
	snapshot.State.LastFailureKind = routing.FailureKind(lastFailureKind.String)
	return snapshot, nil
}

func insertTargetHealthTx(
	ctx context.Context,
	tx *sql.Tx,
	providerModelTargetID int64,
	state routing.HealthState,
	writeAt int64,
) (TargetHealthSnapshot, error) {
	_, err := tx.ExecContext(ctx, `INSERT INTO target_health(
provider_model_target_id,config_revision,state_version,phase,capability,consecutive_failures,
failure_window_started_at,last_failure_at,last_success_at,last_failure_incident_id,last_failure_kind,
cooldown_until,last_event_sequence,last_event_at,half_open_sequence,half_open_lease_until,created_at,updated_at)
VALUES (?,?,1,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, healthStateArgs(providerModelTargetID, state, writeAt, writeAt)...)
	if err != nil {
		return TargetHealthSnapshot{}, err
	}
	return TargetHealthSnapshot{
		ProviderModelTargetID: providerModelTargetID,
		State:                 state,
		StateVersion:          1,
		CreatedAt:             writeAt,
		UpdatedAt:             writeAt,
	}, nil
}

func updateTargetHealthCASTx(
	ctx context.Context,
	tx *sql.Tx,
	current TargetHealthSnapshot,
	next routing.HealthState,
	writeAt int64,
) (TargetHealthSnapshot, error) {
	args := healthStateArgs(current.ProviderModelTargetID, next, current.CreatedAt, writeAt)
	// The INSERT-only target ID and created_at positions are not part of an
	// update. Keep one encoder so nullable state fields cannot drift between
	// the two write paths.
	result, err := tx.ExecContext(ctx, `UPDATE target_health SET
config_revision=?,phase=?,capability=?,consecutive_failures=?,failure_window_started_at=?,last_failure_at=?,
last_success_at=?,last_failure_incident_id=?,last_failure_kind=?,cooldown_until=?,last_event_sequence=?,
last_event_at=?,half_open_sequence=?,half_open_lease_until=?,state_version=state_version+1,updated_at=?
WHERE provider_model_target_id=? AND state_version=?`,
		args[1], args[2], args[3], args[4], args[5], args[6], args[7], args[8], args[9], args[10],
		args[11], args[12], args[13], args[14], writeAt, current.ProviderModelTargetID, current.StateVersion,
	)
	if err != nil {
		return TargetHealthSnapshot{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return TargetHealthSnapshot{}, err
	}
	if changed != 1 {
		return TargetHealthSnapshot{}, ErrTargetHealthCASConflict
	}
	current.State = next
	current.StateVersion++
	current.UpdatedAt = writeAt
	return current, nil
}

func healthStateArgs(providerModelTargetID int64, state routing.HealthState, createdAt, updatedAt int64) []any {
	return []any{
		providerModelTargetID,
		int64(state.Revision),
		string(state.Phase),
		string(state.Capability),
		state.ConsecutiveFailures,
		nullableHealthTime(state.FailureWindowStartedAt),
		nullableHealthTime(state.LastFailureAt),
		nullableHealthTime(state.LastSuccessAt),
		nullableString(state.LastFailureIncidentID),
		nullableString(string(state.LastFailureKind)),
		nullableHealthTime(state.CooldownUntil),
		int64(state.LastEventSequence),
		nullableHealthTime(state.LastEventAt),
		int64(state.HalfOpenSequence),
		nullableHealthTime(state.HalfOpenLeaseUntil),
		createdAt,
		updatedAt,
	}
}

func validateRoutingRevision(revision routing.Revision) error {
	if revision == 0 || revision > routing.Revision(math.MaxInt64) {
		return errors.New("target config revision must fit a positive SQLite INTEGER")
	}
	return nil
}

func requireCurrentTargetRevision(configRevision routing.Revision, currentRevision int64) error {
	current := routing.Revision(currentRevision)
	if configRevision < current {
		return routing.ErrStaleRevision
	}
	if configRevision > current {
		return fmt.Errorf("target config revision %d is ahead of stored revision %d", configRevision, currentRevision)
	}
	return nil
}

func canonicalHealthState(state routing.HealthState) routing.HealthState {
	state.FailureWindowStartedAt = canonicalHealthTime(state.FailureWindowStartedAt)
	state.LastFailureAt = canonicalHealthTime(state.LastFailureAt)
	state.LastSuccessAt = canonicalHealthTime(state.LastSuccessAt)
	state.CooldownUntil = canonicalHealthTime(state.CooldownUntil)
	state.HalfOpenLeaseUntil = canonicalHealthTime(state.HalfOpenLeaseUntil)
	state.LastEventAt = canonicalHealthTime(state.LastEventAt)
	return state
}

func canonicalHealthTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return time.UnixMilli(value.UTC().UnixMilli()).UTC()
}

func nullableHealthTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().UnixMilli()
}

func timeFromNullMillis(value sql.NullInt64) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return time.UnixMilli(value.Int64).UTC()
}
