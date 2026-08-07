package adminauth

import (
	"sync"
	"time"
)

type loginLimiter struct {
	mu          sync.Mutex
	maxFailures int
	window      time.Duration
	lockout     time.Duration
	attempts    map[string]loginAttempt
}

type loginAttempt struct {
	failures    int
	windowStart time.Time
	lockedUntil time.Time
	lastSeen    time.Time
}

func newLoginLimiter(maxFailures int, window, lockout time.Duration) *loginLimiter {
	return &loginLimiter{
		maxFailures: maxFailures,
		window:      window,
		lockout:     lockout,
		attempts:    make(map[string]loginAttempt),
	}
}

func (limiter *loginLimiter) before(key string, now time.Time) time.Duration {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	limiter.cleanup(now)
	attempt, exists := limiter.attempts[key]
	if !exists || !attempt.lockedUntil.After(now) {
		return 0
	}
	attempt.lastSeen = now
	limiter.attempts[key] = attempt
	return attempt.lockedUntil.Sub(now)
}

func (limiter *loginLimiter) failure(key string, now time.Time) time.Duration {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	limiter.cleanup(now)
	attempt := limiter.attempts[key]
	if attempt.windowStart.IsZero() || now.Sub(attempt.windowStart) >= limiter.window {
		attempt.failures = 0
		attempt.windowStart = now
	}
	attempt.failures++
	attempt.lastSeen = now
	if attempt.failures >= limiter.maxFailures {
		attempt.lockedUntil = now.Add(limiter.lockout)
	}
	limiter.attempts[key] = attempt
	if attempt.lockedUntil.After(now) {
		return attempt.lockedUntil.Sub(now)
	}
	return 0
}

func (limiter *loginLimiter) success(key string) {
	limiter.mu.Lock()
	delete(limiter.attempts, key)
	limiter.mu.Unlock()
}

func (limiter *loginLimiter) cleanup(now time.Time) {
	retention := limiter.window + limiter.lockout
	for key, attempt := range limiter.attempts {
		if !attempt.lastSeen.IsZero() && now.Sub(attempt.lastSeen) > retention {
			delete(limiter.attempts, key)
		}
	}
}
