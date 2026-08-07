package routing

import (
	"strings"
	"testing"
	"time"
)

func TestCompilePlanUsesStrictUserPosition(t *testing.T) {
	plan, err := CompilePlan([]Target{
		{ID: 30, Revision: 1, Position: 20, Enabled: true, Credentials: []Credential{{ID: 300, Position: 0, Enabled: true}}},
		{ID: 10, Revision: 1, Position: 0, Enabled: true, Credentials: []Credential{{ID: 101, Position: 1, Enabled: true}, {ID: 100, Position: 0, Enabled: true}}},
		{ID: 20, Revision: 1, Position: 10, Enabled: true, Credentials: []Credential{{ID: 200, Position: 0, Enabled: true}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	targets := plan.Targets()
	if len(targets) != 3 || targets[0].ID != 10 || targets[1].ID != 20 || targets[2].ID != 30 {
		t.Fatalf("targets not ordered strictly by position: %+v", targets)
	}
	if targets[0].Credentials[0].ID != 100 || targets[0].Credentials[1].ID != 101 {
		t.Fatalf("credentials not ordered strictly by position: %+v", targets[0].Credentials)
	}
}

func TestCompilePlanRejectsAmbiguousPositions(t *testing.T) {
	_, err := CompilePlan([]Target{
		{ID: 1, Revision: 1, Position: 0},
		{ID: 2, Revision: 1, Position: 0},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate target position") {
		t.Fatalf("duplicate target positions accepted: %v", err)
	}

	_, err = CompilePlan([]Target{{
		ID: 1, Revision: 1, Position: 0,
		Credentials: []Credential{{ID: 1, Position: 0}, {ID: 2, Position: 0}},
	}})
	if err == nil || !strings.Contains(err.Error(), "duplicate credential position") {
		t.Fatalf("duplicate credential positions accepted: %v", err)
	}
}

func TestRetryableTargetFailureImmediatelyMovesToNextTarget(t *testing.T) {
	plan := mustPlan(t, []Target{
		{ID: 1, Revision: 1, Position: 0, Enabled: true, Credentials: []Credential{{ID: 11, Position: 0, Enabled: true}, {ID: 12, Position: 1, Enabled: true}}},
		{ID: 2, Revision: 1, Position: 1, Enabled: true, Credentials: []Credential{{ID: 21, Position: 0, Enabled: true}}},
	})
	cursor := plan.NewCursor(nil, time.Now())
	first, ok := cursor.First()
	if !ok || first.Target.ID != 1 || first.Credential.ID != 11 {
		t.Fatalf("unexpected first candidate: %+v ok=%v", first, ok)
	}
	next, ok := cursor.Advance(Failure{Kind: FailureTransport})
	if !ok || next.Target.ID != 2 || next.Credential.ID != 21 {
		t.Fatalf("target failure did not switch immediately: %+v ok=%v", next, ok)
	}
}

func TestCursorUsesStreamCommitBoundaryForFailover(t *testing.T) {
	plan := mustPlan(t, []Target{
		{ID: 1, Revision: 1, Position: 0, Enabled: true, Credentials: []Credential{{ID: 11, Position: 0, Enabled: true}}},
		{ID: 2, Revision: 1, Position: 1, Enabled: true, Credentials: []Credential{{ID: 21, Position: 0, Enabled: true}}},
	})

	beforeCommit := plan.NewCursor(nil, time.Now())
	_, _ = beforeCommit.First()
	next, ok := beforeCommit.Advance(Failure{Kind: FailureStreamTruncated})
	if !ok || next.Target.ID != 2 {
		t.Fatalf("pre-commit truncation did not fail over: next=%+v ok=%v", next, ok)
	}

	afterCommit := plan.NewCursor(nil, time.Now())
	_, _ = afterCommit.First()
	if next, ok := afterCommit.Advance(Failure{Kind: FailureStreamTruncated, ResponseCommitted: true}); ok {
		t.Fatalf("committed truncation retried another target: %+v", next)
	}
}

func TestCredentialFailureRotatesLocallyBeforeNextTarget(t *testing.T) {
	plan := mustPlan(t, []Target{
		{ID: 1, Revision: 1, Position: 0, Enabled: true, Credentials: []Credential{{ID: 11, Position: 0, Enabled: true}, {ID: 12, Position: 1, Enabled: true}}},
		{ID: 2, Revision: 1, Position: 1, Enabled: true, Credentials: []Credential{{ID: 21, Position: 0, Enabled: true}}},
	})
	cursor := plan.NewCursor(nil, time.Now())
	first, _ := cursor.First()
	second, ok := cursor.Advance(Failure{Kind: FailureCredentialAuth})
	if !ok || first.Target.ID != second.Target.ID || second.Credential.ID != 12 {
		t.Fatalf("credential failure did not rotate within target: first=%+v second=%+v", first, second)
	}
	third, ok := cursor.Advance(Failure{Kind: FailureCredentialRateLimited})
	if !ok || third.Target.ID != 2 || third.Credential.ID != 21 {
		t.Fatalf("exhausted credential list did not move to next target: %+v", third)
	}
}

func TestFailureVocabularyKeepsCredentialAndTargetOwnershipSeparate(t *testing.T) {
	tests := []struct {
		kind             FailureKind
		scope            FailureScope
		retry            RetryStep
		penalizeTarget   bool
		credentialEffect CredentialEffect
	}{
		{FailureCredentialAuth, FailureScopeCredential, RetryNextCredential, false, CredentialEffectInvalidate},
		{FailureCredentialPermission, FailureScopeCredential, RetryNextCredential, false, CredentialEffectDenyTargetAccess},
		{FailureCredentialPaymentRequired, FailureScopeCredential, RetryNextCredential, false, CredentialEffectExhaust},
		{FailureCredentialRateLimited, FailureScopeCredential, RetryNextCredential, false, CredentialEffectCooldown},
		{FailureTransport, FailureScopeTarget, RetryNextTarget, true, CredentialEffectNone},
		{FailureUpstreamTransient, FailureScopeTarget, RetryNextTarget, true, CredentialEffectNone},
		{FailureClientInvalid, FailureScopeRequest, RetryStop, false, CredentialEffectNone},
		{FailureDownstreamCanceled, FailureScopeRequest, RetryStop, false, CredentialEffectNone},
	}
	for _, test := range tests {
		t.Run(string(test.kind), func(t *testing.T) {
			disposition := (Failure{Kind: test.kind}).Disposition()
			if disposition.Scope != test.scope || disposition.Retry != test.retry ||
				disposition.PenalizeTarget != test.penalizeTarget || disposition.CredentialEffect != test.credentialEffect {
				t.Fatalf("disposition = %+v", disposition)
			}
		})
	}
}

func TestCredentialFailureDoesNotChangeTargetHealth(t *testing.T) {
	policy := DefaultHealthPolicy()
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	state, _ := NewHealthState(1)
	got, result, err := ApplyHealthEvent(state, policy, HealthEvent{
		Revision: 1, Sequence: 1, OccurredAt: now, Outcome: HealthFailure,
		Failure: Failure{Kind: FailureCredentialAuth},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.Reason != ApplyNonTargetFailure || got != state {
		t.Fatalf("credential failure poisoned target health: result=%+v got=%+v", result, got)
	}
}

func TestCursorSkipsCoolingTargetWithoutRerankingOthers(t *testing.T) {
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	plan := mustPlan(t, []Target{
		{ID: 1, Revision: 1, Position: 0, Enabled: true, Credentials: []Credential{{ID: 11, Position: 0, Enabled: true}}},
		{ID: 2, Revision: 1, Position: 1, Enabled: true, Credentials: []Credential{{ID: 21, Position: 0, Enabled: true}}},
		{ID: 3, Revision: 1, Position: 2, Enabled: true, Credentials: []Credential{{ID: 31, Position: 0, Enabled: true}}},
	})
	health := map[TargetID]HealthState{
		1: {Revision: 1, Phase: CircuitOpen, CooldownUntil: now.Add(time.Minute)},
	}
	cursor := plan.NewCursor(health, now)
	first, ok := cursor.First()
	if !ok || first.Target.ID != 2 {
		t.Fatalf("expected next user-position target after cooling target: %+v", first)
	}
	second, ok := cursor.Advance(Failure{Kind: FailureTransport})
	if !ok || second.Target.ID != 3 {
		t.Fatalf("remaining targets were reranked: %+v", second)
	}
}

func TestCursorSkipsUnavailableCredentials(t *testing.T) {
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	plan := mustPlan(t, []Target{{
		ID: 1, Revision: 1, Position: 0, Enabled: true,
		Credentials: []Credential{
			{ID: 11, Position: 0, Enabled: true, State: CredentialInvalid},
			{ID: 12, Position: 1, Enabled: true, State: CredentialCooling, CooldownUntil: now.Add(time.Minute)},
			{ID: 13, Position: 2, Enabled: true, State: CredentialReady},
		},
	}})
	candidate, ok := plan.NewCursor(nil, now).First()
	if !ok || candidate.Credential.ID != 13 {
		t.Fatalf("cursor selected unavailable credential: %+v ok=%v", candidate, ok)
	}
}

func TestNewTargetRevisionIgnoresOldCoolingState(t *testing.T) {
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	plan := mustPlan(t, []Target{{
		ID: 1, Revision: 2, Position: 0, Enabled: true,
		Credentials: []Credential{{ID: 11, Position: 0, Enabled: true}},
	}})
	health := map[TargetID]HealthState{
		1: {Revision: 1, Phase: CircuitOpen, CooldownUntil: now.Add(time.Hour)},
	}
	candidate, ok := plan.NewCursor(health, now).First()
	if !ok || candidate.Target.ID != 1 || candidate.PermitMode != PermitNormal {
		t.Fatalf("new revision did not invalidate old cooling state: %+v ok=%v", candidate, ok)
	}
}

func mustPlan(t *testing.T, targets []Target) Plan {
	t.Helper()
	plan, err := CompilePlan(targets)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
