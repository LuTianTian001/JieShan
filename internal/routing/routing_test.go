package routing

import "testing"

func TestOrderedEligibleUsesOnlyUserOrder(t *testing.T) {
	targets := []Target{
		{ID: 30, Position: 2, Enabled: true, CredentialState: "active"},
		{ID: 10, Position: 0, Enabled: true, CredentialState: "active"},
		{ID: 20, Position: 1, Enabled: true, CredentialState: "active"},
	}
	got := OrderedEligible(targets, 1_000)
	if len(got) != 3 || got[0].ID != 10 || got[1].ID != 20 || got[2].ID != 30 {
		t.Fatalf("unexpected order: %+v", got)
	}
}

func TestOrderedEligibleSkipsCoolingAndIsolatedTargets(t *testing.T) {
	targets := []Target{
		{ID: 1, Position: 0, Enabled: true, CircuitPhase: "open", CooldownUntil: 2_000, CredentialState: "active"},
		{ID: 2, Position: 1, Enabled: true, CredentialState: "invalid"},
		{ID: 3, Position: 2, Enabled: true, CredentialState: "active", CapabilityState: "unsupported"},
		{ID: 4, Position: 3, Enabled: true, CredentialState: "active"},
	}
	got := OrderedEligible(targets, 1_000)
	if len(got) != 1 || got[0].ID != 4 {
		t.Fatalf("unexpected eligible targets: %+v", got)
	}
}

func TestOrderedEligibleSkipsDisabledTargetImmediately(t *testing.T) {
	got := OrderedEligible([]Target{
		{ID: 1, Position: 0, Enabled: false, CredentialState: "active"},
		{ID: 2, Position: 1, Enabled: true, CredentialState: "active"},
	}, 1_000)
	if len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("disabled target was not skipped: %+v", got)
	}
}
