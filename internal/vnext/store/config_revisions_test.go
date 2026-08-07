package store

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/runtimeconfig"
)

func TestConfigRevisionPollingRecoversMissedNotifications(t *testing.T) {
	ctx := context.Background()
	storage := newTestStore(t)
	bootstrap, err := storage.LatestConfigRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap.Revision != 1 || bootstrap.Cursor != 1 || bootstrap.Reason != "schema_bootstrap" {
		t.Fatalf("bootstrap revision = %+v", bootstrap)
	}
	now := time.Date(2026, time.August, 7, 13, 0, 0, 0, time.UTC)
	for _, reason := range []string{"site_updated", "route_updated"} {
		if _, err := storage.EnqueueConfigRevision(ctx, reason, now); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}

	// Starting from the last durable cursor recovers both events even though no
	// process was present to receive an in-memory notification.
	poller, err := runtimeconfig.NewPoller(runtimeconfig.PollerOptions{
		Repository: storage, Cursor: bootstrap.Cursor, BatchSize: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	var revisions []int64
	processed, err := poller.Poll(ctx, func(_ context.Context, event runtimeconfig.RevisionEvent) error {
		revisions = append(revisions, event.Revision)
		return nil
	})
	if err != nil || processed != 2 || len(revisions) != 2 || revisions[0] != 2 || revisions[1] != 3 {
		t.Fatalf("recovered revisions = %v, processed %d, error %v", revisions, processed, err)
	}
	if events, err := storage.PollConfigRevisions(ctx, poller.Cursor(), 10); err != nil || len(events) != 0 {
		t.Fatalf("events after acknowledged cursor = %+v, %v", events, err)
	}
	history, err := storage.ListConfigRevisions(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Revision != 3 || history[1].Revision != 2 {
		t.Fatalf("newest configuration history = %+v", history)
	}
}

func TestConfigRevisionTransactionRollsBackWithControlWrite(t *testing.T) {
	ctx := context.Background()
	storage := newTestStore(t)
	tx, err := storage.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runtime_settings
SET max_attempts=9,revision=revision+1 WHERE singleton_id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.EnqueueConfigRevisionTx(ctx, tx, "rolled_back", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	latest, err := storage.LatestConfigRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Revision != 1 || latest.Cursor != 1 {
		t.Fatalf("rolled-back revision became visible: %+v", latest)
	}
	settings, err := storage.GetRuntimeSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Revision != 1 || settings.MaxAttempts == 9 {
		t.Fatalf("rolled-back control write became visible: %+v", settings)
	}
}

func TestRuntimeSettingsWriteEnqueuesConfigRevisionInSameTransaction(t *testing.T) {
	ctx := context.Background()
	storage := newTestStore(t)
	input := DefaultRuntimeSettingsWrite()
	input.MaxAttempts = 7
	now := time.Date(2026, time.August, 7, 14, 0, 0, 0, time.UTC)
	if _, err := storage.UpdateRuntimeSettingsCAS(ctx, 1, input, now); err != nil {
		t.Fatal(err)
	}
	events, err := storage.PollConfigRevisions(ctx, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Revision != 2 || events[0].Reason != "runtime_settings_updated" ||
		!events[0].CreatedAt.Equal(now) {
		t.Fatalf("runtime settings revision event = %+v", events)
	}
}

func TestConfigRevisionsRemainStrictlyMonotonicUnderConcurrentWriters(t *testing.T) {
	ctx := context.Background()
	storage := newTestStore(t)
	const writers = 16
	revisions := make(chan int64, writers)
	errors := make(chan error, writers)
	var group sync.WaitGroup
	for index := 0; index < writers; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			event, err := storage.EnqueueConfigRevision(ctx, "concurrent_writer", time.Unix(int64(index+1), 0))
			if err != nil {
				errors <- err
				return
			}
			revisions <- event.Revision
		}(index)
	}
	group.Wait()
	close(revisions)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	got := make([]int64, 0, writers)
	for revision := range revisions {
		got = append(got, revision)
	}
	sort.Slice(got, func(left, right int) bool { return got[left] < got[right] })
	if len(got) != writers {
		t.Fatalf("revision count = %d, want %d", len(got), writers)
	}
	for index, revision := range got {
		want := int64(index + 2)
		if revision != want {
			t.Fatalf("revisions = %v; position %d = %d, want %d", got, index, revision, want)
		}
	}
}
