package routing

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

type Revision uint64

type CircuitPhase string

const (
	CircuitClosed   CircuitPhase = "closed"
	CircuitSuspect  CircuitPhase = "suspect"
	CircuitOpen     CircuitPhase = "open"
	CircuitHalfOpen CircuitPhase = "half_open"
)

type CapabilityState string

const (
	CapabilityUnknown     CapabilityState = "unknown"
	CapabilitySupported   CapabilityState = "supported"
	CapabilityUnsupported CapabilityState = "unsupported"
)

type HealthPolicy struct {
	FailureThreshold int
	FailureWindow    time.Duration
	Cooldown         time.Duration
	HalfOpenLease    time.Duration
}

func DefaultHealthPolicy() HealthPolicy {
	return HealthPolicy{
		FailureThreshold: 2,
		FailureWindow:    5 * time.Minute,
		Cooldown:         5 * time.Minute,
		HalfOpenLease:    30 * time.Second,
	}
}

func (policy HealthPolicy) normalized() HealthPolicy {
	defaults := DefaultHealthPolicy()
	if policy.FailureThreshold < 2 {
		policy.FailureThreshold = defaults.FailureThreshold
	}
	if policy.FailureWindow <= 0 {
		policy.FailureWindow = defaults.FailureWindow
	}
	if policy.Cooldown <= 0 {
		policy.Cooldown = defaults.Cooldown
	}
	if policy.HalfOpenLease <= 0 {
		policy.HalfOpenLease = defaults.HalfOpenLease
	}
	return policy
}

type HealthState struct {
	Revision Revision

	Phase               CircuitPhase
	Capability          CapabilityState
	ConsecutiveFailures int

	FailureWindowStartedAt time.Time
	LastFailureAt          time.Time
	LastSuccessAt          time.Time
	CooldownUntil          time.Time
	HalfOpenLeaseUntil     time.Time
	HalfOpenSequence       uint64
	LastEventSequence      uint64
	LastEventAt            time.Time
	LastFailureIncidentID  string
	LastFailureKind        FailureKind
}

func NewHealthState(revision Revision) (HealthState, error) {
	if revision == 0 {
		return HealthState{}, errors.New("routing: target revision must be positive")
	}
	return freshHealthState(revision), nil
}

func freshHealthState(revision Revision) HealthState {
	return HealthState{
		Revision:   revision,
		Phase:      CircuitClosed,
		Capability: CapabilityUnknown,
	}
}

var ErrStaleRevision = errors.New("routing: stale target revision")

// ReconcileRevision resets all learned health when configuration advances.
// An older route snapshot is rejected so it cannot resurrect invalid health.
func ReconcileRevision(state HealthState, revision Revision) (HealthState, bool, error) {
	if revision == 0 {
		return state, false, errors.New("routing: target revision must be positive")
	}
	if state.Revision == 0 || revision > state.Revision {
		return freshHealthState(revision), true, nil
	}
	if revision < state.Revision {
		return state, false, ErrStaleRevision
	}
	return normalizeHealthState(state), false, nil
}

func normalizeHealthState(state HealthState) HealthState {
	if state.Phase == "" {
		state.Phase = CircuitClosed
	}
	if state.Capability == "" {
		state.Capability = CapabilityUnknown
	}
	return state
}

type PermitMode string

const (
	PermitNormal   PermitMode = "normal"
	PermitHalfOpen PermitMode = "half_open"
)

type PermitReason string

const (
	PermitGranted       PermitReason = "granted"
	PermitCooling       PermitReason = "cooling"
	PermitLeaseHeld     PermitReason = "half_open_lease_held"
	PermitUnsupported   PermitReason = "unsupported"
	PermitStaleRevision PermitReason = "stale_revision"
	PermitStaleSequence PermitReason = "stale_sequence"
)

type Permit struct {
	Allowed  bool
	Mode     PermitMode
	Reason   PermitReason
	Sequence uint64
}

// AcquirePermit is a pure state transition. Callers that persist HealthState
// must compare-and-swap the returned state atomically. Circuit provides that
// ownership rule for in-memory use.
func AcquirePermit(state HealthState, policy HealthPolicy, revision Revision, sequence uint64, now time.Time) (HealthState, Permit, error) {
	if sequence == 0 {
		return state, Permit{}, errors.New("routing: attempt sequence must be positive")
	}
	if now.IsZero() {
		return state, Permit{}, errors.New("routing: permit time must be set")
	}

	next, _, err := ReconcileRevision(state, revision)
	if err != nil {
		return state, Permit{}, err
	}
	if sequence <= next.LastEventSequence {
		return next, Permit{Reason: PermitStaleSequence, Sequence: sequence}, nil
	}
	if next.Capability == CapabilityUnsupported {
		return next, Permit{Reason: PermitUnsupported, Sequence: sequence}, nil
	}

	policy = policy.normalized()
	switch next.Phase {
	case CircuitClosed, CircuitSuspect:
		return next, Permit{Allowed: true, Mode: PermitNormal, Reason: PermitGranted, Sequence: sequence}, nil
	case CircuitOpen:
		if next.CooldownUntil.After(now) {
			return next, Permit{Reason: PermitCooling, Sequence: sequence}, nil
		}
	case CircuitHalfOpen:
		if next.HalfOpenLeaseUntil.After(now) {
			return next, Permit{Reason: PermitLeaseHeld, Sequence: sequence}, nil
		}
	default:
		return state, Permit{}, fmt.Errorf("routing: unknown circuit phase %q", next.Phase)
	}

	if sequence <= next.HalfOpenSequence {
		return next, Permit{Reason: PermitStaleSequence, Sequence: sequence}, nil
	}
	next.Phase = CircuitHalfOpen
	next.HalfOpenSequence = sequence
	next.HalfOpenLeaseUntil = now.Add(policy.HalfOpenLease)
	return next, Permit{Allowed: true, Mode: PermitHalfOpen, Reason: PermitGranted, Sequence: sequence}, nil
}

type HealthOutcome string

const (
	HealthSuccess HealthOutcome = "success"
	HealthFailure HealthOutcome = "failure"
)

type HealthEvent struct {
	Revision   Revision
	Sequence   uint64
	OccurredAt time.Time
	Outcome    HealthOutcome
	IncidentID string
	Failure    Failure
}

type ApplyReason string

const (
	ApplyAccepted          ApplyReason = "accepted"
	ApplyStaleRevision     ApplyReason = "stale_revision"
	ApplyStaleSequence     ApplyReason = "stale_sequence"
	ApplyHalfOpenMismatch  ApplyReason = "half_open_mismatch"
	ApplyNonTargetFailure  ApplyReason = "non_target_failure"
	ApplyDuplicateIncident ApplyReason = "duplicate_incident"
)

type ApplyResult struct {
	Applied bool
	Counted bool
	Reason  ApplyReason
}

// ApplyHealthEvent is ordered by the sequence assigned when the attempt began,
// not by completion time. A slow older request therefore cannot overwrite a
// newer result that completed first.
func ApplyHealthEvent(state HealthState, policy HealthPolicy, event HealthEvent) (HealthState, ApplyResult, error) {
	if event.Revision == 0 {
		return state, ApplyResult{}, errors.New("routing: event revision must be positive")
	}
	if event.Sequence == 0 {
		return state, ApplyResult{}, errors.New("routing: event sequence must be positive")
	}
	if event.OccurredAt.IsZero() {
		return state, ApplyResult{}, errors.New("routing: event time must be set")
	}
	if event.Outcome != HealthSuccess && event.Outcome != HealthFailure {
		return state, ApplyResult{}, fmt.Errorf("routing: unknown health outcome %q", event.Outcome)
	}

	next, _, err := ReconcileRevision(state, event.Revision)
	if errors.Is(err, ErrStaleRevision) {
		return state, ApplyResult{Reason: ApplyStaleRevision}, nil
	}
	if err != nil {
		return state, ApplyResult{}, err
	}
	if event.Sequence <= next.LastEventSequence {
		return next, ApplyResult{Reason: ApplyStaleSequence}, nil
	}
	if next.Phase == CircuitHalfOpen && event.Sequence != next.HalfOpenSequence {
		return next, ApplyResult{Reason: ApplyHalfOpenMismatch}, nil
	}

	policy = policy.normalized()
	eventAt := event.OccurredAt
	if eventAt.Before(next.LastEventAt) {
		eventAt = next.LastEventAt
	}

	if event.Outcome == HealthSuccess {
		next.Phase = CircuitClosed
		next.Capability = CapabilitySupported
		next.ConsecutiveFailures = 0
		next.FailureWindowStartedAt = time.Time{}
		next.LastSuccessAt = eventAt
		next.CooldownUntil = time.Time{}
		next.HalfOpenLeaseUntil = time.Time{}
		next.HalfOpenSequence = 0
		next.LastFailureIncidentID = ""
		next.LastFailureKind = ""
		stampHealthEvent(&next, event.Sequence, eventAt)
		return next, ApplyResult{Applied: true, Reason: ApplyAccepted}, nil
	}

	disposition := event.Failure.Disposition()
	if disposition.Scope != FailureScopeTarget || !disposition.PenalizeTarget {
		return next, ApplyResult{Reason: ApplyNonTargetFailure}, nil
	}

	wasHalfOpen := next.Phase == CircuitHalfOpen
	next.LastFailureKind = event.Failure.Kind
	if disposition.MarkUnsupported {
		next.Phase = CircuitClosed
		next.Capability = CapabilityUnsupported
		next.ConsecutiveFailures = 0
		next.FailureWindowStartedAt = time.Time{}
		next.LastFailureAt = eventAt
		next.CooldownUntil = time.Time{}
		next.HalfOpenLeaseUntil = time.Time{}
		next.HalfOpenSequence = 0
		next.LastFailureIncidentID = event.IncidentID
		stampHealthEvent(&next, event.Sequence, eventAt)
		return next, ApplyResult{Applied: true, Counted: true, Reason: ApplyAccepted}, nil
	}

	if event.IncidentID != "" && event.IncidentID == next.LastFailureIncidentID {
		stampHealthEvent(&next, event.Sequence, eventAt)
		return next, ApplyResult{Applied: true, Reason: ApplyDuplicateIncident}, nil
	}

	windowStartedAt := next.FailureWindowStartedAt
	if windowStartedAt.IsZero() && next.ConsecutiveFailures > 0 {
		// Compatibility for state created before the explicit window start was
		// introduced. The previous reducer used LastFailureAt as its boundary.
		windowStartedAt = next.LastFailureAt
	}
	withinWindow := next.ConsecutiveFailures > 0 && !windowStartedAt.IsZero() &&
		eventAt.Sub(windowStartedAt) <= policy.FailureWindow
	failures := 1
	if withinWindow {
		failures = next.ConsecutiveFailures + 1
	} else {
		windowStartedAt = eventAt
	}

	next.FailureWindowStartedAt = windowStartedAt
	next.LastFailureAt = eventAt
	next.LastFailureIncidentID = event.IncidentID
	next.ConsecutiveFailures = failures
	next.Capability = CapabilityUnknown
	stampHealthEvent(&next, event.Sequence, eventAt)

	if !wasHalfOpen && next.Phase != CircuitOpen && failures < policy.FailureThreshold {
		next.Phase = CircuitSuspect
		next.CooldownUntil = time.Time{}
		next.HalfOpenLeaseUntil = time.Time{}
		next.HalfOpenSequence = 0
		return next, ApplyResult{Applied: true, Counted: true, Reason: ApplyAccepted}, nil
	}

	cooldown := policy.Cooldown
	if event.Failure.RetryAfter > cooldown {
		cooldown = event.Failure.RetryAfter
	}
	cooldownUntil := eventAt.Add(cooldown)
	if next.CooldownUntil.After(cooldownUntil) {
		cooldownUntil = next.CooldownUntil
	}
	next.Phase = CircuitOpen
	if next.ConsecutiveFailures < policy.FailureThreshold {
		next.ConsecutiveFailures = policy.FailureThreshold
	}
	next.CooldownUntil = cooldownUntil
	next.HalfOpenLeaseUntil = time.Time{}
	next.HalfOpenSequence = 0
	return next, ApplyResult{Applied: true, Counted: true, Reason: ApplyAccepted}, nil
}

func stampHealthEvent(state *HealthState, sequence uint64, occurredAt time.Time) {
	state.LastEventSequence = sequence
	state.LastEventAt = occurredAt
}

type TargetEligibility struct {
	Eligible bool
	Mode     PermitMode
	Reason   PermitReason
}

func EvaluateTarget(state HealthState, revision Revision, now time.Time) TargetEligibility {
	if revision == 0 || now.IsZero() {
		return TargetEligibility{Reason: PermitStaleSequence}
	}
	if state.Revision == 0 || revision > state.Revision {
		return TargetEligibility{Eligible: true, Mode: PermitNormal, Reason: PermitGranted}
	}
	if revision < state.Revision {
		return TargetEligibility{Reason: PermitStaleRevision}
	}
	state = normalizeHealthState(state)
	if state.Capability == CapabilityUnsupported {
		return TargetEligibility{Reason: PermitUnsupported}
	}
	switch state.Phase {
	case CircuitClosed, CircuitSuspect:
		return TargetEligibility{Eligible: true, Mode: PermitNormal, Reason: PermitGranted}
	case CircuitOpen:
		if state.CooldownUntil.After(now) {
			return TargetEligibility{Reason: PermitCooling}
		}
		return TargetEligibility{Eligible: true, Mode: PermitHalfOpen, Reason: PermitGranted}
	case CircuitHalfOpen:
		if state.HalfOpenLeaseUntil.After(now) {
			return TargetEligibility{Reason: PermitLeaseHeld}
		}
		return TargetEligibility{Eligible: true, Mode: PermitHalfOpen, Reason: PermitGranted}
	default:
		return TargetEligibility{Reason: PermitCooling}
	}
}

// Circuit owns one HealthState and makes half-open permit acquisition atomic.
// The reducer functions remain available for durable-store integrations.
type Circuit struct {
	mu     sync.Mutex
	policy HealthPolicy
	state  HealthState
}

func NewCircuit(state HealthState, policy HealthPolicy) (*Circuit, error) {
	if state.Revision == 0 {
		return nil, errors.New("routing: circuit state revision must be positive")
	}
	return &Circuit{policy: policy.normalized(), state: normalizeHealthState(state)}, nil
}

func (circuit *Circuit) Snapshot() HealthState {
	circuit.mu.Lock()
	defer circuit.mu.Unlock()
	return circuit.state
}

func (circuit *Circuit) Reconcile(revision Revision) (bool, error) {
	circuit.mu.Lock()
	defer circuit.mu.Unlock()
	next, changed, err := ReconcileRevision(circuit.state, revision)
	if err == nil {
		circuit.state = next
	}
	return changed, err
}

func (circuit *Circuit) Acquire(revision Revision, sequence uint64, now time.Time) (Permit, error) {
	circuit.mu.Lock()
	defer circuit.mu.Unlock()
	next, permit, err := AcquirePermit(circuit.state, circuit.policy, revision, sequence, now)
	if err == nil {
		circuit.state = next
	}
	return permit, err
}

func (circuit *Circuit) Apply(event HealthEvent) (ApplyResult, error) {
	circuit.mu.Lock()
	defer circuit.mu.Unlock()
	next, result, err := ApplyHealthEvent(circuit.state, circuit.policy, event)
	if err == nil {
		circuit.state = next
	}
	return result, err
}
