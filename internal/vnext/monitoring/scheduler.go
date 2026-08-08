package monitoring

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const (
	defaultPollInterval         = 5 * time.Second
	defaultLeaseDuration        = 5 * time.Minute
	defaultProbeTimeout         = 30 * time.Second
	defaultMaxConcurrentModels  = 1
	defaultMaxConcurrentTargets = 2
)

var (
	ErrProbeInProgress    = errors.New("model probe is already in progress")
	ErrProbeTargetMissing = errors.New("model probe target is unavailable")
)

type Repository interface {
	ClaimDueModelMonitors(context.Context, string, time.Time, time.Duration, int) ([]vnextstore.ModelMonitorJob, error)
	ClaimModelMonitor(context.Context, int64, string, time.Time, time.Duration) (vnextstore.ModelMonitorJob, error)
	FinishModelMonitorClaim(context.Context, int64, string, time.Time) error
	ReleaseModelMonitorClaim(context.Context, int64, string) error
	StartModelProbeRun(context.Context, vnextstore.ModelProbeRunWrite) error
	SaveModelProbeTargetResult(context.Context, vnextstore.ModelProbeTargetWrite) error
	FinishModelProbeRun(context.Context, string, string, time.Time) (vnextstore.ModelProbeRun, error)
}

type LiveTrafficRepository interface {
	LatestSuccessfulRequestAttempt(context.Context, int64, int64, time.Time) (time.Time, bool, error)
}

// HealthRepository deliberately matches the gateway health boundary. A probe
// must acquire a durable attempt sequence before sending traffic and applies
// the same routing.Failure vocabulary after it finishes.
type HealthRepository interface {
	AcquireTargetAttempt(context.Context, int64, routing.Revision, routing.HealthPolicy, time.Time) (vnextstore.TargetAttemptPermit, error)
	ApplyTargetHealthEvent(context.Context, int64, routing.HealthPolicy, routing.HealthEvent) (vnextstore.TargetHealthSnapshot, routing.ApplyResult, error)
}

type ProbeRequest struct {
	RunID                  string
	PublishedModelID       int64
	PublicModel            string
	PublishedModelRevision int64
	Target                 ProbeTarget
}

type ProbeTarget struct {
	PublishedModelTargetID       int64
	PublishedModelTargetRevision int64
	ProviderModelTargetID        int64
	ProviderModelTargetRevision  routing.Revision
	Position                     int
	SiteID                       int64
	SiteName                     string
	EndpointID                   int64
	EndpointName                 string
	BaseURL                      string
	WireProtocol                 string
	Surface                      string
	AdapterKind                  string
	AuthScheme                   string
	HeaderTemplate               []byte
	SecretHeadersConfigured      bool
	SecretHeadersCipherVersion   int64
	SourceModel                  string
	CredentialIDs                []int64
}

type ProbeObservation struct {
	Outcome            Outcome
	Failure            routing.Failure
	ErrorCode          string
	HTTPStatus         int
	Latency            time.Duration
	FirstOutputLatency *time.Duration
	Attempts           []ProbeAttempt
}

func (observation ProbeObservation) Validate() error {
	if observation.Outcome != OutcomeSuccess && observation.Outcome != OutcomeFailure {
		return fmt.Errorf("probe executor returned unsupported outcome %q", observation.Outcome)
	}
	if observation.HTTPStatus < 0 || observation.HTTPStatus > 599 ||
		(observation.HTTPStatus > 0 && observation.HTTPStatus < 100) {
		return errors.New("probe executor returned an invalid HTTP status")
	}
	if observation.Latency < 0 {
		return errors.New("probe executor returned a negative latency")
	}
	if observation.FirstOutputLatency != nil &&
		(*observation.FirstOutputLatency < 0 || *observation.FirstOutputLatency > observation.Latency) {
		return errors.New("probe executor returned an invalid first output latency")
	}
	if observation.Outcome == OutcomeSuccess && observation.Failure.Kind != "" {
		return errors.New("successful probe executor result contains a failure")
	}
	if observation.Outcome == OutcomeFailure && observation.Failure.Kind == "" {
		return errors.New("failed probe executor result has no failure kind")
	}
	return nil
}

// ProbeExecutor owns protocol-specific construction, credential iteration, and
// semantic-output detection. The scheduler owns timing, health sequencing,
// persistence, cancellation, and bounded concurrency around it.
type ProbeExecutor interface {
	Probe(context.Context, ProbeRequest) (ProbeObservation, error)
}

type Options struct {
	HealthPolicy         routing.HealthPolicy
	PolicyProvider       RuntimePolicyProvider
	PollInterval         time.Duration
	LeaseDuration        time.Duration
	ProbeTimeout         time.Duration
	MaxConcurrentModels  int
	MaxConcurrentTargets int
	Owner                string
	Now                  func() time.Time
	NewID                func() (string, error)
}

type Scheduler struct {
	repository Repository
	health     HealthRepository
	live       LiveTrafficRepository
	executor   ProbeExecutor
	policy     RuntimePolicyProvider
	poll       time.Duration
	lease      time.Duration
	timeout    time.Duration
	maxModels  int
	owner      string
	now        func() time.Time
	newID      func() (string, error)
	targets    chan struct{}

	// Network probes stay concurrent, while their short SQLite health/history
	// transactions are serialized to avoid self-inflicted writer contention.
	stateMu  sync.Mutex
	activeMu sync.Mutex
	active   map[int64]struct{}
}

type ModelRun struct {
	Run     vnextstore.ModelProbeRun
	Results []TargetResult
}

func NewScheduler(repository Repository, health HealthRepository, executor ProbeExecutor, options Options) (*Scheduler, error) {
	if repository == nil || health == nil || executor == nil {
		return nil, errors.New("monitor repository, health repository, and probe executor are required")
	}
	if options.HealthPolicy.FailureThreshold <= 0 {
		options.HealthPolicy = routing.DefaultHealthPolicy()
	}
	if options.PolicyProvider == nil {
		options.PolicyProvider = StaticRuntimePolicyProvider{Policy: RuntimePolicy{
			HealthPolicy: options.HealthPolicy, ProbeInterval: DefaultProbeInterval,
		}}
	}
	if options.PollInterval <= 0 {
		options.PollInterval = defaultPollInterval
	}
	if options.LeaseDuration <= 0 {
		options.LeaseDuration = defaultLeaseDuration
	}
	if options.ProbeTimeout <= 0 {
		options.ProbeTimeout = defaultProbeTimeout
	}
	if options.MaxConcurrentModels <= 0 {
		options.MaxConcurrentModels = defaultMaxConcurrentModels
	}
	if options.MaxConcurrentTargets <= 0 {
		options.MaxConcurrentTargets = defaultMaxConcurrentTargets
	}
	if options.MaxConcurrentModels > 32 || options.MaxConcurrentTargets > 32 {
		return nil, errors.New("monitor concurrency cannot exceed 32")
	}
	options.Owner = strings.TrimSpace(options.Owner)
	if options.Owner == "" {
		options.Owner = "jieshan-monitor"
	}
	if len(options.Owner) > 80 {
		return nil, errors.New("monitor owner prefix cannot exceed 80 characters")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.NewID == nil {
		options.NewID = newRandomID
	}
	scheduler := &Scheduler{
		repository: repository,
		health:     health,
		executor:   executor,
		policy:     options.PolicyProvider,
		poll:       options.PollInterval,
		lease:      options.LeaseDuration,
		timeout:    options.ProbeTimeout,
		maxModels:  options.MaxConcurrentModels,
		owner:      options.Owner,
		now:        options.Now,
		newID:      options.NewID,
		targets:    make(chan struct{}, options.MaxConcurrentTargets),
		active:     make(map[int64]struct{}),
	}
	if live, ok := repository.(LiveTrafficRepository); ok {
		scheduler.live = live
	}
	return scheduler, nil
}

func (scheduler *Scheduler) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("monitor scheduler context is required")
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			if _, err := scheduler.RunDueOnce(ctx); err != nil && ctx.Err() == nil {
				return err
			}
			timer.Reset(scheduler.poll)
		}
	}
}

func (scheduler *Scheduler) RunDueOnce(ctx context.Context) ([]ModelRun, error) {
	if ctx == nil {
		return nil, errors.New("monitor scheduler context is required")
	}
	owner, err := scheduler.newLeaseOwner()
	if err != nil {
		return nil, err
	}
	scheduler.stateMu.Lock()
	jobs, err := scheduler.repository.ClaimDueModelMonitors(ctx, owner, scheduler.now().UTC(), scheduler.lease, scheduler.maxModels)
	scheduler.stateMu.Unlock()
	if err != nil {
		return nil, err
	}
	runs := make([]ModelRun, len(jobs))
	errs := make([]error, len(jobs))
	var wait sync.WaitGroup
	for index := range jobs {
		job := jobs[index]
		if !scheduler.markActive(job.Setting.PublishedModelID) {
			cleanupCtx, cancel := cleanupContext()
			scheduler.stateMu.Lock()
			errs[index] = errors.Join(ErrProbeInProgress,
				scheduler.repository.ReleaseModelMonitorClaim(cleanupCtx, job.Setting.PublishedModelID, job.Setting.LeaseOwner))
			scheduler.stateMu.Unlock()
			cancel()
			continue
		}
		wait.Add(1)
		go func(index int, job vnextstore.ModelMonitorJob) {
			defer wait.Done()
			defer scheduler.unmarkActive(job.Setting.PublishedModelID)
			runs[index], errs[index] = scheduler.runClaimed(ctx, job, "scheduled")
		}(index, job)
	}
	wait.Wait()
	return runs, errors.Join(errs...)
}

// ProbeModel performs one manual model-wide run. Every currently enabled
// published model target is included once, preserving the operator's order.
func (scheduler *Scheduler) ProbeModel(ctx context.Context, publishedModelID int64) (ModelRun, error) {
	return scheduler.probeManual(ctx, publishedModelID, nil)
}

// ProbeTarget performs one manual run for exactly one currently enabled
// upstream target. The model-wide lease remains the serialization boundary so
// scheduled, model-wide, and target-specific probes cannot overlap.
func (scheduler *Scheduler) ProbeTarget(
	ctx context.Context,
	publishedModelID, providerModelTargetID int64,
) (ModelRun, error) {
	if providerModelTargetID <= 0 {
		return ModelRun{}, errors.New("provider model target ID must be positive")
	}
	return scheduler.probeManual(ctx, publishedModelID, []int64{providerModelTargetID})
}

// ProbeTargets performs one manual run for a selected set of currently enabled
// upstream targets. Selection is de-duplicated, while execution and returned
// results retain the published model's route order.
func (scheduler *Scheduler) ProbeTargets(
	ctx context.Context,
	publishedModelID int64,
	providerModelTargetIDs []int64,
) (ModelRun, error) {
	targetIDs, err := normalizeProbeTargetIDs(providerModelTargetIDs)
	if err != nil {
		return ModelRun{}, err
	}
	return scheduler.probeManual(ctx, publishedModelID, targetIDs)
}

func (scheduler *Scheduler) probeManual(
	ctx context.Context,
	publishedModelID int64,
	providerModelTargetIDs []int64,
) (ModelRun, error) {
	if ctx == nil {
		return ModelRun{}, errors.New("monitor probe context is required")
	}
	if publishedModelID <= 0 {
		return ModelRun{}, errors.New("published model ID must be positive")
	}
	if !scheduler.markActive(publishedModelID) {
		return ModelRun{}, ErrProbeInProgress
	}
	defer scheduler.unmarkActive(publishedModelID)
	owner, err := scheduler.newLeaseOwner()
	if err != nil {
		return ModelRun{}, err
	}
	scheduler.stateMu.Lock()
	job, err := scheduler.repository.ClaimModelMonitor(ctx, publishedModelID, owner, scheduler.now().UTC(), scheduler.lease)
	scheduler.stateMu.Unlock()
	if errors.Is(err, vnextstore.ErrModelMonitorBusy) {
		return ModelRun{}, ErrProbeInProgress
	}
	if err != nil {
		return ModelRun{}, err
	}
	if providerModelTargetIDs != nil {
		requested := make(map[int64]struct{}, len(providerModelTargetIDs))
		for _, targetID := range providerModelTargetIDs {
			requested[targetID] = struct{}{}
		}
		selected := make([]vnextstore.ModelMonitorTarget, 0, len(requested))
		for _, target := range job.Targets {
			if _, exists := requested[target.ProviderModelTargetID]; exists {
				selected = append(selected, target)
				delete(requested, target.ProviderModelTargetID)
			}
		}
		if len(requested) > 0 {
			cleanupCtx, cancel := cleanupContext()
			scheduler.stateMu.Lock()
			releaseErr := scheduler.repository.ReleaseModelMonitorClaim(
				cleanupCtx, publishedModelID, job.Setting.LeaseOwner,
			)
			scheduler.stateMu.Unlock()
			cancel()
			return ModelRun{}, errors.Join(ErrProbeTargetMissing, releaseErr)
		}
		job.Targets = selected
	}
	return scheduler.runClaimed(ctx, job, "manual")
}

func normalizeProbeTargetIDs(targetIDs []int64) ([]int64, error) {
	if len(targetIDs) == 0 {
		return nil, errors.New("at least one provider model target ID is required")
	}
	normalized := make([]int64, 0, len(targetIDs))
	seen := make(map[int64]struct{}, len(targetIDs))
	for _, targetID := range targetIDs {
		if targetID <= 0 {
			return nil, errors.New("provider model target IDs must be positive")
		}
		if _, exists := seen[targetID]; exists {
			continue
		}
		seen[targetID] = struct{}{}
		normalized = append(normalized, targetID)
	}
	return normalized, nil
}

func (scheduler *Scheduler) runClaimed(ctx context.Context, job vnextstore.ModelMonitorJob, trigger string) (ModelRun, error) {
	runID, err := scheduler.newID()
	if err != nil {
		scheduler.finishClaim(job, scheduler.now().UTC())
		return ModelRun{}, err
	}
	startedAt := scheduler.now().UTC()
	scheduler.stateMu.Lock()
	err = scheduler.repository.StartModelProbeRun(ctx, vnextstore.ModelProbeRunWrite{
		ID: runID, PublishedModelID: job.Setting.PublishedModelID, LeaseOwner: job.Setting.LeaseOwner,
		TriggerKind: trigger, TargetCount: len(job.Targets), StartedAt: startedAt,
	})
	scheduler.stateMu.Unlock()
	if err != nil {
		finishErr := scheduler.finishClaim(job, scheduler.now().UTC())
		return ModelRun{}, errors.Join(err, finishErr)
	}

	results := make([]TargetResult, len(job.Targets))
	persistErrors := make([]error, len(job.Targets))
	work := make(chan int)
	workerCount := len(job.Targets)
	if workerCount > cap(scheduler.targets) {
		workerCount = cap(scheduler.targets)
	}
	var wait sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := range work {
				results[index] = scheduler.probeTarget(ctx, runID, job, job.Targets[index], trigger)
				cleanupCtx, cancel := cleanupContext()
				scheduler.stateMu.Lock()
				persistErrors[index] = scheduler.repository.SaveModelProbeTargetResult(
					cleanupCtx,
					probeTargetWrite(job, job.Targets[index], results[index]),
				)
				scheduler.stateMu.Unlock()
				cancel()
			}
		}()
	}
	for index := range job.Targets {
		work <- index
	}
	close(work)
	wait.Wait()

	finishedAt := scheduler.now().UTC()
	status := "completed"
	if ctx.Err() != nil {
		status = "cancelled"
	} else if errors.Join(persistErrors...) != nil {
		status = "internal_error"
	}
	cleanupCtx, cancel := cleanupContext()
	scheduler.stateMu.Lock()
	finishedRun, finishRunErr := scheduler.repository.FinishModelProbeRun(cleanupCtx, runID, status, finishedAt)
	scheduler.stateMu.Unlock()
	cancel()
	finishClaimErr := scheduler.finishClaim(job, finishedAt)
	return ModelRun{Run: finishedRun, Results: results}, errors.Join(
		errors.Join(persistErrors...), finishRunErr, finishClaimErr, ctx.Err(),
	)
}

func (scheduler *Scheduler) probeTarget(
	ctx context.Context,
	runID string,
	job vnextstore.ModelMonitorJob,
	target vnextstore.ModelMonitorTarget,
	trigger string,
) TargetResult {
	startedAt := scheduler.now().UTC()
	result := TargetResult{RunID: runID, TargetID: target.ProviderModelTargetID, StartedAt: startedAt}
	select {
	case scheduler.targets <- struct{}{}:
		defer func() { <-scheduler.targets }()
	case <-ctx.Done():
		result.Outcome = OutcomeSkipped
		result.PermitReason = routing.PermitReason("scheduler_cancelled")
		result.ErrorCode = "probe_cancelled"
		result.FinishedAt = scheduler.now().UTC()
		result.LatencyMS = elapsedMilliseconds(startedAt, result.FinishedAt)
		return result
	}

	policy := normalizeRuntimePolicy(scheduler.policy.MonitoringSnapshot())
	if trigger == "scheduled" && scheduler.live != nil {
		scheduler.stateMu.Lock()
		_, recent, recentErr := scheduler.live.LatestSuccessfulRequestAttempt(
			ctx, target.ProviderModelTargetID, target.ProviderModelTargetRevision,
			startedAt.Add(-policy.ProbeInterval),
		)
		scheduler.stateMu.Unlock()
		if recentErr == nil && recent {
			result.Outcome = OutcomeSkipped
			result.PermitReason = routing.PermitRecentSuccess
			result.ErrorCode = string(routing.PermitRecentSuccess)
			result.FinishedAt = scheduler.now().UTC()
			result.LatencyMS = elapsedMilliseconds(startedAt, result.FinishedAt)
			return result
		}
	}

	scheduler.stateMu.Lock()
	permit, err := scheduler.health.AcquireTargetAttempt(ctx, target.ProviderModelTargetID,
		routing.Revision(target.ProviderModelTargetRevision), policy.HealthPolicy, scheduler.now().UTC())
	scheduler.stateMu.Unlock()
	if err != nil {
		result.Outcome = OutcomeFailure
		result.FailureKind = routing.FailureUnknown
		result.ErrorCode = "health_permit_failed"
		result.FinishedAt = scheduler.now().UTC()
		result.LatencyMS = elapsedMilliseconds(startedAt, result.FinishedAt)
		return result
	}
	result.PermitMode = permit.Permit.Mode
	result.PermitReason = permit.Permit.Reason
	if !permit.Permit.Allowed {
		result.Outcome = OutcomeSkipped
		result.ErrorCode = "health_" + string(permit.Permit.Reason)
		result.FinishedAt = scheduler.now().UTC()
		result.LatencyMS = elapsedMilliseconds(startedAt, result.FinishedAt)
		return result
	}

	probeCtx, cancel := context.WithTimeout(ctx, scheduler.timeout)
	observation, probeErr := scheduler.executor.Probe(probeCtx, ProbeRequest{
		RunID: runID, PublishedModelID: job.Setting.PublishedModelID, PublicModel: job.PublicModel,
		PublishedModelRevision: job.PublishedModelRevision, Target: monitorProbeTarget(target),
	})
	probeContextErr := probeCtx.Err()
	cancel()
	finishedAt := scheduler.now().UTC()
	result.FinishedAt = finishedAt
	result.Outcome = observation.Outcome
	result.FailureKind = observation.Failure.Kind
	result.ErrorCode = stableCode(observation.ErrorCode)
	result.HTTPStatus = observation.HTTPStatus
	result.Attempts = append([]ProbeAttempt(nil), observation.Attempts...)
	result.LatencyMS = durationMilliseconds(observation.Latency)
	if observation.Latency == 0 {
		result.LatencyMS = elapsedMilliseconds(startedAt, finishedAt)
	}
	if observation.FirstOutputLatency != nil {
		value := durationMilliseconds(*observation.FirstOutputLatency)
		result.FirstOutputLatencyMS = &value
	}

	if probeErr != nil {
		result.Outcome = OutcomeFailure
		switch {
		case errors.Is(probeContextErr, context.DeadlineExceeded):
			result.FailureKind = routing.FailureTransport
			result.ErrorCode = "probe_timeout"
		case errors.Is(ctx.Err(), context.Canceled):
			result.FailureKind = routing.FailureDownstreamCanceled
			result.ErrorCode = "probe_cancelled"
		case result.FailureKind == "":
			result.FailureKind = routing.FailureUnknown
			result.ErrorCode = "probe_executor_error"
		}
	} else if validateErr := observation.Validate(); validateErr != nil {
		result.Outcome = OutcomeFailure
		result.FailureKind = routing.FailureUnknown
		result.ErrorCode = "invalid_probe_result"
		result.FirstOutputLatencyMS = nil
	} else if result.Outcome == OutcomeSuccess && observation.FirstOutputLatency != nil &&
		*observation.FirstOutputLatency > policy.FirstOutputTimeout {
		result.Outcome = OutcomeFailure
		result.FailureKind = routing.FailureFirstOutputTimeout
		result.ErrorCode = string(routing.FailureFirstOutputTimeout)
	}
	if result.Outcome == "" {
		result.Outcome = OutcomeFailure
		result.FailureKind = routing.FailureUnknown
		result.ErrorCode = "empty_probe_result"
	}

	healthEvent := routing.HealthEvent{
		Revision: routing.Revision(target.ProviderModelTargetRevision), Sequence: permit.Sequence,
		OccurredAt: finishedAt,
	}
	if result.Outcome == OutcomeSuccess {
		healthEvent.Outcome = routing.HealthSuccess
	} else {
		healthEvent.Outcome = routing.HealthFailure
		healthEvent.IncidentID = fmt.Sprintf("probe:%s:%d", runID, target.ProviderModelTargetID)
		healthEvent.Failure = routing.Failure{Kind: result.FailureKind, RetryAfter: observation.Failure.RetryAfter}
	}
	cleanupCtx, cleanupCancel := cleanupContext()
	scheduler.stateMu.Lock()
	_, applyResult, applyErr := scheduler.health.ApplyTargetHealthEvent(
		cleanupCtx, target.ProviderModelTargetID, policy.HealthPolicy, healthEvent,
	)
	scheduler.stateMu.Unlock()
	cleanupCancel()
	if applyErr != nil {
		result.HealthErrorCode = "health_update_failed"
	} else {
		result.HealthApplied = applyResult.Applied
		result.HealthApplyReason = applyResult.Reason
	}
	return result
}

func (scheduler *Scheduler) finishClaim(job vnextstore.ModelMonitorJob, finishedAt time.Time) error {
	ctx, cancel := cleanupContext()
	defer cancel()
	scheduler.stateMu.Lock()
	err := scheduler.repository.FinishModelMonitorClaim(
		ctx, job.Setting.PublishedModelID, job.Setting.LeaseOwner, finishedAt,
	)
	scheduler.stateMu.Unlock()
	if errors.Is(err, vnextstore.ErrModelMonitorLeaseLost) {
		return nil
	}
	return err
}

func (scheduler *Scheduler) markActive(modelID int64) bool {
	scheduler.activeMu.Lock()
	defer scheduler.activeMu.Unlock()
	if _, exists := scheduler.active[modelID]; exists {
		return false
	}
	scheduler.active[modelID] = struct{}{}
	return true
}

func (scheduler *Scheduler) unmarkActive(modelID int64) {
	scheduler.activeMu.Lock()
	delete(scheduler.active, modelID)
	scheduler.activeMu.Unlock()
}

func (scheduler *Scheduler) newLeaseOwner() (string, error) {
	id, err := scheduler.newID()
	if err != nil {
		return "", err
	}
	owner := scheduler.owner + ":" + id
	if len(owner) > 128 {
		return "", errors.New("generated monitor lease owner is too long")
	}
	return owner, nil
}

func monitorProbeTarget(target vnextstore.ModelMonitorTarget) ProbeTarget {
	return ProbeTarget{
		PublishedModelTargetID: target.PublishedModelTargetID, PublishedModelTargetRevision: target.PublishedModelTargetRevision,
		ProviderModelTargetID:       target.ProviderModelTargetID,
		ProviderModelTargetRevision: routing.Revision(target.ProviderModelTargetRevision),
		Position:                    target.Position, SiteID: target.SiteID, SiteName: target.SiteName,
		EndpointID: target.EndpointID, EndpointName: target.EndpointName, BaseURL: target.BaseURL,
		WireProtocol: target.WireProtocol, Surface: target.Surface, AdapterKind: target.AdapterKind,
		AuthScheme: target.AuthScheme, HeaderTemplate: append([]byte(nil), target.HeaderTemplate...),
		SecretHeadersConfigured:    target.SecretHeadersConfigured,
		SecretHeadersCipherVersion: target.SecretHeadersCipherVersion,
		SourceModel:                target.SourceModel, CredentialIDs: append([]int64(nil), target.CredentialIDs...),
	}
}

func probeTargetWrite(
	job vnextstore.ModelMonitorJob,
	target vnextstore.ModelMonitorTarget,
	result TargetResult,
) vnextstore.ModelProbeTargetWrite {
	var httpStatus *int
	if result.HTTPStatus > 0 {
		value := result.HTTPStatus
		httpStatus = &value
	}
	return vnextstore.ModelProbeTargetWrite{
		RunID: result.RunID, PublishedModelID: job.Setting.PublishedModelID,
		PublishedModelTargetID:       target.PublishedModelTargetID,
		PublishedModelTargetRevision: target.PublishedModelTargetRevision,
		ProviderModelTargetID:        target.ProviderModelTargetID,
		ProviderModelTargetRevision:  target.ProviderModelTargetRevision, TargetPosition: target.Position,
		SiteID: target.SiteID, EndpointID: target.EndpointID, SiteName: target.SiteName,
		EndpointName: target.EndpointName, SourceModel: target.SourceModel,
		WireProtocol: target.WireProtocol, Surface: target.Surface, Outcome: string(result.Outcome),
		PermitMode: string(result.PermitMode), PermitReason: string(result.PermitReason), HTTPStatus: httpStatus,
		FailureKind: string(result.FailureKind), ErrorCode: result.ErrorCode, LatencyMS: result.LatencyMS,
		FirstOutputMS: result.FirstOutputLatencyMS, StartedAt: result.StartedAt, FinishedAt: result.FinishedAt,
		HealthApplied: result.HealthApplied, HealthApplyReason: string(result.HealthApplyReason),
		HealthErrorCode: result.HealthErrorCode,
	}
}

func durationMilliseconds(value time.Duration) int64 {
	if value <= 0 {
		return 0
	}
	return value.Milliseconds()
}

func elapsedMilliseconds(startedAt, finishedAt time.Time) int64 {
	if finishedAt.Before(startedAt) {
		return 0
	}
	return durationMilliseconds(finishedAt.Sub(startedAt))
}

func stableCode(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) > 128 {
		return value[:128]
	}
	return value
}

func cleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func newRandomID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate monitor ID: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}
