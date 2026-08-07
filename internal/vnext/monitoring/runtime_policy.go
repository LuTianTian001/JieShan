package monitoring

import (
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
)

// RuntimePolicy is the administrator-controlled portion of monitoring policy.
// A scheduler samples it once per target probe so permit acquisition and the
// resulting health event use one coherent failure policy.
type RuntimePolicy struct {
	HealthPolicy  routing.HealthPolicy
	ProbeInterval time.Duration
}

type RuntimePolicyProvider interface {
	MonitoringSnapshot() RuntimePolicy
}

type StaticRuntimePolicyProvider struct {
	Policy RuntimePolicy
}

func (provider StaticRuntimePolicyProvider) MonitoringSnapshot() RuntimePolicy {
	return normalizeRuntimePolicy(provider.Policy)
}

func normalizeRuntimePolicy(policy RuntimePolicy) RuntimePolicy {
	defaults := routing.DefaultHealthPolicy()
	if policy.HealthPolicy.FailureThreshold < 2 {
		policy.HealthPolicy.FailureThreshold = defaults.FailureThreshold
	}
	if policy.HealthPolicy.FailureWindow <= 0 {
		policy.HealthPolicy.FailureWindow = defaults.FailureWindow
	}
	if policy.HealthPolicy.Cooldown <= 0 {
		policy.HealthPolicy.Cooldown = defaults.Cooldown
	}
	if policy.HealthPolicy.HalfOpenLease <= 0 {
		policy.HealthPolicy.HalfOpenLease = defaults.HalfOpenLease
	}
	if policy.ProbeInterval <= 0 {
		policy.ProbeInterval = DefaultProbeInterval
	}
	return policy
}
