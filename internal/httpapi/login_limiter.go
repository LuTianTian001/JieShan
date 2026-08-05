package httpapi

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	loginLimiterMaxEntries = 1024
	loginLimiterIdleTTL    = 15 * time.Minute
	loginBackoffBase       = time.Second
	loginBackoffMax        = time.Minute
)

type loginFailureState struct {
	failures     int
	blockedUntil time.Time
	lastSeen     time.Time
}

type loginFailureLimiter struct {
	mu         sync.Mutex
	entries    map[string]loginFailureState
	maxEntries int
}

func newLoginFailureLimiter(maxEntries int) *loginFailureLimiter {
	if maxEntries < 1 {
		maxEntries = loginLimiterMaxEntries
	}
	return &loginFailureLimiter{entries: make(map[string]loginFailureState), maxEntries: maxEntries}
}

func (l *loginFailureLimiter) allow(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	state, ok := l.entries[key]
	if !ok || !now.Before(state.blockedUntil) {
		return true, 0
	}
	return false, state.blockedUntil.Sub(now)
}

func (l *loginFailureLimiter) failure(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
	if _, exists := l.entries[key]; !exists && len(l.entries) >= l.maxEntries {
		l.evictOldestLocked()
	}
	state := l.entries[key]
	state.failures++
	state.lastSeen = now
	if state.failures > 1 {
		shift := state.failures - 2
		if shift > 6 {
			shift = 6
		}
		delay := loginBackoffBase * time.Duration(1<<shift)
		if delay > loginBackoffMax {
			delay = loginBackoffMax
		}
		state.blockedUntil = now.Add(delay)
	}
	l.entries[key] = state
}

func (l *loginFailureLimiter) success(key string) {
	l.mu.Lock()
	delete(l.entries, key)
	l.mu.Unlock()
}

func (l *loginFailureLimiter) pruneLocked(now time.Time) {
	for key, state := range l.entries {
		if now.Sub(state.lastSeen) >= loginLimiterIdleTTL {
			delete(l.entries, key)
		}
	}
}

func (l *loginFailureLimiter) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	for key, state := range l.entries {
		if oldestKey == "" || state.lastSeen.Before(oldest) {
			oldestKey = key
			oldest = state.lastSeen
		}
	}
	if oldestKey != "" {
		delete(l.entries, oldestKey)
	}
}

func loginClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); net.ParseIP(forwarded) != nil {
			return forwarded
		}
		if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); net.ParseIP(realIP) != nil {
			return realIP
		}
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && net.ParseIP(host) != nil {
		return host
	}
	if parsed := net.ParseIP(strings.TrimSpace(r.RemoteAddr)); parsed != nil {
		return parsed.String()
	}
	return "unknown"
}
