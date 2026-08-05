package httpapi

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLoginFailureLimiterBackoffAndReset(t *testing.T) {
	limiter := newLoginFailureLimiter(8)
	now := time.Unix(1_700_000_000, 0)
	key := "192.0.2.1|admin"

	limiter.failure(key, now)
	if allowed, _ := limiter.allow(key, now); !allowed {
		t.Fatal("a single typo should not block the next login")
	}
	limiter.failure(key, now)
	if allowed, retry := limiter.allow(key, now); allowed || retry != time.Second {
		t.Fatalf("second failure should block for one second: allowed=%v retry=%v", allowed, retry)
	}
	if allowed, _ := limiter.allow(key, now.Add(time.Second)); !allowed {
		t.Fatal("login should be allowed after the backoff expires")
	}
	limiter.failure(key, now.Add(time.Second))
	if allowed, retry := limiter.allow(key, now.Add(time.Second)); allowed || retry != 2*time.Second {
		t.Fatalf("third failure should double the backoff: allowed=%v retry=%v", allowed, retry)
	}
	limiter.success(key)
	if allowed, _ := limiter.allow(key, now); !allowed {
		t.Fatal("successful login should clear failure state")
	}
}

func TestLoginFailureLimiterIsBounded(t *testing.T) {
	limiter := newLoginFailureLimiter(4)
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < 20; i++ {
		limiter.failure(fmt.Sprintf("192.0.2.%d|admin", i), now.Add(time.Duration(i)*time.Second))
	}
	if got := len(limiter.entries); got > 4 {
		t.Fatalf("limiter retained %d entries, want at most 4", got)
	}
}

func TestLoginClientIPHonorsTrustedProxyOnly(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	req.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.1")
	if got := loginClientIP(req, false); got != "192.0.2.10" {
		t.Fatalf("untrusted proxy header changed client IP: %q", got)
	}
	if got := loginClientIP(req, true); got != "198.51.100.7" {
		t.Fatalf("trusted proxy client IP = %q", got)
	}
}
