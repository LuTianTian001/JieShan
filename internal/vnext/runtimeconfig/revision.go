package runtimeconfig

import (
	"context"
	"errors"
	"sync"
	"time"
)

type RevisionCursor int64

type RevisionEvent struct {
	Cursor    RevisionCursor
	Revision  int64
	Reason    string
	CreatedAt time.Time
}

// RevisionRepository is deliberately database-neutral. A PostgreSQL adapter
// can implement the same durable polling contract and add LISTEN/NOTIFY only as
// a wake-up optimization; callers always recover from the persisted cursor.
type RevisionRepository interface {
	LatestConfigRevision(context.Context) (RevisionEvent, error)
	PollConfigRevisions(context.Context, RevisionCursor, int) ([]RevisionEvent, error)
	EnqueueConfigRevision(context.Context, string, time.Time) (RevisionEvent, error)
}

type RevisionHandler func(context.Context, RevisionEvent) error

type PollerOptions struct {
	Repository RevisionRepository
	Cursor     RevisionCursor
	BatchSize  int
}

// Poller advances its cursor only after a handler succeeds. A process restart,
// a missed notification, or a handler error therefore cannot silently skip a
// durable configuration generation.
type Poller struct {
	repository RevisionRepository
	batchSize  int

	pollMu   sync.Mutex
	cursorMu sync.Mutex
	cursor   RevisionCursor
}

func NewPoller(options PollerOptions) (*Poller, error) {
	if options.Repository == nil {
		return nil, errors.New("runtime configuration revision repository is required")
	}
	if options.Cursor < 0 {
		return nil, errors.New("runtime configuration revision cursor cannot be negative")
	}
	if options.BatchSize == 0 {
		options.BatchSize = 100
	}
	if options.BatchSize < 1 || options.BatchSize > 1000 {
		return nil, errors.New("runtime configuration revision batch size must be between 1 and 1000")
	}
	return &Poller{repository: options.Repository, batchSize: options.BatchSize, cursor: options.Cursor}, nil
}

func (poller *Poller) Cursor() RevisionCursor {
	if poller == nil {
		return 0
	}
	poller.cursorMu.Lock()
	defer poller.cursorMu.Unlock()
	return poller.cursor
}

func (poller *Poller) Poll(ctx context.Context, handler RevisionHandler) (int, error) {
	if poller == nil || poller.repository == nil {
		return 0, errors.New("runtime configuration revision poller is unavailable")
	}
	if ctx == nil || handler == nil {
		return 0, errors.New("runtime configuration poll context and handler are required")
	}
	poller.pollMu.Lock()
	defer poller.pollMu.Unlock()
	after := poller.Cursor()
	events, err := poller.repository.PollConfigRevisions(ctx, after, poller.batchSize)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, event := range events {
		if event.Cursor <= after {
			return processed, errors.New("runtime configuration repository returned a non-increasing cursor")
		}
		if err := handler(ctx, event); err != nil {
			return processed, err
		}
		poller.cursorMu.Lock()
		poller.cursor = event.Cursor
		poller.cursorMu.Unlock()
		after = event.Cursor
		processed++
	}
	return processed, nil
}
