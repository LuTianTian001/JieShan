package gateway

import (
	"testing"
	"time"
)

func TestStreamWatchdogTransitionsToIdleAndResetsOnActivity(t *testing.T) {
	cancelled := make(chan struct{}, 1)
	watchdog := newStreamWatchdog(80*time.Millisecond, 35*time.Millisecond, func() { cancelled <- struct{}{} })
	defer watchdog.stop()

	time.Sleep(15 * time.Millisecond)
	watchdog.semanticOutput()
	time.Sleep(20 * time.Millisecond)
	watchdog.activity()

	select {
	case <-cancelled:
		t.Fatal("watchdog expired before the reset idle timeout")
	case <-time.After(20 * time.Millisecond):
	}
	select {
	case <-cancelled:
		if phase := watchdog.stop(); phase != streamTimeoutIdle {
			t.Fatalf("timeout phase = %q, want %q", phase, streamTimeoutIdle)
		}
	case <-time.After(40 * time.Millisecond):
		t.Fatal("watchdog did not expire after stream idle timeout")
	}
}

func TestStreamWatchdogReportsFirstOutputTimeout(t *testing.T) {
	cancelled := make(chan struct{}, 1)
	watchdog := newStreamWatchdog(20*time.Millisecond, time.Second, func() { cancelled <- struct{}{} })
	select {
	case <-cancelled:
		if phase := watchdog.stop(); phase != streamTimeoutFirstOutput {
			t.Fatalf("timeout phase = %q, want %q", phase, streamTimeoutFirstOutput)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("watchdog did not expire while waiting for first output")
	}
}
