package settings

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

func TestServiceAtomicallyPublishesGatewayAndMonitoringPolicy(t *testing.T) {
	ctx := context.Background()
	storage, err := vnextstore.Open(ctx, filepath.Join(t.TempDir(), "jieshan.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	now := time.Date(2026, 8, 6, 11, 0, 0, 0, time.UTC)
	service, err := NewService(ctx, storage, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}

	initial := service.Snapshot()
	if initial.HealthPolicy.FailureThreshold != 2 || initial.HealthPolicy.Cooldown != 15*time.Minute ||
		initial.FirstOutputTimeout != 15*time.Second || initial.StreamIdleTimeout != time.Minute ||
		initial.RequestTimeout != 5*time.Minute || initial.MaxAttempts != 4 {
		t.Fatalf("initial gateway snapshot = %+v", initial)
	}
	if monitor := service.MonitoringSnapshot(); monitor.ProbeInterval != 15*time.Minute ||
		monitor.HealthPolicy != initial.HealthPolicy || monitor.FirstOutputTimeout != 15*time.Second {
		t.Fatalf("initial monitor snapshot = %+v", monitor)
	}

	write := vnextstore.DefaultRuntimeSettingsWrite()
	write.FailureThreshold = 4
	write.Cooldown = 90 * time.Second
	write.ProbeInterval = 3 * time.Minute
	write.FirstOutputTimeout = 10 * time.Second
	write.StreamIdleTimeout = 20 * time.Second
	write.RequestTimeout = 2 * time.Minute
	write.MaxAttempts = 7
	write.LogRetentionDays = 60
	now = now.Add(time.Minute)

	var readers sync.WaitGroup
	for index := 0; index < 16; index++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for iteration := 0; iteration < 100; iteration++ {
				snapshot := service.Snapshot()
				if snapshot.HealthPolicy.FailureThreshold != 2 && snapshot.HealthPolicy.FailureThreshold != 4 {
					t.Errorf("observed partial snapshot: %+v", snapshot)
					return
				}
			}
		}()
	}
	updated, err := service.UpdateCAS(ctx, 1, write)
	if err != nil {
		t.Fatal(err)
	}
	readers.Wait()
	if updated.Revision != 2 || service.Current().Revision != 2 {
		t.Fatalf("updated/current revisions = %d/%d", updated.Revision, service.Current().Revision)
	}
	policy := service.Snapshot()
	if policy.HealthPolicy.FailureThreshold != 4 || policy.HealthPolicy.Cooldown != 90*time.Second ||
		policy.FirstOutputTimeout != 10*time.Second || policy.MaxAttempts != 7 {
		t.Fatalf("updated gateway snapshot = %+v", policy)
	}
	if monitor := service.MonitoringSnapshot(); monitor.ProbeInterval != 3*time.Minute ||
		monitor.HealthPolicy.FailureThreshold != 4 || monitor.FirstOutputTimeout != 10*time.Second {
		t.Fatalf("updated monitor snapshot = %+v", monitor)
	}
	if _, err := service.UpdateCAS(ctx, 1, write); !errors.Is(err, vnextstore.ErrRevisionConflict) {
		t.Fatalf("stale service update error = %v", err)
	}
}

func TestServiceBootstrapCannotOverwritePersistedSettings(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "jieshan.sqlite")
	storage, err := vnextstore.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	first, err := NewService(ctx, storage, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	write := vnextstore.DefaultRuntimeSettingsWrite()
	write.Cooldown = 7 * time.Minute
	now = now.Add(time.Minute)
	if _, err := first.UpdateCAS(ctx, 1, write); err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := vnextstore.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	differentBootstrap := vnextstore.DefaultRuntimeSettingsWrite()
	differentBootstrap.Cooldown = time.Minute
	second, err := NewService(ctx, reopened, Options{
		Initial: differentBootstrap, Now: func() time.Time { return now.Add(time.Minute) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if current := second.Current(); current.Revision != 2 || current.Cooldown != 7*time.Minute {
		t.Fatalf("persisted settings were overwritten by bootstrap: %+v", current)
	}
}
