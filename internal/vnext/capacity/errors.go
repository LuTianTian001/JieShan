package capacity

import (
	"errors"
	"fmt"
	"time"
)

const UpstreamBusyCode = "upstream_busy"

var (
	ErrUpstreamBusy      = errors.New(UpstreamBusyCode)
	ErrClosed            = errors.New("capacity manager is closed")
	ErrSiteNotConfigured = errors.New("site capacity is not configured")
)

type BusyReason string

const (
	BusyQueueFull    BusyReason = "queue_full"
	BusyQueueTimeout BusyReason = "queue_timeout"
)

// BusyError is the stable gateway-facing classification for requests that
// could not obtain upstream capacity within the bounded admission policy.
type BusyError struct {
	Reason    BusyReason
	QueuedFor time.Duration
}

func (err *BusyError) Error() string {
	if err == nil {
		return ErrUpstreamBusy.Error()
	}
	if err.Reason == "" {
		return ErrUpstreamBusy.Error()
	}
	return fmt.Sprintf("%s: %s", ErrUpstreamBusy, err.Reason)
}

func (err *BusyError) Unwrap() error { return ErrUpstreamBusy }

func (err *BusyError) Code() string { return UpstreamBusyCode }

type SiteConfigError struct {
	SiteID SiteID
}

func (err *SiteConfigError) Error() string {
	if err == nil || err.SiteID <= 0 {
		return ErrSiteNotConfigured.Error()
	}
	return fmt.Sprintf("%s: site %d", ErrSiteNotConfigured, err.SiteID)
}

func (err *SiteConfigError) Unwrap() error { return ErrSiteNotConfigured }
