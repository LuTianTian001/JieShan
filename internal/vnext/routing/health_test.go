package routing

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHealthTransitionsSuspectOpenHalfOpenClosed(t *testing.T) {
	policy := DefaultHealthPolicy()
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	state, err := NewHealthState(1)
	if err != nil {
		t.Fatal(err)
	}

	state, result, err := ApplyHealthEvent(state, policy, HealthEvent{
		Revision: 1, Sequence: 1, OccurredAt: now,
		Outcome: HealthFailure, IncidentID: "request-1",
		Failure: Failure{Kind: FailureTransport},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || !result.Counted || state.Phase != CircuitSuspect || state.ConsecutiveFailures != 1 {
		t.Fatalf("first independent failure must be suspect: result=%+v state=%+v", result, state)
	}

	state, result, err = ApplyHealthEvent(state, policy, HealthEvent{
		Revision: 1, Sequence: 2, OccurredAt: now.Add(time.Minute),
		Outcome: HealthFailure, IncidentID: "request-2",
		Failure: Failure{Kind: FailureUpstreamTransient},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCooldown := now.Add(time.Minute + 15*time.Minute)
	if !result.Counted || state.Phase != CircuitOpen || !state.CooldownUntil.Equal(wantCooldown) {
		t.Fatalf("second independent failure must open circuit until %s: %+v", wantCooldown, state)
	}

	state, permit, err := AcquirePermit(state, policy, 1, 3, wantCooldown.Add(-time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if permit.Allowed || permit.Reason != PermitCooling {
		t.Fatalf("cooling target received permit: %+v", permit)
	}

	state, permit, err = AcquirePermit(state, policy, 1, 3, wantCooldown)
	if err != nil {
		t.Fatal(err)
	}
	if !permit.Allowed || permit.Mode != PermitHalfOpen || state.Phase != CircuitHalfOpen {
		t.Fatalf("cooldown expiry must grant one half-open permit: permit=%+v state=%+v", permit, state)
	}

	state, result, err = ApplyHealthEvent(state, policy, HealthEvent{
		Revision: 1, Sequence: 3, OccurredAt: wantCooldown.Add(time.Second), Outcome: HealthSuccess,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || state.Phase != CircuitClosed || state.ConsecutiveFailures != 0 || !state.CooldownUntil.IsZero() {
		t.Fatalf("successful half-open attempt must close circuit: result=%+v state=%+v", result, state)
	}
}

func TestFailureOutsideWindowStartsNewSuspectSequence(t *testing.T) {
	policy := DefaultHealthPolicy()
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	state, _ := NewHealthState(1)
	state, _, _ = ApplyHealthEvent(state, policy, HealthEvent{
		Revision: 1, Sequence: 1, OccurredAt: now, Outcome: HealthFailure,
		IncidentID: "one", Failure: Failure{Kind: FailureTransport},
	})
	state, _, _ = ApplyHealthEvent(state, policy, HealthEvent{
		Revision: 1, Sequence: 2, OccurredAt: now.Add(policy.FailureWindow + time.Second), Outcome: HealthFailure,
		IncidentID: "two", Failure: Failure{Kind: FailureTransport},
	})
	if state.Phase != CircuitSuspect || state.ConsecutiveFailures != 1 {
		t.Fatalf("failure outside window must start a new suspect sequence: %+v", state)
	}
}

func TestFirstOutputTimeoutOpensCircuitOnFirstIncident(t *testing.T) {
	policy := DefaultHealthPolicy()
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	state, _ := NewHealthState(1)
	state, result, err := ApplyHealthEvent(state, policy, HealthEvent{
		Revision: 1, Sequence: 1, OccurredAt: now, Outcome: HealthFailure,
		IncidentID: "slow-first-token", Failure: Failure{Kind: FailureFirstOutputTimeout},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || !result.Counted || state.Phase != CircuitOpen || state.ConsecutiveFailures != 1 ||
		!state.CooldownUntil.Equal(now.Add(policy.Cooldown)) {
		t.Fatalf("first-output timeout did not immediately open: result=%+v state=%+v", result, state)
	}
}

func TestDuplicateIncidentDoesNotReachThreshold(t *testing.T) {
	policy := DefaultHealthPolicy()
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	state, _ := NewHealthState(1)
	state, _, _ = ApplyHealthEvent(state, policy, HealthEvent{
		Revision: 1, Sequence: 1, OccurredAt: now, Outcome: HealthFailure,
		IncidentID: "same-request", Failure: Failure{Kind: FailureTransport},
	})
	state, result, err := ApplyHealthEvent(state, policy, HealthEvent{
		Revision: 1, Sequence: 2, OccurredAt: now.Add(time.Second), Outcome: HealthFailure,
		IncidentID: "same-request", Failure: Failure{Kind: FailureTransport},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Counted || result.Reason != ApplyDuplicateIncident || state.Phase != CircuitSuspect || state.ConsecutiveFailures != 1 {
		t.Fatalf("duplicate incident affected threshold: result=%+v state=%+v", result, state)
	}
}

func TestOlderEventCannotOverwriteNewerSuccess(t *testing.T) {
	policy := DefaultHealthPolicy()
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	state, _ := NewHealthState(1)
	state, success, err := ApplyHealthEvent(state, policy, HealthEvent{
		Revision: 1, Sequence: 20, OccurredAt: now.Add(2 * time.Second), Outcome: HealthSuccess,
	})
	if err != nil || !success.Applied {
		t.Fatalf("newer success failed: result=%+v err=%v", success, err)
	}

	got, stale, err := ApplyHealthEvent(state, policy, HealthEvent{
		Revision: 1, Sequence: 10, OccurredAt: now.Add(3 * time.Second), Outcome: HealthFailure,
		IncidentID: "slow-old-request", Failure: Failure{Kind: FailureTransport},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stale.Applied || stale.Reason != ApplyStaleSequence || got != state {
		t.Fatalf("older completion overwrote newer state: result=%+v got=%+v want=%+v", stale, got, state)
	}
}

func TestOlderSuccessCannotCloseNewerOpenState(t *testing.T) {
	policy := DefaultHealthPolicy()
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	state := HealthState{
		Revision: 1, Phase: CircuitSuspect, Capability: CapabilityUnknown,
		ConsecutiveFailures: 1, LastFailureAt: now, LastEventSequence: 10, LastEventAt: now,
	}
	state, result, err := ApplyHealthEvent(state, policy, HealthEvent{
		Revision: 1, Sequence: 20, OccurredAt: now.Add(time.Second), Outcome: HealthFailure,
		IncidentID: "newer-failure", Failure: Failure{Kind: FailureTransport},
	})
	if err != nil || !result.Applied || state.Phase != CircuitOpen {
		t.Fatalf("newer failure did not open circuit: result=%+v state=%+v err=%v", result, state, err)
	}

	got, stale, err := ApplyHealthEvent(state, policy, HealthEvent{
		Revision: 1, Sequence: 15, OccurredAt: now.Add(2 * time.Second), Outcome: HealthSuccess,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stale.Applied || stale.Reason != ApplyStaleSequence || got != state {
		t.Fatalf("older success closed newer open state: result=%+v got=%+v", stale, got)
	}
}

func TestRevisionAdvanceInvalidatesHealthAndRejectsOldEvents(t *testing.T) {
	policy := DefaultHealthPolicy()
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	state := HealthState{
		Revision: 1, Phase: CircuitOpen, Capability: CapabilityUnsupported,
		ConsecutiveFailures: 2, CooldownUntil: now.Add(time.Hour), LastEventSequence: 8,
	}

	state, changed, err := ReconcileRevision(state, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || state.Revision != 2 || state.Phase != CircuitClosed || state.Capability != CapabilityUnknown || state.ConsecutiveFailures != 0 {
		t.Fatalf("new revision did not invalidate learned health: %+v", state)
	}

	got, result, err := ApplyHealthEvent(state, policy, HealthEvent{
		Revision: 1, Sequence: 100, OccurredAt: now.Add(time.Minute), Outcome: HealthFailure,
		IncidentID: "old-revision", Failure: Failure{Kind: FailureTransport},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.Reason != ApplyStaleRevision || got != state {
		t.Fatalf("old revision event changed new state: result=%+v got=%+v", result, got)
	}
}

func TestHalfOpenFailureReopensImmediately(t *testing.T) {
	policy := DefaultHealthPolicy()
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	state := HealthState{
		Revision: 1, Phase: CircuitOpen, Capability: CapabilityUnknown,
		ConsecutiveFailures: 2, CooldownUntil: now, LastEventSequence: 2,
	}
	state, permit, err := AcquirePermit(state, policy, 1, 3, now)
	if err != nil || !permit.Allowed {
		t.Fatalf("cannot acquire half-open permit: permit=%+v err=%v", permit, err)
	}
	state, result, err := ApplyHealthEvent(state, policy, HealthEvent{
		Revision: 1, Sequence: 3, OccurredAt: now.Add(time.Second), Outcome: HealthFailure,
		IncidentID: "half-open", Failure: Failure{Kind: FailureTransport},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || state.Phase != CircuitOpen || !state.CooldownUntil.Equal(now.Add(time.Second+policy.Cooldown)) {
		t.Fatalf("failed half-open attempt did not reopen circuit: result=%+v state=%+v", result, state)
	}
}

func TestCustomCooldownAndRetryAfterUseLongerDelay(t *testing.T) {
	policy := HealthPolicy{
		FailureThreshold: 2,
		FailureWindow:    10 * time.Minute,
		Cooldown:         2 * time.Minute,
		HalfOpenLease:    time.Minute,
	}
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	state, _ := NewHealthState(1)
	state, _, _ = ApplyHealthEvent(state, policy, HealthEvent{
		Revision: 1, Sequence: 1, OccurredAt: now, Outcome: HealthFailure,
		IncidentID: "one", Failure: Failure{Kind: FailureTransport},
	})
	state, _, _ = ApplyHealthEvent(state, policy, HealthEvent{
		Revision: 1, Sequence: 2, OccurredAt: now.Add(time.Second), Outcome: HealthFailure,
		IncidentID: "two", Failure: Failure{Kind: FailureUpstreamTransient, RetryAfter: 7 * time.Minute},
	})
	want := now.Add(time.Second + 7*time.Minute)
	if state.Phase != CircuitOpen || !state.CooldownUntil.Equal(want) {
		t.Fatalf("cooldown = %s, want retry-after deadline %s", state.CooldownUntil, want)
	}
}

func TestCircuitGrantsOnlyOneConcurrentHalfOpenPermit(t *testing.T) {
	policy := DefaultHealthPolicy()
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	circuit, err := NewCircuit(HealthState{
		Revision: 1, Phase: CircuitOpen, Capability: CapabilityUnknown,
		CooldownUntil: now.Add(-time.Second), LastEventSequence: 2,
	}, policy)
	if err != nil {
		t.Fatal(err)
	}

	var granted atomic.Int32
	var wait sync.WaitGroup
	for index := 0; index < 32; index++ {
		wait.Add(1)
		go func(sequence uint64) {
			defer wait.Done()
			permit, acquireErr := circuit.Acquire(1, sequence, now)
			if acquireErr != nil {
				t.Errorf("acquire: %v", acquireErr)
				return
			}
			if permit.Allowed {
				granted.Add(1)
			}
		}(uint64(index + 3))
	}
	wait.Wait()
	if got := granted.Load(); got != 1 {
		t.Fatalf("half-open permits granted = %d, want 1", got)
	}
}

func TestExpiredHalfOpenLeaseRejectsLateResult(t *testing.T) {
	policy := DefaultHealthPolicy()
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	state := HealthState{
		Revision: 1, Phase: CircuitHalfOpen, Capability: CapabilityUnknown,
		HalfOpenSequence: 3, HalfOpenLeaseUntil: now.Add(-time.Second), LastEventSequence: 2,
	}
	state, permit, err := AcquirePermit(state, policy, 1, 4, now)
	if err != nil || !permit.Allowed || permit.Sequence != 4 {
		t.Fatalf("replacement permit failed: permit=%+v err=%v", permit, err)
	}
	got, result, err := ApplyHealthEvent(state, policy, HealthEvent{
		Revision: 1, Sequence: 3, OccurredAt: now.Add(time.Second), Outcome: HealthSuccess,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.Reason != ApplyHalfOpenMismatch || got != state {
		t.Fatalf("expired lease result overwrote replacement: result=%+v", result)
	}
}

func TestStreamTruncatedBeforeCommitPenalizesAndRetriesNextTarget(t *testing.T) {
	disposition := (Failure{Kind: FailureStreamTruncated, ResponseCommitted: false}).Disposition()
	if disposition.Scope != FailureScopeTarget || !disposition.PenalizeTarget || disposition.Retry != RetryNextTarget || disposition.ResponseCommitted {
		t.Fatalf("unexpected pre-commit stream truncation disposition: %+v", disposition)
	}

	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	state, _ := NewHealthState(1)
	state, result, err := ApplyHealthEvent(state, DefaultHealthPolicy(), HealthEvent{
		Revision: 1, Sequence: 1, OccurredAt: now, Outcome: HealthFailure,
		IncidentID: "truncated-stream", Failure: Failure{Kind: FailureStreamTruncated},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Counted || state.Phase != CircuitSuspect || state.ConsecutiveFailures != 1 {
		t.Fatalf("stream truncation did not affect future health: result=%+v state=%+v", result, state)
	}
}

func TestStreamTruncatedAfterCommitPenalizesAndStopsRetry(t *testing.T) {
	disposition := (Failure{Kind: FailureStreamTruncated, ResponseCommitted: true}).Disposition()
	if disposition.Scope != FailureScopeTarget || !disposition.PenalizeTarget || disposition.Retry != RetryStop || !disposition.ResponseCommitted {
		t.Fatalf("unexpected committed stream truncation disposition: %+v", disposition)
	}
}
