package routing

import (
	"testing"
	"time"
)

func TestDeriveCircuitTransitionsReplaysLiveAndProbeEvidence(t *testing.T) {
	start := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	transitions, err := DeriveCircuitTransitions(1, DefaultHealthPolicy(), []HealthEvidence{
		{
			ID: "live-1", RequestID: "request-1", Source: HealthEvidenceLiveTraffic, Revision: 1,
			StartedAt: start, OccurredAt: start.Add(time.Second), Outcome: HealthFailure,
			Failure: Failure{Kind: FailureTransport}, IncidentID: "request-1:0",
		},
		{
			ID: "probe-2", Source: HealthEvidenceProbe, Revision: 1,
			StartedAt: start.Add(time.Minute), OccurredAt: start.Add(time.Minute + time.Second),
			Outcome: HealthFailure, Failure: Failure{Kind: FailureUpstreamTransient}, IncidentID: "probe:run-2:9",
		},
		{
			ID: "live-3", RequestID: "request-3", Source: HealthEvidenceLiveTraffic, Revision: 1,
			StartedAt: start.Add(17 * time.Minute), OccurredAt: start.Add(17*time.Minute + time.Second),
			Outcome: HealthSuccess,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(transitions) != 4 {
		t.Fatalf("transitions = %+v", transitions)
	}
	assertTransition(t, transitions[0], CircuitClosed, CircuitSuspect, HealthEvidenceLiveTraffic, "target_failure_observed")
	assertTransition(t, transitions[1], CircuitSuspect, CircuitOpen, HealthEvidenceProbe, "failure_threshold_reached")
	assertTransition(t, transitions[2], CircuitOpen, CircuitHalfOpen, HealthEvidenceTimer, "cooldown_elapsed")
	assertTransition(t, transitions[3], CircuitHalfOpen, CircuitClosed, HealthEvidenceLiveTraffic, "recovery_succeeded")
	if transitions[0].RequestID != "request-1" || transitions[3].RequestID != "request-3" {
		t.Fatalf("request evidence lost: %+v", transitions)
	}
}

func TestDeriveCircuitTransitionsIgnoresCredentialLocalFailure(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	transitions, err := DeriveCircuitTransitions(1, DefaultHealthPolicy(), []HealthEvidence{{
		ID: "live-rate", Source: HealthEvidenceLiveTraffic, Revision: 1,
		StartedAt: now, OccurredAt: now.Add(time.Second), Outcome: HealthFailure,
		Failure: Failure{Kind: FailureCredentialRateLimited}, IncidentID: "request-rate:0",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(transitions) != 0 {
		t.Fatalf("credential-local failure changed target circuit: %+v", transitions)
	}
}

func assertTransition(
	t *testing.T,
	transition CircuitTransition,
	from, to CircuitPhase,
	trigger HealthEvidenceSource,
	reason string,
) {
	t.Helper()
	if transition.FromPhase != from || transition.ToPhase != to || transition.Trigger != trigger || transition.Reason != reason {
		t.Fatalf("transition = %+v, want %s -> %s via %s (%s)", transition, from, to, trigger, reason)
	}
}
