package routing

import "sort"

type Target struct {
	ID              int64
	Position        int
	Enabled         bool
	CircuitPhase    string
	CooldownUntil   int64
	CredentialState string
	CapabilityState string
}

func OrderedEligible(targets []Target, nowMS int64) []Target {
	ordered := append([]Target(nil), targets...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Position == ordered[j].Position {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].Position < ordered[j].Position
	})
	result := make([]Target, 0, len(ordered))
	for _, target := range ordered {
		if !target.Enabled || target.CredentialState == "invalid" || target.CredentialState == "revoked" || target.CapabilityState == "unsupported" {
			continue
		}
		if target.CircuitPhase == "open" && target.CooldownUntil > nowMS {
			continue
		}
		result = append(result, target)
	}
	return result
}
