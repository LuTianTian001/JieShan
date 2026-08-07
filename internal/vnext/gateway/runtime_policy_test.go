package gateway

import (
	"testing"
	"time"
)

func TestNormalizeRuntimePolicyKeepsRequestBoundariesCoherent(t *testing.T) {
	defaults := normalizeRuntimePolicy(RuntimePolicy{})
	if defaults.FirstOutputTimeout != 15*time.Second || defaults.HealthPolicy.FailureThreshold != 2 ||
		defaults.HealthPolicy.Cooldown != 5*time.Minute {
		t.Fatalf("gateway defaults = %+v", defaults)
	}

	policy := normalizeRuntimePolicy(RuntimePolicy{
		FirstOutputTimeout: 45 * time.Second,
		StreamIdleTimeout:  90 * time.Second,
		RequestTimeout:     20 * time.Second,
		MaxAttempts:        100,
	})
	if policy.RequestTimeout != 90*time.Second {
		t.Fatalf("request timeout = %s, want longest attempt boundary", policy.RequestTimeout)
	}
	if policy.MaxAttempts != 20 || policy.HealthPolicy.FailureThreshold != 2 {
		t.Fatalf("normalized policy = %+v", policy)
	}
}
