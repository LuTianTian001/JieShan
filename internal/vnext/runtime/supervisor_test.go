package runtime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunRestartsFailedBackgroundServiceWithoutStoppingRuntime(t *testing.T) {
	service := &flakyBackgroundService{ready: make(chan struct{})}
	runtime := testSupervisorRuntime(service)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()

	select {
	case <-service.ready:
	case <-time.After(time.Second):
		t.Fatal("background service was not restarted after transient failures")
	}
	select {
	case err := <-done:
		t.Fatalf("background failure stopped runtime: %v", err)
	default:
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runtime shutdown returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime did not stop after cancellation")
	}
	if attempts := service.attempts.Load(); attempts != 3 {
		t.Fatalf("background attempts = %d, want 3", attempts)
	}
}

func TestRunRecoversAndRestartsPanickingBackgroundService(t *testing.T) {
	service := &panickingBackgroundService{ready: make(chan struct{})}
	runtime := testSupervisorRuntime(service)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()

	select {
	case <-service.ready:
	case <-time.After(time.Second):
		t.Fatal("background service was not restarted after panic")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runtime shutdown returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime did not stop after cancellation")
	}
	if attempts := service.attempts.Load(); attempts != 2 {
		t.Fatalf("background attempts = %d, want 2", attempts)
	}
}

func testSupervisorRuntime(service BackgroundService) *Runtime {
	return &Runtime{
		background:           []namedBackgroundService{{name: "test service", service: service}},
		logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		backgroundRestartMin: time.Millisecond,
		backgroundRestartMax: 2 * time.Millisecond,
	}
}

type flakyBackgroundService struct {
	attempts atomic.Int32
	ready    chan struct{}
	once     sync.Once
}

func (service *flakyBackgroundService) Run(ctx context.Context) error {
	attempt := service.attempts.Add(1)
	if attempt < 3 {
		return errors.New("temporary background failure")
	}
	service.once.Do(func() { close(service.ready) })
	<-ctx.Done()
	return ctx.Err()
}

type panickingBackgroundService struct {
	attempts atomic.Int32
	ready    chan struct{}
	once     sync.Once
}

func (service *panickingBackgroundService) Run(ctx context.Context) error {
	if service.attempts.Add(1) == 1 {
		panic("temporary panic")
	}
	service.once.Do(func() { close(service.ready) })
	<-ctx.Done()
	return ctx.Err()
}
