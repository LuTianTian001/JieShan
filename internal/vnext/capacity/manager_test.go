package capacity

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

type acquireResult struct {
	permit *Permit
	err    error
}

func TestCapacityIsSharedBySiteAcrossTargetsAndKeys(t *testing.T) {
	manager := newTestManager(t, Config{MaxQueued: 8, QueueTimeout: time.Second})
	configureSites(t, manager, SiteConfig{SiteID: 1, MaxInFlight: 1})

	first := mustAcquire(t, manager, 10, Candidate{TargetID: 101, SiteID: 1})
	second := acquireAsync(manager, 20, Candidate{TargetID: 202, SiteID: 1})
	waitForQueued(t, manager, 1)
	assertSnapshot(t, manager, 1, SiteSnapshot{SiteID: 1, InFlight: 1, MaxInFlight: 1, Queued: 1})

	first.Release()
	granted := receiveAcquire(t, second)
	if granted.err != nil || granted.permit.TargetID != 202 {
		t.Fatalf("second Site-shared permit = %+v, %v", granted.permit, granted.err)
	}
	granted.permit.Release()
	assertSnapshot(t, manager, 0, SiteSnapshot{SiteID: 1, InFlight: 0, MaxInFlight: 1})
}

func TestUnconfiguredCandidateDoesNotBlockLaterConfiguredSite(t *testing.T) {
	manager := newTestManager(t, Config{MaxQueued: 2, QueueTimeout: time.Second})
	configureSites(t, manager, SiteConfig{SiteID: 2, MaxInFlight: 1})

	permit := mustAcquire(t, manager, 1,
		Candidate{TargetID: 10, SiteID: 1},
		Candidate{TargetID: 20, SiteID: 2},
	)
	defer permit.Release()
	if permit.SiteID != 2 || permit.TargetID != 20 || !permit.Overflowed {
		t.Fatalf("fallback permit = %+v", permit)
	}
}

func TestQueuedRequestDropsRemovedSiteAndUsesConfiguredFallback(t *testing.T) {
	manager := newTestManager(t, Config{MaxQueued: 2, QueueTimeout: time.Second})
	configureSites(t, manager,
		SiteConfig{SiteID: 1, MaxInFlight: 1},
		SiteConfig{SiteID: 2, MaxInFlight: 1},
	)
	first := mustAcquire(t, manager, 1, Candidate{TargetID: 1, SiteID: 1})
	second := mustAcquire(t, manager, 2, Candidate{TargetID: 2, SiteID: 2})
	queued := acquireAsync(manager, 3,
		Candidate{TargetID: 11, SiteID: 1},
		Candidate{TargetID: 22, SiteID: 2},
	)
	waitForQueued(t, manager, 1)

	configureSites(t, manager, SiteConfig{SiteID: 2, MaxInFlight: 1})
	second.Release()
	granted := receiveAcquire(t, queued)
	if granted.err != nil || granted.permit.SiteID != 2 || granted.permit.TargetID != 22 {
		t.Fatalf("fallback after removal = %+v, %v", granted.permit, granted.err)
	}
	granted.permit.Release()
	first.Release()
}

func TestRemovingPreferredSiteEndsGraceAndUsesFallback(t *testing.T) {
	manager := newTestManager(t, Config{
		MaxQueued: 2, QueueTimeout: time.Second, PreferredTargetGrace: MaxPreferredTargetGrace,
	})
	configureSites(t, manager,
		SiteConfig{SiteID: 1, MaxInFlight: 1},
		SiteConfig{SiteID: 2, MaxInFlight: 1},
	)
	holder := mustAcquire(t, manager, 1, Candidate{TargetID: 1, SiteID: 1})
	queued := acquireAsync(manager, 2,
		Candidate{TargetID: 11, SiteID: 1},
		Candidate{TargetID: 22, SiteID: 2},
	)
	waitForQueued(t, manager, 1)

	configureSites(t, manager, SiteConfig{SiteID: 2, MaxInFlight: 1})
	snapshot := manager.Snapshot()
	if snapshot.Queued != 0 || len(snapshot.Sites) != 2 || snapshot.Sites[1].SiteID != 2 || snapshot.Sites[1].InFlight != 1 {
		t.Fatalf("fallback was not granted when preferred Site was removed: %+v", snapshot)
	}
	granted := receiveAcquire(t, queued)
	if granted.err != nil || granted.permit.SiteID != 2 || granted.permit.TargetID != 22 {
		t.Fatalf("fallback after preferred removal = %+v, %v", granted.permit, granted.err)
	}
	granted.permit.Release()
	holder.Release()
}

func TestLoweredLimitWaitsForExistingPermitsToDrain(t *testing.T) {
	manager := newTestManager(t, Config{MaxQueued: 2, QueueTimeout: time.Second})
	configureSites(t, manager, SiteConfig{SiteID: 1, MaxInFlight: 2})
	first := mustAcquire(t, manager, 1, Candidate{TargetID: 1, SiteID: 1})
	second := mustAcquire(t, manager, 2, Candidate{TargetID: 2, SiteID: 1})
	configureSites(t, manager, SiteConfig{SiteID: 1, MaxInFlight: 1})
	queued := acquireAsync(manager, 3, Candidate{TargetID: 3, SiteID: 1})
	waitForQueued(t, manager, 1)

	first.Release()
	select {
	case early := <-queued:
		if early.permit != nil {
			early.permit.Release()
		}
		t.Fatalf("lowered limit granted before in-flight drained: %+v", early)
	case <-time.After(20 * time.Millisecond):
	}
	second.Release()
	granted := receiveAcquire(t, queued)
	if granted.err != nil || granted.permit.TargetID != 3 {
		t.Fatalf("post-drain permit = %+v, %v", granted.permit, granted.err)
	}
	granted.permit.Release()
}

func TestPreferredTargetGraceThenStrictOverflow(t *testing.T) {
	manager := newTestManager(t, Config{
		MaxQueued: 8, QueueTimeout: time.Second, PreferredTargetGrace: MinPreferredTargetGrace,
	})
	configureSites(t, manager,
		SiteConfig{SiteID: 1, MaxInFlight: 1},
		SiteConfig{SiteID: 2, MaxInFlight: 1},
	)
	holder := mustAcquire(t, manager, 1, Candidate{TargetID: 1, SiteID: 1})
	started := time.Now()
	result := acquireAsync(manager, 2,
		Candidate{TargetID: 11, SiteID: 1},
		Candidate{TargetID: 22, SiteID: 2},
	)

	select {
	case early := <-result:
		if early.permit != nil {
			early.permit.Release()
		}
		t.Fatalf("overflow bypassed preferred-target grace: %+v", early)
	case <-time.After(60 * time.Millisecond):
	}
	granted := receiveAcquire(t, result)
	if granted.err != nil {
		t.Fatal(granted.err)
	}
	if granted.permit.TargetID != 22 || granted.permit.SiteID != 2 || !granted.permit.Overflowed {
		t.Fatalf("overflow permit = %+v", granted.permit)
	}
	if waited := time.Since(started); waited < 80*time.Millisecond {
		t.Fatalf("overflow waited %s, want preferred grace", waited)
	}
	granted.permit.Release()
	holder.Release()
}

func TestPreferredTargetReleasedDuringGraceKeepsFirstRoute(t *testing.T) {
	manager := newTestManager(t, Config{
		MaxQueued: 8, QueueTimeout: time.Second, PreferredTargetGrace: MinPreferredTargetGrace,
	})
	configureSites(t, manager,
		SiteConfig{SiteID: 1, MaxInFlight: 1},
		SiteConfig{SiteID: 2, MaxInFlight: 1},
	)
	holder := mustAcquire(t, manager, 1, Candidate{TargetID: 1, SiteID: 1})
	result := acquireAsync(manager, 2,
		Candidate{TargetID: 11, SiteID: 1},
		Candidate{TargetID: 22, SiteID: 2},
	)
	waitForQueued(t, manager, 1)
	holder.Release()
	granted := receiveAcquire(t, result)
	if granted.err != nil || granted.permit.TargetID != 11 || granted.permit.Overflowed {
		t.Fatalf("preferred permit = %+v, %v", granted.permit, granted.err)
	}
	granted.permit.Release()
}

func TestFairQueueRotatesByDownstreamKey(t *testing.T) {
	manager := newTestManager(t, Config{MaxQueued: 8, QueueTimeout: 2 * time.Second})
	configureSites(t, manager, SiteConfig{SiteID: 1, MaxInFlight: 1})
	holder := mustAcquire(t, manager, 99, Candidate{TargetID: 1, SiteID: 1})

	keyAFirst := acquireAsync(manager, 10, Candidate{TargetID: 101, SiteID: 1})
	waitForQueued(t, manager, 1)
	keyASecond := acquireAsync(manager, 10, Candidate{TargetID: 102, SiteID: 1})
	waitForQueued(t, manager, 2)
	keyB := acquireAsync(manager, 20, Candidate{TargetID: 201, SiteID: 1})
	waitForQueued(t, manager, 3)

	holder.Release()
	first := receiveAcquire(t, keyAFirst)
	if first.err != nil || first.permit.TargetID != 101 {
		t.Fatalf("first fair grant = %+v, %v", first.permit, first.err)
	}
	first.permit.Release()
	second := receiveAcquire(t, keyB)
	if second.err != nil || second.permit.TargetID != 201 {
		t.Fatalf("second fair grant = %+v, %v", second.permit, second.err)
	}
	second.permit.Release()
	third := receiveAcquire(t, keyASecond)
	if third.err != nil || third.permit.TargetID != 102 {
		t.Fatalf("third fair grant = %+v, %v", third.permit, third.err)
	}
	third.permit.Release()
}

func TestQueuedDemandDoesNotBlockAnUnrelatedIdleSite(t *testing.T) {
	manager := newTestManager(t, Config{MaxQueued: 1, QueueTimeout: time.Second})
	configureSites(t, manager,
		SiteConfig{SiteID: 1, MaxInFlight: 1},
		SiteConfig{SiteID: 2, MaxInFlight: 1},
	)
	holder := mustAcquire(t, manager, 99, Candidate{TargetID: 1, SiteID: 1})
	queued := acquireAsync(manager, 10, Candidate{TargetID: 101, SiteID: 1})
	waitForQueued(t, manager, 1)

	permit := mustAcquire(t, manager, 20, Candidate{TargetID: 202, SiteID: 2})
	if permit.SiteID != 2 || permit.TargetID != 202 {
		t.Fatalf("unrelated idle Site permit = %+v", permit)
	}
	permit.Release()

	holder.Release()
	granted := receiveAcquire(t, queued)
	if granted.err != nil || granted.permit.TargetID != 101 {
		t.Fatalf("original queued permit = %+v, %v", granted.permit, granted.err)
	}
	granted.permit.Release()
}

func TestQueueTimeoutReturnsTypedUpstreamBusyWithoutLeak(t *testing.T) {
	manager := newTestManager(t, Config{MaxQueued: 2, QueueTimeout: 40 * time.Millisecond})
	configureSites(t, manager, SiteConfig{SiteID: 1, MaxInFlight: 1})
	holder := mustAcquire(t, manager, 1, Candidate{TargetID: 1, SiteID: 1})

	_, err := manager.Acquire(context.Background(), Request{
		KeyID: 2, Candidates: []Candidate{{TargetID: 2, SiteID: 1}},
	})
	var busy *BusyError
	if !errors.Is(err, ErrUpstreamBusy) || !errors.As(err, &busy) || busy.Code() != UpstreamBusyCode || busy.Reason != BusyQueueTimeout {
		t.Fatalf("timeout error = %#v", err)
	}
	if busy.QueuedFor < 20*time.Millisecond {
		t.Fatalf("reported queue duration = %s", busy.QueuedFor)
	}
	assertSnapshot(t, manager, 0, SiteSnapshot{SiteID: 1, InFlight: 1, MaxInFlight: 1})
	holder.Release()
}

func TestQueueIsBounded(t *testing.T) {
	manager := newTestManager(t, Config{MaxQueued: 1, QueueTimeout: time.Second})
	configureSites(t, manager, SiteConfig{SiteID: 1, MaxInFlight: 1})
	holder := mustAcquire(t, manager, 1, Candidate{TargetID: 1, SiteID: 1})
	queued := acquireAsync(manager, 2, Candidate{TargetID: 2, SiteID: 1})
	waitForQueued(t, manager, 1)

	_, err := manager.Acquire(context.Background(), Request{
		KeyID: 3, Candidates: []Candidate{{TargetID: 3, SiteID: 1}},
	})
	var busy *BusyError
	if !errors.As(err, &busy) || busy.Reason != BusyQueueFull {
		t.Fatalf("full queue error = %#v", err)
	}
	holder.Release()
	granted := receiveAcquire(t, queued)
	if granted.err != nil {
		t.Fatal(granted.err)
	}
	granted.permit.Release()
}

func TestCanceledWaiterIsRemoved(t *testing.T) {
	manager := newTestManager(t, Config{MaxQueued: 2, QueueTimeout: time.Second})
	configureSites(t, manager, SiteConfig{SiteID: 1, MaxInFlight: 1})
	holder := mustAcquire(t, manager, 1, Candidate{TargetID: 1, SiteID: 1})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan acquireResult, 1)
	go func() {
		permit, err := manager.Acquire(ctx, Request{KeyID: 2, Candidates: []Candidate{{TargetID: 2, SiteID: 1}}})
		result <- acquireResult{permit: permit, err: err}
	}()
	waitForQueued(t, manager, 1)
	cancel()
	canceled := receiveAcquire(t, result)
	if !errors.Is(canceled.err, context.Canceled) || canceled.permit != nil {
		t.Fatalf("canceled acquisition = %+v, %v", canceled.permit, canceled.err)
	}
	assertSnapshot(t, manager, 0, SiteSnapshot{SiteID: 1, InFlight: 1, MaxInFlight: 1})
	holder.Release()
}

func TestPermitReleaseIsIdempotentAndStreamSafe(t *testing.T) {
	(&Permit{}).Release()

	manager := newTestManager(t, Config{})
	configureSites(t, manager, SiteConfig{SiteID: 1, MaxInFlight: 1})
	permit := mustAcquire(t, manager, 1, Candidate{TargetID: 1, SiteID: 1})
	body := permit.WrapReadCloser(io.NopCloser(bytes.NewBufferString("stream")))
	value, err := io.ReadAll(body)
	if err != nil || string(value) != "stream" {
		t.Fatalf("wrapped stream = %q, %v", value, err)
	}
	permit.Release()
	permit.Release()
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	assertSnapshot(t, manager, 0, SiteSnapshot{SiteID: 1, InFlight: 0, MaxInFlight: 1})

	permit = mustAcquire(t, manager, 2, Candidate{TargetID: 2, SiteID: 1})
	ctx, cancel := context.WithCancel(context.Background())
	permit.ReleaseOnDone(ctx)
	cancel()
	waitForInflight(t, manager, 0)
}

func TestThrottleIsSeparateOrderedSignalAndLatestConcurrentDeadlineWins(t *testing.T) {
	manager := newTestManager(t, Config{MaxQueued: 8, QueueTimeout: time.Second, PreferredTargetGrace: MinPreferredTargetGrace})
	configureSites(t, manager,
		SiteConfig{SiteID: 2, MaxInFlight: 1},
		SiteConfig{SiteID: 1, MaxInFlight: 1},
	)
	now := time.Now().UTC()
	var wait sync.WaitGroup
	for index := 1; index <= 8; index++ {
		wait.Add(1)
		go func(delay time.Duration) {
			defer wait.Done()
			if err := manager.ReportThrottle(ThrottleSignal{SiteID: 1, ObservedAt: now, RetryAfter: delay}); err != nil {
				t.Errorf("ReportThrottle: %v", err)
			}
		}(time.Duration(index) * 25 * time.Millisecond)
	}
	wait.Wait()
	snapshot := manager.Snapshot()
	if len(snapshot.Sites) != 2 || snapshot.Sites[0].SiteID != 1 || snapshot.Sites[1].SiteID != 2 {
		t.Fatalf("stable Site ordering = %+v", snapshot.Sites)
	}
	if snapshot.Sites[0].ThrottledUntil.Before(now.Add(190 * time.Millisecond)) {
		t.Fatalf("merged throttle deadline = %s", snapshot.Sites[0].ThrottledUntil)
	}

	permit := mustAcquire(t, manager, 9,
		Candidate{TargetID: 11, SiteID: 1},
		Candidate{TargetID: 22, SiteID: 2},
	)
	if permit.TargetID != 22 || permit.SiteID != 2 || !permit.Overflowed {
		t.Fatalf("throttled route selection = %+v", permit)
	}
	permit.Release()
	if err := manager.ClearThrottle(1); err != nil {
		t.Fatal(err)
	}
	permit = mustAcquire(t, manager, 10,
		Candidate{TargetID: 11, SiteID: 1},
		Candidate{TargetID: 22, SiteID: 2},
	)
	if permit.TargetID != 11 || permit.Overflowed {
		t.Fatalf("cleared throttle selection = %+v", permit)
	}
	permit.Release()
}

func TestThrottleExpiryRetriesQueuedSelection(t *testing.T) {
	manager := newTestManager(t, Config{MaxQueued: 2, QueueTimeout: time.Second})
	configureSites(t, manager, SiteConfig{SiteID: 1, MaxInFlight: 1})
	if err := manager.ReportThrottle(ThrottleSignal{SiteID: 1, RetryAfter: 40 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	permit := mustAcquire(t, manager, 1, Candidate{TargetID: 1, SiteID: 1})
	if waited := time.Since(started); waited < 20*time.Millisecond {
		t.Fatalf("throttled acquisition waited only %s", waited)
	}
	permit.Release()
}

func TestSnapshotCountsQueuedDemandOncePerSite(t *testing.T) {
	manager := newTestManager(t, Config{MaxQueued: 4, QueueTimeout: time.Second})
	configureSites(t, manager,
		SiteConfig{SiteID: 2, MaxInFlight: 1},
		SiteConfig{SiteID: 1, MaxInFlight: 1},
	)
	first := mustAcquire(t, manager, 1, Candidate{TargetID: 1, SiteID: 1})
	second := mustAcquire(t, manager, 2, Candidate{TargetID: 2, SiteID: 2})
	queued := acquireAsync(manager, 3,
		Candidate{TargetID: 11, SiteID: 1},
		Candidate{TargetID: 12, SiteID: 1},
		Candidate{TargetID: 21, SiteID: 2},
	)
	waitForQueued(t, manager, 1)
	snapshot := manager.Snapshot()
	if len(snapshot.Sites) != 2 || snapshot.Sites[0].Queued != 1 || snapshot.Sites[1].Queued != 1 {
		t.Fatalf("queued demand snapshot = %+v", snapshot)
	}
	first.Release()
	granted := receiveAcquire(t, queued)
	if granted.err != nil {
		t.Fatal(granted.err)
	}
	granted.permit.Release()
	second.Release()
}

func TestRemovedSiteDrainsPermitAndRejectsQueuedWork(t *testing.T) {
	manager := newTestManager(t, Config{MaxQueued: 2, QueueTimeout: time.Second})
	configureSites(t, manager, SiteConfig{SiteID: 1, MaxInFlight: 1})
	holder := mustAcquire(t, manager, 1, Candidate{TargetID: 1, SiteID: 1})
	queued := acquireAsync(manager, 2, Candidate{TargetID: 2, SiteID: 1})
	waitForQueued(t, manager, 1)
	if err := manager.ReplaceSites(nil); err != nil {
		t.Fatal(err)
	}
	result := receiveAcquire(t, queued)
	if !errors.Is(result.err, ErrSiteNotConfigured) || result.permit != nil {
		t.Fatalf("removed Site acquisition = %+v, %v", result.permit, result.err)
	}
	snapshot := manager.Snapshot()
	if len(snapshot.Sites) != 1 || snapshot.Sites[0].InFlight != 1 {
		t.Fatalf("draining Site snapshot = %+v", snapshot)
	}
	holder.Release()
	if snapshot := manager.Snapshot(); len(snapshot.Sites) != 0 {
		t.Fatalf("drained removed Site remains = %+v", snapshot)
	}
}

func TestCloseUnblocksQueuedRequests(t *testing.T) {
	manager := newTestManager(t, Config{MaxQueued: 2, QueueTimeout: time.Second})
	configureSites(t, manager, SiteConfig{SiteID: 1, MaxInFlight: 1})
	holder := mustAcquire(t, manager, 1, Candidate{TargetID: 1, SiteID: 1})
	queued := acquireAsync(manager, 2, Candidate{TargetID: 2, SiteID: 1})
	waitForQueued(t, manager, 1)
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	result := receiveAcquire(t, queued)
	if !errors.Is(result.err, ErrClosed) || result.permit != nil {
		t.Fatalf("closed acquisition = %+v, %v", result.permit, result.err)
	}
	holder.Release()
}

func newTestManager(t *testing.T, config Config) *Manager {
	t.Helper()
	manager, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func configureSites(t *testing.T, manager *Manager, configs ...SiteConfig) {
	t.Helper()
	if err := manager.ReplaceSites(configs); err != nil {
		t.Fatal(err)
	}
}

func mustAcquire(t *testing.T, manager *Manager, keyID KeyID, candidates ...Candidate) *Permit {
	t.Helper()
	permit, err := manager.Acquire(context.Background(), Request{KeyID: keyID, Candidates: candidates})
	if err != nil {
		t.Fatal(err)
	}
	return permit
}

func acquireAsync(manager *Manager, keyID KeyID, candidates ...Candidate) <-chan acquireResult {
	result := make(chan acquireResult, 1)
	go func() {
		permit, err := manager.Acquire(context.Background(), Request{KeyID: keyID, Candidates: candidates})
		result <- acquireResult{permit: permit, err: err}
	}()
	return result
}

func receiveAcquire(t *testing.T, result <-chan acquireResult) acquireResult {
	t.Helper()
	select {
	case received := <-result:
		return received
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for acquisition")
		return acquireResult{}
	}
}

func waitForQueued(t *testing.T, manager *Manager, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if manager.Snapshot().Queued == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queued = %d, want %d", manager.Snapshot().Queued, want)
}

func waitForInflight(t *testing.T, manager *Manager, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot := manager.Snapshot()
		if len(snapshot.Sites) == 1 && snapshot.Sites[0].InFlight == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("snapshot = %+v, want inflight %d", manager.Snapshot(), want)
}

func assertSnapshot(t *testing.T, manager *Manager, queued int, want SiteSnapshot) {
	t.Helper()
	snapshot := manager.Snapshot()
	if snapshot.Queued != queued || len(snapshot.Sites) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	got := snapshot.Sites[0]
	if got.SiteID != want.SiteID || got.InFlight != want.InFlight || got.MaxInFlight != want.MaxInFlight || got.Queued != want.Queued {
		t.Fatalf("Site snapshot = %+v, want %+v", got, want)
	}
}
