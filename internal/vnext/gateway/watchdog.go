package gateway

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
)

type attemptWatchdog struct {
	mu                 sync.Mutex
	cancel             context.CancelCauseFunc
	firstOutputTimeout time.Duration
	streamIdleTimeout  time.Duration
	firstOutputSeen    bool
	stopped            bool
	generation         uint64
	timer              *time.Timer
}

func newAttemptWatchdog(
	cancel context.CancelCauseFunc,
	firstOutputTimeout time.Duration,
	streamIdleTimeout time.Duration,
) *attemptWatchdog {
	watchdog := &attemptWatchdog{
		cancel: cancel, firstOutputTimeout: firstOutputTimeout, streamIdleTimeout: streamIdleTimeout,
	}
	watchdog.mu.Lock()
	watchdog.scheduleLocked(firstOutputTimeout, ErrFirstOutputTimeout)
	watchdog.mu.Unlock()
	return watchdog
}

func (watchdog *attemptWatchdog) ObserveBodyRead(bytesRead int) {
	if watchdog == nil || bytesRead <= 0 {
		return
	}
	watchdog.mu.Lock()
	defer watchdog.mu.Unlock()
	if watchdog.stopped || watchdog.firstOutputSeen {
		return
	}
	watchdog.firstOutputSeen = true
	watchdog.stopTimerLocked()
}

func (watchdog *attemptWatchdog) ObserveStreamEvent(semantic bool) {
	if watchdog == nil {
		return
	}
	watchdog.mu.Lock()
	defer watchdog.mu.Unlock()
	if watchdog.stopped {
		return
	}
	if !watchdog.firstOutputSeen {
		if !semantic {
			return
		}
		watchdog.firstOutputSeen = true
	}
	watchdog.scheduleLocked(watchdog.streamIdleTimeout, ErrStreamIdleTimeout)
}

func (watchdog *attemptWatchdog) Stop() {
	if watchdog == nil {
		return
	}
	watchdog.mu.Lock()
	defer watchdog.mu.Unlock()
	if watchdog.stopped {
		return
	}
	watchdog.stopped = true
	watchdog.stopTimerLocked()
}

func (watchdog *attemptWatchdog) scheduleLocked(duration time.Duration, cause error) {
	watchdog.stopTimerLocked()
	if watchdog.stopped || duration <= 0 {
		return
	}
	generation := watchdog.generation
	watchdog.timer = time.AfterFunc(duration, func() {
		watchdog.mu.Lock()
		if watchdog.stopped || watchdog.generation != generation {
			watchdog.mu.Unlock()
			return
		}
		watchdog.stopped = true
		cancel := watchdog.cancel
		watchdog.mu.Unlock()
		cancel(cause)
	})
}

func (watchdog *attemptWatchdog) stopTimerLocked() {
	watchdog.generation++
	if watchdog.timer != nil {
		watchdog.timer.Stop()
		watchdog.timer = nil
	}
}

type observedReader struct {
	reader  interface{ Read([]byte) (int, error) }
	observe func(int)
}

func (reader *observedReader) Read(buffer []byte) (int, error) {
	count, err := reader.reader.Read(buffer)
	if count > 0 && reader.observe != nil {
		reader.observe(count)
	}
	return count, err
}

func requestContextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, ErrRequestTimeout):
		return ErrRequestTimeout
	case errors.Is(cause, context.Canceled), errors.Is(cause, context.DeadlineExceeded):
		return ErrDownstreamClosed
	default:
		return nil
	}
}

func contextFailure(ctx context.Context, committed bool) (routing.Failure, string, error, bool) {
	if ctx == nil {
		return routing.Failure{}, "", nil, false
	}
	cause := context.Cause(ctx)
	switch {
	case errors.Is(cause, ErrFirstOutputTimeout):
		return routing.Failure{Kind: routing.FailureFirstOutputTimeout}, "first_output_timeout", nil, true
	case errors.Is(cause, ErrStreamIdleTimeout):
		return routing.Failure{Kind: routing.FailureStreamIdleTimeout, ResponseCommitted: committed},
			"stream_idle_timeout", ErrStreamIdleTimeout, true
	case errors.Is(cause, ErrRequestTimeout):
		return routing.Failure{Kind: routing.FailureRequestTimeout, ResponseCommitted: committed},
			"request_timeout", ErrRequestTimeout, true
	case errors.Is(cause, context.Canceled), errors.Is(cause, context.DeadlineExceeded):
		return routing.Failure{Kind: routing.FailureDownstreamCanceled, ResponseCommitted: committed},
			"downstream_canceled", ErrDownstreamClosed, true
	default:
		return routing.Failure{}, "", nil, false
	}
}
