// Package capacity owns in-memory upstream Site admission. Limits are shared
// across every downstream key, model, endpoint, credential, and wire protocol
// that resolves to the same Site.
package capacity

import (
	"context"
	"time"
)

const (
	MinPreferredTargetGrace = 100 * time.Millisecond
	MaxPreferredTargetGrace = 200 * time.Millisecond
)

type SiteID int64
type TargetID int64
type KeyID int64

type Config struct {
	MaxQueued            int
	QueueTimeout         time.Duration
	PreferredTargetGrace time.Duration
}

func DefaultConfig() Config {
	return Config{
		MaxQueued:            1024,
		QueueTimeout:         5 * time.Second,
		PreferredTargetGrace: 150 * time.Millisecond,
	}
}

type SiteConfig struct {
	SiteID      SiteID
	MaxInFlight int
}

// Candidate order is authoritative. Capacity never re-ranks candidates.
// The caller must exclude targets that are disabled, unhealthy, or otherwise
// ineligible before asking for admission.
type Candidate struct {
	TargetID TargetID
	SiteID   SiteID
}

type Request struct {
	KeyID      KeyID
	Candidates []Candidate
}

// Acquirer is the narrow data-plane integration boundary.
type Acquirer interface {
	Acquire(context.Context, Request) (*Permit, error)
}

// Controller is the runtime/control-plane boundary. ReplaceSites is atomic;
// permits already in flight remain valid and drain against their original Site.
type Controller interface {
	Acquirer
	ReplaceSites([]SiteConfig) error
	ReportThrottle(ThrottleSignal) error
	ClearThrottle(SiteID) error
	Snapshot() Snapshot
}

// ThrottleSignal is intentionally separate from routing health. It represents
// a Site-level concurrency/rate response such as an upstream 429 Retry-After;
// it must not open or extend a target circuit cooldown.
type ThrottleSignal struct {
	SiteID     SiteID
	ObservedAt time.Time
	RetryAfter time.Duration
	Until      time.Time
}

type Snapshot struct {
	UpdatedAt int64          `json:"updatedAt"`
	Queued    int            `json:"queuedRequests"`
	Sites     []SiteSnapshot `json:"sites"`
}

// SiteSnapshot.Queued counts each queued request once when that request could
// use the Site, even if multiple ordered targets in the request share it.
// Consequently, Site queued counts are demand gauges and are not additive
// across Sites; Snapshot.Queued is the authoritative global queue depth.
type SiteSnapshot struct {
	SiteID         SiteID    `json:"siteId"`
	InFlight       int       `json:"inflightRequests"`
	MaxInFlight    int       `json:"maxConcurrency"`
	Queued         int       `json:"queuedRequests"`
	ThrottledUntil time.Time `json:"throttledUntil,omitempty"`
}
