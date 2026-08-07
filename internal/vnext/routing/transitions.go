package routing

import (
	"errors"
	"sort"
	"strings"
	"time"
)

type HealthEvidenceSource string

const (
	HealthEvidenceLiveTraffic HealthEvidenceSource = "live_traffic"
	HealthEvidenceProbe       HealthEvidenceSource = "probe"
	HealthEvidenceTimer       HealthEvidenceSource = "timer"
)

type HealthEvidence struct {
	ID         string
	RequestID  string
	Source     HealthEvidenceSource
	Revision   Revision
	StartedAt  time.Time
	OccurredAt time.Time
	Outcome    HealthOutcome
	Failure    Failure
	IncidentID string
}

type CircuitTransition struct {
	ID          string
	FromPhase   CircuitPhase
	ToPhase     CircuitPhase
	Trigger     HealthEvidenceSource
	Reason      string
	FailureKind FailureKind
	RequestID   string
	OccurredAt  time.Time
}

// DeriveCircuitTransitions replays durable live-traffic and probe evidence
// through the same reducer used by routing. Throttle and credential-local
// observations must be filtered by the caller because they are not target
// circuit evidence.
func DeriveCircuitTransitions(
	revision Revision,
	policy HealthPolicy,
	evidence []HealthEvidence,
) ([]CircuitTransition, error) {
	if revision == 0 {
		return nil, errors.New("routing: transition revision must be positive")
	}
	ordered := append([]HealthEvidence(nil), evidence...)
	sort.SliceStable(ordered, func(left, right int) bool {
		leftStarted := evidenceStartedAt(ordered[left])
		rightStarted := evidenceStartedAt(ordered[right])
		if leftStarted.Equal(rightStarted) {
			if ordered[left].OccurredAt.Equal(ordered[right].OccurredAt) {
				return ordered[left].ID < ordered[right].ID
			}
			return ordered[left].OccurredAt.Before(ordered[right].OccurredAt)
		}
		return leftStarted.Before(rightStarted)
	})

	state, err := NewHealthState(revision)
	if err != nil {
		return nil, err
	}
	transitions := make([]CircuitTransition, 0)
	for index, item := range ordered {
		if item.Revision != revision {
			continue
		}
		if item.Source != HealthEvidenceLiveTraffic && item.Source != HealthEvidenceProbe {
			return nil, errors.New("routing: transition evidence source is invalid")
		}
		if strings.TrimSpace(item.ID) == "" || item.OccurredAt.IsZero() {
			return nil, errors.New("routing: transition evidence identity and time are required")
		}
		if item.Outcome != HealthSuccess && item.Outcome != HealthFailure {
			return nil, errors.New("routing: transition evidence outcome is invalid")
		}
		if item.Outcome == HealthFailure {
			disposition := item.Failure.Disposition()
			if disposition.Scope != FailureScopeTarget || !disposition.PenalizeTarget {
				continue
			}
		}

		sequence := uint64(index + 1)
		startedAt := evidenceStartedAt(item)
		beforePermit := state.Phase
		next, permit, acquireErr := AcquirePermit(state, policy, revision, sequence, startedAt)
		if acquireErr != nil {
			return nil, acquireErr
		}
		state = next
		if beforePermit != state.Phase {
			transitions = append(transitions, CircuitTransition{
				ID: item.ID + ":permit", FromPhase: beforePermit, ToPhase: state.Phase,
				Trigger: HealthEvidenceTimer, Reason: "cooldown_elapsed", OccurredAt: startedAt,
			})
		}
		if !permit.Allowed {
			continue
		}

		beforeEvent := state.Phase
		event := HealthEvent{
			Revision: revision, Sequence: sequence, OccurredAt: item.OccurredAt,
			Outcome: item.Outcome, IncidentID: item.IncidentID, Failure: item.Failure,
		}
		next, result, applyErr := ApplyHealthEvent(state, policy, event)
		if applyErr != nil {
			return nil, applyErr
		}
		state = next
		if !result.Applied || beforeEvent == state.Phase {
			continue
		}
		transitions = append(transitions, CircuitTransition{
			ID: item.ID, FromPhase: beforeEvent, ToPhase: state.Phase,
			Trigger: item.Source, Reason: transitionReason(beforeEvent, state.Phase, item),
			FailureKind: item.Failure.Kind, RequestID: item.RequestID, OccurredAt: item.OccurredAt,
		})
	}
	return transitions, nil
}

func evidenceStartedAt(evidence HealthEvidence) time.Time {
	if evidence.StartedAt.IsZero() {
		return evidence.OccurredAt
	}
	return evidence.StartedAt
}

func transitionReason(from, to CircuitPhase, evidence HealthEvidence) string {
	switch {
	case evidence.Outcome == HealthSuccess && from == CircuitHalfOpen && to == CircuitClosed:
		return "recovery_succeeded"
	case evidence.Outcome == HealthSuccess:
		return "success_observed"
	case to == CircuitSuspect:
		return "target_failure_observed"
	case from == CircuitHalfOpen && to == CircuitOpen:
		return "recovery_failed"
	case to == CircuitOpen:
		return "failure_threshold_reached"
	default:
		return "health_state_changed"
	}
}
