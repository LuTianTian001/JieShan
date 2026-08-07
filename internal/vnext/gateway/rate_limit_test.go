package gateway

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryRateLimiterUsesRollingWindowAndKeyRevision(t *testing.T) {
	limiter := NewMemoryRateLimiter()
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	for index := 0; index < 2; index++ {
		decision, err := limiter.Allow(context.Background(), 7, 1, 2, now.Add(time.Duration(index)*time.Second))
		if err != nil || !decision.Allowed {
			t.Fatalf("request %d decision = %+v, error = %v", index, decision, err)
		}
	}
	decision, err := limiter.Allow(context.Background(), 7, 1, 2, now.Add(2*time.Second))
	if err != nil || decision.Allowed || decision.RetryAfter < 58*time.Second {
		t.Fatalf("limited decision = %+v, error = %v", decision, err)
	}
	decision, err = limiter.Allow(context.Background(), 7, 2, 2, now.Add(3*time.Second))
	if err != nil || !decision.Allowed {
		t.Fatalf("new key revision should reset the window: %+v, error = %v", decision, err)
	}
	decision, err = limiter.Allow(context.Background(), 7, 2, 2, now.Add(64*time.Second))
	if err != nil || !decision.Allowed {
		t.Fatalf("expired rolling-window entries should be removed: %+v, error = %v", decision, err)
	}
}

func TestRateLimitErrorSupportsStableClassification(t *testing.T) {
	err := &RateLimitError{RetryAfter: 17 * time.Second}
	if !errors.Is(err, ErrRateLimitExceeded) || RateLimitRetryAfter(err) != 17*time.Second {
		t.Fatalf("rate limit error = %v, retry = %s", err, RateLimitRetryAfter(err))
	}
}
