package gateway

import (
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
)

const (
	defaultFirstOutputTimeout = 15 * time.Second
	defaultStreamIdleTimeout  = 60 * time.Second
	defaultRequestTimeout     = 5 * time.Minute
	defaultMaxAttempts        = 4
)

// RuntimePolicy is sampled once per downstream request. A request therefore
// keeps one coherent routing and timeout policy even if an administrator saves
// new settings while it is in flight.
type RuntimePolicy struct {
	HealthPolicy       routing.HealthPolicy
	FirstOutputTimeout time.Duration
	StreamIdleTimeout  time.Duration
	RequestTimeout     time.Duration
	MaxAttempts        int
}

type RuntimePolicyProvider interface {
	Snapshot() RuntimePolicy
}

type StaticRuntimePolicyProvider struct {
	Policy RuntimePolicy
}

func (provider StaticRuntimePolicyProvider) Snapshot() RuntimePolicy {
	return provider.Policy
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
	if policy.FirstOutputTimeout <= 0 {
		policy.FirstOutputTimeout = defaultFirstOutputTimeout
	}
	if policy.StreamIdleTimeout <= 0 {
		policy.StreamIdleTimeout = defaultStreamIdleTimeout
	}
	if policy.RequestTimeout <= 0 {
		policy.RequestTimeout = defaultRequestTimeout
	}
	minimumRequestTimeout := max(policy.FirstOutputTimeout, policy.StreamIdleTimeout)
	if policy.RequestTimeout < minimumRequestTimeout {
		policy.RequestTimeout = minimumRequestTimeout
	}
	if policy.MaxAttempts <= 0 {
		policy.MaxAttempts = defaultMaxAttempts
	}
	if policy.MaxAttempts > 20 {
		policy.MaxAttempts = 20
	}
	return policy
}
