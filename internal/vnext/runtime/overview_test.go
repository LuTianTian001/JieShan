package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/capacity"
	"github.com/LuTianTian001/JieShan/internal/vnext/pricing"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

func TestRuntimeOverviewAggregatesRealRuntimeSources(t *testing.T) {
	ctx := context.Background()
	storage, err := vnextstore.Open(ctx, filepath.Join(t.TempDir(), "overview.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	now := time.Date(2026, time.August, 7, 15, 0, 0, 0, time.UTC)
	event, err := storage.EnqueueConfigRevision(ctx, "site_updated", now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	tracker := newBackgroundTaskTracker([]namedBackgroundService{{
		id: "model_monitoring", name: "model monitoring", label: "模型可用性监控", schedule: "每 15 分钟",
	}})
	tracker.started(namedBackgroundService{id: "model_monitoring"}, now.Add(-time.Minute))
	provider, err := newRuntimeOverviewProvider(
		now.Add(-time.Hour),
		storage,
		staticCapacitySnapshot{snapshot: capacity.Snapshot{
			Queued: 2,
			Sites: []capacity.SiteSnapshot{
				{SiteID: 1, InFlight: 2, MaxInFlight: 4},
				{SiteID: 2, InFlight: 1, MaxInFlight: 3},
			},
		}},
		staticAppliedConfigState{revision: event.Revision, loadedAt: now.Add(-30 * time.Second)},
		staticPriceState{state: pricing.CatalogState{ActiveVersion: "official-usd-test"}},
		tracker,
	)
	if err != nil {
		t.Fatal(err)
	}
	provider.now = func() time.Time { return now }

	overview, err := provider.RuntimeOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if overview.Runtime.InflightRequests != 3 || overview.Runtime.MaxConcurrency != 7 ||
		overview.Runtime.QueuedRequests != 2 || overview.Runtime.ConfigRevision != event.Revision ||
		overview.Runtime.ActivePriceCatalogVersion != "official-usd-test" || overview.Runtime.MeteringMode != "normal" {
		t.Fatalf("runtime snapshot = %+v", overview.Runtime)
	}
	if len(overview.BackgroundTasks) != 1 || overview.BackgroundTasks[0].State != "healthy" ||
		overview.BackgroundTasks[0].Label != "模型可用性监控" {
		t.Fatalf("background tasks = %+v", overview.BackgroundTasks)
	}
	if len(overview.ConfigHistory) != 2 || overview.ConfigHistory[0].Revision != event.Revision ||
		overview.ConfigHistory[0].Status != "applied" || overview.ConfigHistory[0].Summary != "上游站点配置已更新" {
		t.Fatalf("configuration history = %+v", overview.ConfigHistory)
	}
}

func TestBackgroundTaskTrackerReportsRestartState(t *testing.T) {
	now := time.Date(2026, time.August, 7, 16, 0, 0, 0, time.UTC)
	item := namedBackgroundService{id: "account_sync", name: "account sync", label: "上游账户同步"}
	tracker := newBackgroundTaskTracker([]namedBackgroundService{item})
	startedAt := now.Add(-2 * time.Second)
	tracker.started(item, startedAt)
	tracker.stopped(item, startedAt, now, errors.New("temporary failure"), now.Add(5*time.Second))

	snapshot := tracker.snapshot(now)
	if len(snapshot) != 1 || snapshot[0].State != "failed" ||
		snapshot[0].LastErrorCode != "background_service_failed" || snapshot[0].NextRunAt == nil ||
		snapshot[0].LastDurationMS == nil || *snapshot[0].LastDurationMS != 2_000 {
		t.Fatalf("background restart snapshot = %+v", snapshot)
	}
}

type staticCapacitySnapshot struct {
	snapshot capacity.Snapshot
}

func (provider staticCapacitySnapshot) Snapshot() capacity.Snapshot { return provider.snapshot }

type staticAppliedConfigState struct {
	revision int64
	loadedAt time.Time
}

func (provider staticAppliedConfigState) appliedState() (int64, time.Time) {
	return provider.revision, provider.loadedAt
}

type staticPriceState struct {
	state pricing.CatalogState
	err   error
}

func (provider staticPriceState) State(context.Context) (pricing.CatalogState, error) {
	return provider.state, provider.err
}
