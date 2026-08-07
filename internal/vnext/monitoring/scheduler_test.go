package monitoring

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

func TestManualProbeRunsEveryTargetAndPersistsSemanticMetrics(t *testing.T) {
	fixture := newSchedulerFixture(t, "all-targets", 3)
	firstOutput := 45 * time.Millisecond
	executor := &trackingExecutor{run: func(_ context.Context, request ProbeRequest) (ProbeObservation, error) {
		if len(request.Target.CredentialIDs) != 1 {
			t.Fatalf("target %d credentials = %+v", request.Target.ProviderModelTargetID, request.Target.CredentialIDs)
		}
		return ProbeObservation{
			Outcome: OutcomeSuccess, HTTPStatus: 200, Latency: 120 * time.Millisecond,
			FirstOutputLatency: &firstOutput,
		}, nil
	}}
	scheduler := newTestScheduler(t, fixture.storage, executor, Options{MaxConcurrentTargets: 2})

	run, err := scheduler.ProbeModel(context.Background(), fixture.routeID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Run.Status != "completed" || run.Run.TargetCount != 3 || run.Run.SuccessCount != 3 || len(run.Results) != 3 {
		t.Fatalf("model-wide run = %+v", run)
	}
	for index, targetID := range fixture.targetIDs {
		result := run.Results[index]
		if result.TargetID != targetID || result.Outcome != OutcomeSuccess || result.HTTPStatus != 200 ||
			result.FirstOutputLatencyMS == nil || *result.FirstOutputLatencyMS != 45 || !result.HealthApplied {
			t.Fatalf("result[%d] = %+v", index, result)
		}
		history, historyErr := fixture.storage.ListModelProbeTargetResults(
			context.Background(), fixture.routeID, targetID, 5,
		)
		if historyErr != nil {
			t.Fatal(historyErr)
		}
		if len(history) != 1 || history[0].LatencyMS != 120 || history[0].FirstOutputMS == nil ||
			*history[0].FirstOutputMS != 45 || history[0].HTTPStatus == nil || *history[0].HTTPStatus != 200 {
			t.Fatalf("target %d history = %+v", targetID, history)
		}
		health, healthErr := fixture.storage.GetTargetHealth(context.Background(), targetID)
		if healthErr != nil {
			t.Fatal(healthErr)
		}
		if health.State.Phase != routing.CircuitClosed || health.State.Capability != routing.CapabilitySupported {
			t.Fatalf("target %d health = %+v", targetID, health.State)
		}
	}
	if executor.callCount() != 3 {
		t.Fatalf("executor calls = %d", executor.callCount())
	}
}

func TestOrdinaryProbeFailureBecomesSuspectBeforeCooling(t *testing.T) {
	fixture := newSchedulerFixture(t, "threshold", 1)
	executor := &trackingExecutor{run: func(context.Context, ProbeRequest) (ProbeObservation, error) {
		return ProbeObservation{
			Outcome: OutcomeFailure, HTTPStatus: 503, ErrorCode: "upstream_unavailable",
			Latency: 90 * time.Millisecond, Failure: routing.Failure{Kind: routing.FailureTransport},
		}, nil
	}}
	scheduler := newTestScheduler(t, fixture.storage, executor, Options{})

	first, err := scheduler.ProbeModel(context.Background(), fixture.routeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Results) != 1 || first.Results[0].Outcome != OutcomeFailure || !first.Results[0].HealthApplied {
		t.Fatalf("first probe = %+v", first)
	}
	health, err := fixture.storage.GetTargetHealth(context.Background(), fixture.targetIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if health.State.Phase != routing.CircuitSuspect || health.State.ConsecutiveFailures != 1 || !health.State.CooldownUntil.IsZero() {
		t.Fatalf("one ordinary failure cooled the target: %+v", health.State)
	}

	second, err := scheduler.ProbeModel(context.Background(), fixture.routeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Results) != 1 || second.Results[0].Outcome != OutcomeFailure {
		t.Fatalf("second probe = %+v", second)
	}
	health, err = fixture.storage.GetTargetHealth(context.Background(), fixture.targetIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if health.State.Phase != routing.CircuitOpen || health.State.ConsecutiveFailures != 2 || health.State.CooldownUntil.IsZero() {
		t.Fatalf("threshold did not open circuit: %+v", health.State)
	}

	third, err := scheduler.ProbeModel(context.Background(), fixture.routeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Results) != 1 || third.Results[0].Outcome != OutcomeSkipped ||
		third.Results[0].PermitReason != routing.PermitCooling {
		t.Fatalf("cooling target probe = %+v", third)
	}
	if executor.callCount() != 2 {
		t.Fatalf("executor was called through cooling circuit %d times", executor.callCount())
	}
}

func TestSchedulerSamplesDynamicHealthPolicyForEachTargetProbe(t *testing.T) {
	fixture := newSchedulerFixture(t, "dynamic-policy", 1)
	executor := &trackingExecutor{run: func(context.Context, ProbeRequest) (ProbeObservation, error) {
		return ProbeObservation{
			Outcome: OutcomeFailure, HTTPStatus: 503, ErrorCode: "upstream_unavailable",
			Latency: 50 * time.Millisecond, Failure: routing.Failure{Kind: routing.FailureTransport},
		}, nil
	}}
	provider := &mutableRuntimePolicyProvider{policy: RuntimePolicy{
		HealthPolicy: routing.HealthPolicy{
			FailureThreshold: 3, FailureWindow: 5 * time.Minute,
			Cooldown: 5 * time.Minute, HalfOpenLease: 30 * time.Second,
		},
		ProbeInterval: 5 * time.Minute,
	}}
	scheduler := newTestScheduler(t, fixture.storage, executor, Options{PolicyProvider: provider})

	if _, err := scheduler.ProbeModel(context.Background(), fixture.routeID); err != nil {
		t.Fatal(err)
	}
	health, err := fixture.storage.GetTargetHealth(context.Background(), fixture.targetIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if health.State.Phase != routing.CircuitSuspect || health.State.ConsecutiveFailures != 1 {
		t.Fatalf("first failure under threshold 3 = %+v", health.State)
	}

	provider.set(RuntimePolicy{
		HealthPolicy: routing.HealthPolicy{
			FailureThreshold: 2, FailureWindow: 5 * time.Minute,
			Cooldown: 2 * time.Minute, HalfOpenLease: 30 * time.Second,
		},
		ProbeInterval: 3 * time.Minute,
	})
	if _, err := scheduler.ProbeModel(context.Background(), fixture.routeID); err != nil {
		t.Fatal(err)
	}
	health, err = fixture.storage.GetTargetHealth(context.Background(), fixture.targetIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if health.State.Phase != routing.CircuitOpen || health.State.ConsecutiveFailures != 2 ||
		health.State.CooldownUntil.Sub(time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)) != 2*time.Minute {
		t.Fatalf("second failure did not use dynamic policy = %+v", health.State)
	}
}

func TestSchedulerPreventsDuplicateModelRunsAndBoundsTargetConcurrency(t *testing.T) {
	fixture := newSchedulerFixture(t, "bounded", 5)
	release := make(chan struct{})
	started := make(chan struct{}, 5)
	executor := &trackingExecutor{run: func(ctx context.Context, _ ProbeRequest) (ProbeObservation, error) {
		started <- struct{}{}
		select {
		case <-release:
			return ProbeObservation{Outcome: OutcomeSuccess, HTTPStatus: 200, Latency: time.Millisecond}, nil
		case <-ctx.Done():
			return ProbeObservation{}, ctx.Err()
		}
	}}
	scheduler := newTestScheduler(t, fixture.storage, executor, Options{MaxConcurrentTargets: 2})

	result := make(chan error, 1)
	go func() {
		_, err := scheduler.ProbeModel(context.Background(), fixture.routeID)
		result <- err
	}()
	<-started
	<-started
	if _, err := scheduler.ProbeModel(context.Background(), fixture.routeID); !errors.Is(err, ErrProbeInProgress) {
		t.Fatalf("overlapping model probe error = %v", err)
	}
	if executor.maxConcurrency() > 2 {
		t.Fatalf("target concurrency before release = %d", executor.maxConcurrency())
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if executor.callCount() != 5 || executor.maxConcurrency() > 2 {
		t.Fatalf("calls=%d max concurrency=%d", executor.callCount(), executor.maxConcurrency())
	}
}

func TestSchedulerCancellationFinishesRunWithoutPenalizingTarget(t *testing.T) {
	fixture := newSchedulerFixture(t, "cancel", 1)
	started := make(chan struct{})
	executor := &trackingExecutor{run: func(ctx context.Context, _ ProbeRequest) (ProbeObservation, error) {
		close(started)
		<-ctx.Done()
		return ProbeObservation{}, ctx.Err()
	}}
	scheduler := newTestScheduler(t, fixture.storage, executor, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan ModelRun, 1)
	errorsFound := make(chan error, 1)
	go func() {
		run, err := scheduler.ProbeModel(ctx, fixture.routeID)
		result <- run
		errorsFound <- err
	}()
	<-started
	cancel()
	run := <-result
	err := <-errorsFound
	if !errors.Is(err, context.Canceled) || run.Run.Status != "cancelled" || len(run.Results) != 1 {
		t.Fatalf("cancelled run=%+v err=%v", run, err)
	}
	if run.Results[0].FailureKind != routing.FailureDownstreamCanceled || run.Results[0].HealthApplied {
		t.Fatalf("cancelled target result = %+v", run.Results[0])
	}
	health, err := fixture.storage.GetTargetHealth(context.Background(), fixture.targetIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if health.State.Phase != routing.CircuitClosed || health.State.ConsecutiveFailures != 0 {
		t.Fatalf("cancellation penalized target: %+v", health.State)
	}
}

type schedulerFixture struct {
	storage   *vnextstore.Store
	routeID   int64
	targetIDs []int64
}

func newSchedulerFixture(t *testing.T, suffix string, targetCount int) schedulerFixture {
	t.Helper()
	ctx := context.Background()
	storage, err := vnextstore.Open(ctx, filepath.Join(t.TempDir(), "vnext.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	targetIDs := make([]int64, 0, targetCount)
	for index := 0; index < targetCount; index++ {
		siteID, err := storage.CreateSite(ctx, vnextstore.SiteWrite{
			Name: fmt.Sprintf("%s site %d", suffix, index), Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		endpointID, err := storage.CreateSiteEndpoint(ctx, siteID, vnextstore.SiteEndpointWrite{
			Name: fmt.Sprintf("Endpoint %d", index), BaseURL: fmt.Sprintf("https://%s-%d.example/v1", suffix, index),
			WireProtocol: "openai", Surface: "openai.chat_completions", AdapterKind: "generic",
			AuthScheme: "bearer", Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		credentialID, err := storage.CreateSiteCredential(ctx, siteID, vnextstore.SiteCredentialWrite{
			Name: "Primary", SecretCipher: []byte{1, 2, 3}, CipherVersion: 1, Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		endpoint, err := storage.GetSiteEndpoint(ctx, endpointID)
		if err != nil {
			t.Fatal(err)
		}
		if err := storage.ReplaceEndpointCredentialBindings(
			ctx, siteID, endpointID, endpoint.Revision, []int64{credentialID},
		); err != nil {
			t.Fatal(err)
		}
		targetID, err := storage.CreateProviderModelTarget(ctx, vnextstore.ProviderModelTargetWrite{
			SiteID: siteID, EndpointID: endpointID, SourceModel: "model-" + suffix, Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		targetIDs = append(targetIDs, targetID)
	}
	model, err := storage.CreatePublishedModel(ctx, vnextstore.PublishedModelWrite{
		PublicName: "public-" + suffix, OfficialPriceSKU: "public-" + suffix, Enabled: true,
	}, targetIDs)
	if err != nil {
		t.Fatal(err)
	}
	routeID := model.ID
	if _, err := storage.PutModelMonitorSetting(ctx, routeID, vnextstore.ModelMonitorSettingWrite{
		Enabled: true,
	}, time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	return schedulerFixture{storage: storage, routeID: routeID, targetIDs: targetIDs}
}

func newTestScheduler(t *testing.T, storage *vnextstore.Store, executor ProbeExecutor, options Options) *Scheduler {
	t.Helper()
	var idMu sync.Mutex
	idSequence := 0
	options.Owner = "test-monitor"
	options.Now = func() time.Time { return time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC) }
	options.NewID = func() (string, error) {
		idMu.Lock()
		defer idMu.Unlock()
		idSequence++
		return fmt.Sprintf("id-%d", idSequence), nil
	}
	scheduler, err := NewScheduler(storage, storage, executor, options)
	if err != nil {
		t.Fatal(err)
	}
	return scheduler
}

type trackingExecutor struct {
	mu     sync.Mutex
	run    func(context.Context, ProbeRequest) (ProbeObservation, error)
	calls  int
	active int
	max    int
}

type mutableRuntimePolicyProvider struct {
	mu     sync.Mutex
	policy RuntimePolicy
}

func (provider *mutableRuntimePolicyProvider) MonitoringSnapshot() RuntimePolicy {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.policy
}

func (provider *mutableRuntimePolicyProvider) set(policy RuntimePolicy) {
	provider.mu.Lock()
	provider.policy = policy
	provider.mu.Unlock()
}

func (executor *trackingExecutor) Probe(ctx context.Context, request ProbeRequest) (ProbeObservation, error) {
	executor.mu.Lock()
	executor.calls++
	executor.active++
	if executor.active > executor.max {
		executor.max = executor.active
	}
	executor.mu.Unlock()
	defer func() {
		executor.mu.Lock()
		executor.active--
		executor.mu.Unlock()
	}()
	return executor.run(ctx, request)
}

func (executor *trackingExecutor) callCount() int {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.calls
}

func (executor *trackingExecutor) maxConcurrency() int {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.max
}
