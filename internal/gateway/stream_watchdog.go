package gateway

import (
	"sync"
	"time"
)

type streamTimeoutPhase string

const (
	streamTimeoutFirstOutput streamTimeoutPhase = "first_output"
	streamTimeoutIdle        streamTimeoutPhase = "stream_idle"
)

// streamWatchdog owns the request cancellation timer while a streaming
// attempt waits for semantic output and, after that, while the stream is idle.
type streamWatchdog struct {
	mu          sync.Mutex
	timer       *time.Timer
	cancel      func()
	idleTimeout time.Duration
	phase       streamTimeoutPhase
	fired       streamTimeoutPhase
	stopped     bool
}

func newStreamWatchdog(firstOutputTimeout, idleTimeout time.Duration, cancel func()) *streamWatchdog {
	watchdog := &streamWatchdog{
		cancel:      cancel,
		idleTimeout: idleTimeout,
		phase:       streamTimeoutFirstOutput,
	}
	watchdog.timer = time.AfterFunc(firstOutputTimeout, watchdog.expire)
	return watchdog
}

func (w *streamWatchdog) semanticOutput() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped || w.fired != "" {
		return
	}
	w.phase = streamTimeoutIdle
	w.timer.Reset(w.idleTimeout)
}

func (w *streamWatchdog) activity() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped || w.fired != "" || w.phase != streamTimeoutIdle {
		return
	}
	w.timer.Reset(w.idleTimeout)
}

func (w *streamWatchdog) stop() streamTimeoutPhase {
	if w == nil {
		return ""
	}
	w.mu.Lock()
	w.stopped = true
	w.timer.Stop()
	fired := w.fired
	w.mu.Unlock()
	return fired
}

func (w *streamWatchdog) expire() {
	w.mu.Lock()
	if w.stopped || w.fired != "" {
		w.mu.Unlock()
		return
	}
	w.fired = w.phase
	cancel := w.cancel
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
