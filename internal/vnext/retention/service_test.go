package retention

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

func TestRunCleansImmediatelyAndReadsLatestRetentionEveryPass(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	clock := &retentionClock{now: now}
	settings := &retentionSettings{days: 30}
	repository := &retentionRepository{
		calls:            make(chan retentionCall, 8),
		maintenanceCalls: make(chan maintenanceCall, 8),
	}
	service, err := New(repository, settings, Options{
		Interval: 15 * time.Millisecond,
		Timeout:  100 * time.Millisecond,
		Now:      clock.Now,
		Logger:   discardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()

	first := awaitRetentionCall(t, repository.calls)
	if want := now.Add(-30 * 24 * time.Hour); !first.cutoff.Equal(want) {
		t.Fatalf("immediate cutoff = %v, want %v", first.cutoff, want)
	}
	assertBoundedRetentionContext(t, first, 100*time.Millisecond)
	assertMaintenanceContextActive(t, awaitMaintenanceCall(t, repository.maintenanceCalls), 100*time.Millisecond)

	settings.SetDays(7)
	nextNow := now.Add(2 * time.Hour)
	clock.Set(nextNow)
	second := awaitRetentionCall(t, repository.calls)
	if want := nextNow.Add(-7 * 24 * time.Hour); !second.cutoff.Equal(want) {
		t.Fatalf("dynamic cutoff = %v, want %v", second.cutoff, want)
	}
	assertBoundedRetentionContext(t, second, 100*time.Millisecond)
	assertMaintenanceContextActive(t, awaitMaintenanceCall(t, repository.maintenanceCalls), 100*time.Millisecond)

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("service shutdown error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("retention service did not stop")
	}
}

func TestRunContainsCleanupTimeoutsAndKeepsScheduling(t *testing.T) {
	repository := &retentionRepository{calls: make(chan retentionCall, 8), blockUntilDone: true}
	service, err := New(repository, &retentionSettings{days: 30}, Options{
		Interval: 5 * time.Millisecond,
		Timeout:  10 * time.Millisecond,
		Now:      func() time.Time { return time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC) },
		Logger:   discardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()

	first := awaitRetentionCall(t, repository.calls)
	assertBoundedRetentionContext(t, first, 10*time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("cleanup timeout stopped the service: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	second := awaitRetentionCall(t, repository.calls)
	assertBoundedRetentionContext(t, second, 10*time.Millisecond)

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("service shutdown error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("retention service did not stop after cancellation")
	}
}

func TestNewValidatesRetentionDependenciesAndBounds(t *testing.T) {
	repository := &retentionRepository{calls: make(chan retentionCall, 1)}
	settings := &retentionSettings{days: 30}
	tests := []struct {
		name       string
		repository Repository
		settings   SettingsProvider
		options    Options
	}{
		{name: "missing repository", settings: settings},
		{name: "missing settings", repository: repository},
		{name: "negative interval", repository: repository, settings: settings, options: Options{Interval: -time.Second}},
		{name: "negative timeout", repository: repository, settings: settings, options: Options{Timeout: -time.Second}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if service, err := New(test.repository, test.settings, test.options); err == nil {
				t.Fatalf("New unexpectedly returned service %+v", service)
			}
		})
	}
}

func TestNewUsesDailyCleanupAndBoundedDefaultTimeout(t *testing.T) {
	service, err := New(
		&retentionRepository{calls: make(chan retentionCall, 1)},
		&retentionSettings{days: 30},
		Options{Logger: discardLogger()},
	)
	if err != nil {
		t.Fatal(err)
	}
	if service.interval != 24*time.Hour || service.timeout != time.Minute {
		t.Fatalf("retention defaults = interval %v timeout %v", service.interval, service.timeout)
	}
}

type retentionCall struct {
	cutoff      time.Time
	observedAt  time.Time
	deadline    time.Time
	hasDeadline bool
}

type maintenanceCall struct {
	observedAt  time.Time
	deadline    time.Time
	hasDeadline bool
	err         error
}

type retentionRepository struct {
	calls            chan retentionCall
	maintenanceCalls chan maintenanceCall
	blockUntilDone   bool
	mu               sync.Mutex
	maintenance      int
}

func (repository *retentionRepository) PruneOperationalHistory(
	ctx context.Context,
	cutoff time.Time,
) (vnextstore.RetentionCleanupResult, error) {
	observedAt := time.Now()
	deadline, hasDeadline := ctx.Deadline()
	repository.calls <- retentionCall{cutoff: cutoff, observedAt: observedAt, deadline: deadline, hasDeadline: hasDeadline}
	if repository.blockUntilDone {
		<-ctx.Done()
		return vnextstore.RetentionCleanupResult{}, ctx.Err()
	}
	return vnextstore.RetentionCleanupResult{CutoffAt: cutoff}, nil
}

func (repository *retentionRepository) Maintain(ctx context.Context) error {
	observedAt := time.Now()
	deadline, hasDeadline := ctx.Deadline()
	repository.mu.Lock()
	repository.maintenance++
	repository.mu.Unlock()
	if repository.maintenanceCalls != nil {
		repository.maintenanceCalls <- maintenanceCall{
			observedAt: observedAt, deadline: deadline, hasDeadline: hasDeadline, err: ctx.Err(),
		}
	}
	return nil
}

type retentionSettings struct {
	mu   sync.RWMutex
	days int
}

func (settings *retentionSettings) Current() vnextstore.RuntimeSettings {
	settings.mu.RLock()
	defer settings.mu.RUnlock()
	return vnextstore.RuntimeSettings{LogRetentionDays: settings.days}
}

func (settings *retentionSettings) SetDays(days int) {
	settings.mu.Lock()
	settings.days = days
	settings.mu.Unlock()
}

type retentionClock struct {
	mu  sync.RWMutex
	now time.Time
}

func (clock *retentionClock) Now() time.Time {
	clock.mu.RLock()
	defer clock.mu.RUnlock()
	return clock.now
}

func (clock *retentionClock) Set(now time.Time) {
	clock.mu.Lock()
	clock.now = now
	clock.mu.Unlock()
}

func awaitRetentionCall(t *testing.T, calls <-chan retentionCall) retentionCall {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(time.Second):
		t.Fatal("retention cleanup was not called")
		return retentionCall{}
	}
}

func awaitMaintenanceCall(t *testing.T, calls <-chan maintenanceCall) maintenanceCall {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(time.Second):
		t.Fatal("SQLite maintenance was not called")
		return maintenanceCall{}
	}
}

func assertMaintenanceContextActive(t *testing.T, call maintenanceCall, maximum time.Duration) {
	t.Helper()
	if call.err != nil {
		t.Fatalf("SQLite maintenance context error = %v", call.err)
	}
	if !call.hasDeadline {
		t.Fatal("SQLite maintenance context has no deadline")
	}
	duration := call.deadline.Sub(call.observedAt)
	if duration <= 0 || duration > maximum {
		t.Fatalf("SQLite maintenance deadline duration = %v, want within (0,%v]", duration, maximum)
	}
}

func assertBoundedRetentionContext(t *testing.T, call retentionCall, maximum time.Duration) {
	t.Helper()
	if !call.hasDeadline {
		t.Fatal("retention cleanup context has no deadline")
	}
	duration := call.deadline.Sub(call.observedAt)
	if duration <= 0 || duration > maximum {
		t.Fatalf("retention cleanup deadline duration = %v, want within (0,%v]", duration, maximum)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
