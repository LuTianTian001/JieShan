package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/LuTianTian001/JieShan/internal/health"
	"github.com/LuTianTian001/JieShan/internal/store"
)

func (g *Gateway) ProbeRoute(ctx context.Context, routeID int64, targetID *int64) ([]store.MonitorCell, error) {
	route, err := g.store.GetRoute(ctx, routeID)
	if err != nil {
		return nil, err
	}
	targets := make([]store.RouteTarget, 0, len(route.Targets))
	for _, target := range route.Targets {
		if targetID != nil && target.ID != *targetID {
			continue
		}
		if !target.Enabled || target.CredentialState != "active" {
			continue
		}
		if targetID == nil && target.CapabilityState == "unsupported" {
			interval := unsupportedReprobeInterval(route.MonitorIntervalSeconds)
			if target.LastProbeAt != nil && time.Since(time.UnixMilli(*target.LastProbeAt)) < interval {
				continue
			}
		}
		targets = append(targets, target)
	}
	if targetID != nil && len(targets) == 0 {
		return nil, fmt.Errorf("target is disabled, unavailable, or does not belong to route")
	}
	type probeResult struct {
		position int
		cell     store.MonitorCell
	}
	jobs := make(chan struct {
		position int
		target   store.RouteTarget
	})
	output := make(chan probeResult, len(targets))
	workers := 3
	if len(targets) < workers {
		workers = len(targets)
	}
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for job := range jobs {
				probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				cell := g.probeTarget(probeCtx, route, job.target)
				cancel()
				output <- probeResult{position: job.position, cell: cell}
			}
		}()
	}
	go func() {
		for position, target := range targets {
			jobs <- struct {
				position int
				target   store.RouteTarget
			}{position: position, target: target}
		}
		close(jobs)
		group.Wait()
		close(output)
	}()
	results := make([]store.MonitorCell, len(targets))
	for result := range output {
		results[result.position] = result.cell
	}
	return results, nil
}

func (g *Gateway) probeTarget(ctx context.Context, route store.Route, target store.RouteTarget) store.MonitorCell {
	checkedAt := time.Now().UnixMilli()
	cell := store.MonitorCell{TargetID: target.ID, UpstreamID: target.UpstreamID, UpstreamName: target.UpstreamName, CheckedAt: &checkedAt}
	allowed, permitErr := g.store.AcquireProbePermit(ctx, target.ID, checkedAt, 20*time.Second)
	if permitErr != nil || !allowed {
		cell.Status = "skipped"
		return cell
	}
	payload, _ := json.Marshal(map[string]any{
		"model": route.PublicModel, "messages": []map[string]string{{"role": "user", "content": "Reply with exactly OK and nothing else."}},
		"stream": false,
	})
	started := time.Now()
	req, buildErr := g.upstream.BuildChatRequest(ctx, target, payload)
	if buildErr != nil {
		decision := health.Decision{Class: health.ClassTargetMisconfigured, PenalizeTarget: true}
		return g.finishProbe(route, target, cell, decision, buildErr.Error(), nil, checkedAt)
	}
	resp, requestErr := g.upstream.Do(req)
	latency := time.Since(started).Milliseconds()
	cell.LatencyMS = &latency
	var body []byte
	var status int
	var headers http.Header
	var readErr error
	if resp != nil {
		status = resp.StatusCode
		headers = resp.Header
		body, readErr = readLimited(resp.Body, 1<<20)
		resp.Body.Close()
	}
	if requestErr == nil && readErr != nil {
		requestErr = readErr
	}
	decision := health.Classify(status, body, requestErr, false, headers)
	if decision.Class == health.ClassNone && !validProbeResponse(body) {
		decision = health.Decision{Class: health.ClassUpstreamTransient, PenalizeTarget: true}
		requestErr = fmt.Errorf("probe returned no semantic model output")
	}
	message := compact(body, 300)
	if requestErr != nil {
		message = requestErr.Error()
	}
	return g.finishProbe(route, target, cell, decision, message, &latency, checkedAt)
}

func (g *Gateway) finishProbe(route store.Route, target store.RouteTarget, cell store.MonitorCell, decision health.Decision, message string, latency *int64, checkedAt int64) store.MonitorCell {
	persistCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if decision.Class == health.ClassNone {
		cell.Status = "healthy"
		if err := g.store.RecordTargetSuccess(persistCtx, target, checkedAt); err != nil {
			slog.Error("cannot persist probe success", "route_id", route.ID, "target_id", target.ID, "error", err)
		}
		if err := g.store.InsertProbeResult(persistCtx, route.ID, target.ID, cell.Status, latency, "", "", checkedAt); err != nil {
			slog.Error("cannot persist probe result", "route_id", route.ID, "target_id", target.ID, "error", err)
		}
		return cell
	}
	cell.Status = "failed"
	incidentID := fmt.Sprintf("probe-%d-%d-%d", route.ID, checkedAt/1000, target.ID)
	if err := g.store.RecordTargetFailure(persistCtx, target, decision, incidentID, message, checkedAt, decision.RetryAfter); err != nil {
		slog.Error("cannot persist probe failure", "route_id", route.ID, "target_id", target.ID, "error", err)
	}
	if err := g.store.InsertProbeResult(persistCtx, route.ID, target.ID, cell.Status, latency, string(decision.Class), message, checkedAt); err != nil {
		slog.Error("cannot persist failed probe result", "route_id", route.ID, "target_id", target.ID, "error", err)
	}
	return cell
}

func unsupportedReprobeInterval(routeIntervalSeconds int) time.Duration {
	interval := time.Duration(routeIntervalSeconds) * time.Second * 12
	if interval < time.Hour {
		return time.Hour
	}
	if interval > 24*time.Hour {
		return 24 * time.Hour
	}
	return interval
}

func validProbeResponse(body []byte) bool {
	return validateChatCompletionResponse(body) == nil
}
