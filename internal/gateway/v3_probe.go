package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/LuTianTian001/JieShan/internal/health"
	"github.com/LuTianTian001/JieShan/internal/store"
)

var ErrPublishedModelMonitoringDisabled = errors.New("published model monitoring is not enabled")

// ProbePublishedModel checks every configured site for one selected model, or
// one target when targetID is provided. Sites run concurrently; credentials
// inside each site preserve administrator order and use the same local-key
// failover classification as live V3 traffic.
func (g *Gateway) ProbePublishedModel(ctx context.Context, publishedModelID int64, targetID *int64, triggerKind string) (store.ProbeRun, error) {
	model, err := g.store.GetPublishedModel(ctx, publishedModelID)
	if err != nil {
		return store.ProbeRun{}, err
	}
	if !model.Enabled || !model.MonitorEnabled {
		return store.ProbeRun{}, ErrPublishedModelMonitoringDisabled
	}
	resolved, err := g.store.ResolvePublishedModel(ctx, model.PublicName, time.Now().UnixMilli())
	if err != nil {
		return store.ProbeRun{}, err
	}
	targets := resolved.Targets
	if targetID != nil {
		targets = nil
		for _, target := range resolved.Targets {
			if target.ID == *targetID {
				targets = append(targets, target)
				break
			}
		}
		if len(targets) == 0 {
			return store.ProbeRun{}, errors.New("probe target is disabled, unavailable, or does not belong to the published model")
		}
	}
	if len(targets) == 0 {
		return store.ProbeRun{}, errors.New("published model has no eligible site targets")
	}

	startedAt := time.Now().UnixMilli()
	run := store.ProbeRun{
		ID: newID(), PublishedModelID: model.ID, PublishedModelRevision: model.Revision,
		TriggerKind: strings.ToLower(strings.TrimSpace(triggerKind)), TargetCount: len(targets), StartedAt: startedAt,
	}
	if run.TriggerKind == "" {
		run.TriggerKind = "manual"
	}
	if err := g.store.InsertProbeRun(ctx, run); err != nil {
		return store.ProbeRun{}, err
	}
	probeCtx, stopProbe := context.WithTimeout(ctx, time.Duration(positiveOrDefault(model.RequestDeadlineSeconds, 120))*time.Second)
	defer stopProbe()

	type probeJob struct {
		target    store.ResolvedRouteSiteTarget
		baseIndex int
	}
	jobs := make([]probeJob, 0, len(targets))
	nextIndex := 0
	for _, target := range targets {
		jobs = append(jobs, probeJob{target: target, baseIndex: nextIndex})
		span := len(target.Credentials)
		if span == 0 {
			span = 1
		}
		nextIndex += span
	}

	queue := make(chan probeJob)
	errorsOut := make(chan error, len(jobs))
	workers := 3
	if len(jobs) < workers {
		workers = len(jobs)
	}
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for job := range queue {
				if err := g.probePublishedTarget(probeCtx, run.ID, resolved.PublishedModel, job.target, job.baseIndex); err != nil {
					errorsOut <- err
				}
			}
		}()
	}
	for _, job := range jobs {
		select {
		case queue <- job:
		case <-probeCtx.Done():
			break
		}
		if probeCtx.Err() != nil {
			break
		}
	}
	close(queue)
	group.Wait()
	close(errorsOut)

	completionMessage := ""
	for probeErr := range errorsOut {
		if completionMessage == "" {
			completionMessage = probeErr.Error()
		}
		slog.Error("cannot persist V3 probe observation", "probe_run_id", run.ID, "error", probeErr)
	}
	if probeCtx.Err() != nil && completionMessage == "" {
		completionMessage = probeCtx.Err().Error()
	}
	persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	completed, err := g.store.CompleteProbeRun(persistCtx, run.ID, time.Now().UnixMilli(), completionMessage)
	if err != nil {
		return store.ProbeRun{}, err
	}
	return completed, nil
}

func (g *Gateway) probePublishedTarget(ctx context.Context, runID string, model store.PublishedModel, target store.ResolvedRouteSiteTarget, baseIndex int) error {
	started := time.Now()
	allowed, permitErr := g.store.AcquireRouteSiteTargetPermit(ctx, target.ID, started.UnixMilli(), probeTargetTimeout(model)+5*time.Second, true)
	if permitErr != nil {
		return g.persistProbeAttempt(store.ProbeAttempt{
			ProbeRunID: runID, AttemptIndex: baseIndex, RouteSiteTargetID: int64Value(target.ID), Status: "skipped",
			ErrorClass: "health_state_unavailable", ErrorMessage: permitErr.Error(), StartedAt: started.UnixMilli(), FinishedAt: time.Now().UnixMilli(),
		})
	}
	if !allowed {
		return g.persistProbeAttempt(store.ProbeAttempt{
			ProbeRunID: runID, AttemptIndex: baseIndex, RouteSiteTargetID: int64Value(target.ID), Status: "skipped",
			ErrorClass: "cooling", ErrorMessage: "site target is cooling or another recovery probe owns the lease",
			StartedAt: started.UnixMilli(), FinishedAt: time.Now().UnixMilli(),
		})
	}
	if len(target.Credentials) == 0 {
		return g.persistProbeAttempt(store.ProbeAttempt{
			ProbeRunID: runID, AttemptIndex: baseIndex, RouteSiteTargetID: int64Value(target.ID), Status: "skipped",
			ErrorClass: "no_eligible_credential", ErrorMessage: "site has no eligible API key",
			StartedAt: started.UnixMilli(), FinishedAt: time.Now().UnixMilli(),
		})
	}

	payload, _ := json.Marshal(map[string]any{
		"model":    model.PublicName,
		"messages": []map[string]string{{"role": "user", "content": "Reply with exactly OK and nothing else."}},
		"stream":   false,
	})
	buildFailures := 0
	for keyIndex, credential := range target.Credentials {
		attemptStarted := time.Now()
		attemptIndex := baseIndex + keyIndex
		if ctx.Err() != nil {
			return g.persistProbeAttempt(store.ProbeAttempt{
				ProbeRunID: runID, AttemptIndex: attemptIndex, RouteSiteTargetID: int64Value(target.ID),
				InferenceCredentialID: int64Value(credential.ID), Status: "skipped", ErrorClass: "cancelled",
				ErrorMessage: ctx.Err().Error(), StartedAt: attemptStarted.UnixMilli(), FinishedAt: time.Now().UnixMilli(),
			})
		}

		attemptCtx, cancel := context.WithTimeout(ctx, probeTargetTimeout(model))
		request, buildErr := g.upstream.BuildResolvedChatRequest(attemptCtx, target, credential, payload)
		if buildErr != nil {
			cancel()
			buildFailures++
			if err := g.persistPublishedProbeResult(runID, attemptIndex, target, credential, "failed", 0, nil,
				string(health.ClassTargetMisconfigured), buildErr.Error(), attemptStarted); err != nil {
				return err
			}
			continue
		}

		response, requestErr := g.upstream.Do(request)
		latency := time.Since(attemptStarted).Milliseconds()
		var responseBody []byte
		var status int
		var headers http.Header
		var readErr error
		if response != nil {
			status = response.StatusCode
			headers = response.Header
			responseBody, readErr = readLimited(response.Body, 1<<20)
			response.Body.Close()
		}
		cancel()
		if requestErr == nil && readErr != nil {
			requestErr = readErr
		}
		decision := health.Classify(status, responseBody, requestErr, false, headers)
		message := compact(responseBody, 300)
		if requestErr != nil {
			message = requestErr.Error()
		}
		if decision.Class == health.ClassNone {
			if validateErr := validateChatCompletionResponse(responseBody); validateErr != nil {
				decision = health.ClassifyInvalidSuccess(responseBody)
				message = validateErr.Error()
			}
		}

		if decision.Class == health.ClassNone {
			g.recordV3ProbeSuccess(runID, target, credential, status)
			return g.persistPublishedProbeResult(runID, attemptIndex, target, credential, "success", status, &latency, "", "", attemptStarted)
		}

		if err := g.persistPublishedProbeResult(runID, attemptIndex, target, credential, "failed", status, &latency,
			string(decision.Class), message, attemptStarted); err != nil {
			return err
		}
		if isV3CredentialLocal(decision) {
			g.recordV3CredentialFailure(credential, decision, status, message, time.Now())
			continue
		}
		if decision.PenalizeTarget || decision.UnsupportedModel {
			g.recordV3ProbeTargetFailure(runID, target.ID, decision, message)
		}
		return nil
	}

	if buildFailures == len(target.Credentials) {
		g.recordV3ProbeTargetFailure(runID, target.ID, health.Decision{
			Class: health.ClassTargetMisconfigured, Failover: true, PenalizeTarget: true,
		}, "all API keys failed local request construction")
	}
	return nil
}

func (g *Gateway) persistPublishedProbeResult(runID string, attemptIndex int, target store.ResolvedRouteSiteTarget, credential store.InferenceCredentialSecret, status string, httpStatus int, latency *int64, class, message string, started time.Time) error {
	var statusValue *int
	if httpStatus > 0 {
		statusValue = intValue(httpStatus)
	}
	return g.persistProbeAttempt(store.ProbeAttempt{
		ProbeRunID: runID, AttemptIndex: attemptIndex, RouteSiteTargetID: int64Value(target.ID),
		InferenceCredentialID: int64Value(credential.ID), Status: status, HTTPStatus: statusValue,
		LatencyMS: latency, ErrorClass: class, ErrorMessage: message,
		StartedAt: started.UnixMilli(), FinishedAt: time.Now().UnixMilli(),
	})
}

func (g *Gateway) persistProbeAttempt(item store.ProbeAttempt) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := g.store.InsertProbeAttempt(ctx, item)
	return err
}

func (g *Gateway) recordV3ProbeSuccess(runID string, target store.ResolvedRouteSiteTarget, credential store.InferenceCredentialSecret, status int) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	now := time.Now().UnixMilli()
	if err := g.store.RecordRouteSiteTargetSuccess(ctx, target.ID, now); err != nil {
		slog.Error("cannot persist V3 probe site success", "probe_run_id", runID, "target_id", target.ID, "error", err)
	}
	if err := g.store.UpdateInferenceCredentialRuntime(ctx, credential.ID, store.InferenceCredentialRuntimeUpdate{
		RuntimeState: "active", LastTestAt: &now, LastTestStatus: fmt.Sprintf("HTTP %d", status),
	}); err != nil {
		slog.Error("cannot persist V3 probe credential success", "probe_run_id", runID, "credential_id", credential.ID, "error", err)
	}
}

func (g *Gateway) recordV3ProbeTargetFailure(runID string, targetID int64, decision health.Decision, message string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := g.store.RecordRouteSiteTargetFailure(ctx, targetID, decision, runID+"-"+fmt.Sprint(targetID), message,
		time.Now().UnixMilli(), decision.RetryAfter); err != nil {
		slog.Error("cannot persist V3 probe site failure", "probe_run_id", runID, "target_id", targetID, "error", err)
	}
}

func probeTargetTimeout(model store.PublishedModel) time.Duration {
	return time.Duration(positiveOrDefault(model.FirstOutputTimeoutSeconds, 30)) * time.Second
}

func intValue(value int) *int { return &value }
