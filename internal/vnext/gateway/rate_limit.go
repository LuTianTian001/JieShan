package gateway

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrRateLimitExceeded = errors.New("downstream key request rate exceeded")

type RateLimitDecision struct {
	Allowed    bool
	RetryAfter time.Duration
}

type RateLimiter interface {
	Allow(context.Context, int64, int64, int, time.Time) (RateLimitDecision, error)
}

type RateLimitError struct {
	RetryAfter time.Duration
}

func (err *RateLimitError) Error() string { return ErrRateLimitExceeded.Error() }

func (err *RateLimitError) Is(target error) bool { return target == ErrRateLimitExceeded }

func RateLimitRetryAfter(err error) time.Duration {
	var limited *RateLimitError
	if errors.As(err, &limited) && limited.RetryAfter > 0 {
		return limited.RetryAfter
	}
	return 0
}

type rateWindow struct {
	revision   int64
	timestamps []time.Time
	lastSeen   time.Time
}

// MemoryRateLimiter implements an exact rolling one-minute window. RPM is an
// operational guardrail, so it deliberately resets after a process restart;
// quota accounting remains durable in SQLite.
type MemoryRateLimiter struct {
	mu      sync.Mutex
	windows map[int64]rateWindow
	calls   uint64
}

func NewMemoryRateLimiter() *MemoryRateLimiter {
	return &MemoryRateLimiter{windows: make(map[int64]rateWindow)}
}

func (limiter *MemoryRateLimiter) Allow(
	ctx context.Context,
	keyID int64,
	revision int64,
	limit int,
	now time.Time,
) (RateLimitDecision, error) {
	if err := ctx.Err(); err != nil {
		return RateLimitDecision{}, err
	}
	if limit <= 0 {
		return RateLimitDecision{Allowed: true}, nil
	}
	if limiter == nil || keyID <= 0 || revision <= 0 {
		return RateLimitDecision{}, errors.New("invalid downstream rate limit identity")
	}
	now = now.UTC()
	cutoff := now.Add(-time.Minute)

	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	window := limiter.windows[keyID]
	if window.revision != revision {
		window = rateWindow{revision: revision}
	}
	firstActive := 0
	for firstActive < len(window.timestamps) && !window.timestamps[firstActive].After(cutoff) {
		firstActive++
	}
	if firstActive > 0 {
		window.timestamps = append(window.timestamps[:0], window.timestamps[firstActive:]...)
	}
	window.lastSeen = now
	if len(window.timestamps) >= limit {
		retryAfter := window.timestamps[0].Add(time.Minute).Sub(now)
		if retryAfter < time.Second {
			retryAfter = time.Second
		}
		limiter.windows[keyID] = window
		return RateLimitDecision{RetryAfter: retryAfter}, nil
	}
	window.timestamps = append(window.timestamps, now)
	limiter.windows[keyID] = window
	limiter.calls++
	if limiter.calls%256 == 0 {
		limiter.removeStale(now.Add(-10 * time.Minute))
	}
	return RateLimitDecision{Allowed: true}, nil
}

func (limiter *MemoryRateLimiter) removeStale(cutoff time.Time) {
	for keyID, window := range limiter.windows {
		if window.lastSeen.Before(cutoff) {
			delete(limiter.windows, keyID)
		}
	}
}
