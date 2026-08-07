package capacity

import (
	"context"
	"io"
	"sync"
	"time"
)

// Permit owns one in-flight slot on a Site. Release is idempotent. A caller
// should normally defer Release immediately after Acquire; WrapReadCloser and
// ReleaseOnDone cover streaming and cancellation paths that outlive a stack
// frame.
type Permit struct {
	SiteID     SiteID
	TargetID   TargetID
	AcquiredAt time.Time
	QueuedFor  time.Duration
	Overflowed bool

	manager  *Manager
	once     sync.Once
	released chan struct{}
}

func (permit *Permit) Release() {
	if permit == nil {
		return
	}
	permit.once.Do(func() {
		if permit.manager != nil {
			permit.manager.release(permit.SiteID)
		}
		if permit.released != nil {
			close(permit.released)
		}
	})
}

// ReleaseOnDone releases the permit when ctx is canceled. The watcher also
// exits when Release is called, so a successful request does not leave a
// goroutine behind.
func (permit *Permit) ReleaseOnDone(ctx context.Context) {
	if permit == nil || ctx == nil {
		return
	}
	go func() {
		select {
		case <-ctx.Done():
			permit.Release()
		case <-permit.released:
		}
	}()
}

// WrapReadCloser keeps the permit through the complete upstream body or
// stream lifecycle and releases it on EOF, read failure, or Close.
func (permit *Permit) WrapReadCloser(body io.ReadCloser) io.ReadCloser {
	if permit == nil || body == nil {
		return body
	}
	return &permitReadCloser{body: body, permit: permit}
}

type permitReadCloser struct {
	body   io.ReadCloser
	permit *Permit
}

func (body *permitReadCloser) Read(destination []byte) (int, error) {
	count, err := body.body.Read(destination)
	if err != nil {
		body.permit.Release()
	}
	return count, err
}

func (body *permitReadCloser) Close() error {
	err := body.body.Close()
	body.permit.Release()
	return err
}
