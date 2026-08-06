package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/LuTianTian001/JieShan/internal/health"
	"github.com/LuTianTian001/JieShan/internal/store"
)

func (g *Gateway) serveV3Inference(
	w http.ResponseWriter,
	r *http.Request,
	key store.DownstreamKey,
	meta chatMeta,
	body []byte,
	surface inferenceSurface,
	route store.ResolvedPublishedModel,
) {
	priceModel := firstNonEmptyString(route.OfficialPriceSKU, route.PublicName)
	account, err := g.prepareAccountingForModel(key, meta, body, priceModel)
	if err != nil {
		if isPricingUnavailable(err) && key.QuotaMicroUSD != nil {
			writeOpenAIError(w, http.StatusBadRequest, "this model does not have a confirmed official price for metered keys", "model_not_metered")
			return
		}
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}

	started := time.Now()
	requestID := newID()
	startInput := store.RequestStart{
		ID: requestID, RoutingGeneration: "v3", Surface: surface.name,
		DownstreamKeyID: key.ID, PublishedModelID: route.ID, PublishedModelRevision: route.Revision,
		RoutingProfileName: route.RoutingProfileName,
		RequestedModel:     meta.Model, ReasoningEffort: meta.ReasoningEffort, ThinkingBudget: meta.ThinkingBudget,
		Stream: meta.Stream, StartedAt: started.UnixMilli(),
	}
	if route.RoutingProfileID != nil {
		startInput.RoutingProfileID = *route.RoutingProfileID
	}
	if err := g.store.StartRequestWithReservation(r.Context(), startInput, account.reservedMicroUSD, key.QuotaMicroUSD != nil); err != nil {
		if errors.Is(err, store.ErrQuotaExceeded) {
			writeOpenAIError(w, http.StatusTooManyRequests, "API key quota is insufficient for this request", "insufficient_quota")
			return
		}
		slog.Error("cannot create V3 request reservation", "request_id", requestID, "error", err)
		writeOpenAIError(w, http.StatusInternalServerError, "cannot persist request accounting", "internal_error")
		return
	}

	finalized := false
	pending := finishParams{status: "failed", httpStatus: http.StatusServiceUnavailable, message: "request ended before completion"}
	finalize := func(next finishParams) error {
		pending = next
		err := g.finish(requestID, started, account, next)
		if err == nil {
			finalized = true
		}
		return err
	}
	defer func() {
		if finalized {
			return
		}
		if err := g.finish(requestID, started, account, pending); err != nil {
			slog.Error("cannot finalize V3 request accounting", "request_id", requestID, "error", err)
		}
	}()

	deadlineSeconds := positiveOrDefault(route.RequestDeadlineSeconds, 120)
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(deadlineSeconds)*time.Second)
	defer cancel()
	firstOutputTimeout := time.Duration(positiveOrDefault(route.FirstOutputTimeoutSeconds, 30)) * time.Second
	streamIdleTimeout := time.Duration(positiveOrDefault(route.StreamIdleTimeoutSeconds, 60)) * time.Second
	maxSiteAttempts := positiveOrDefault(route.MaxAttempts, 3)
	if maxSiteAttempts > len(route.Targets) {
		maxSiteAttempts = len(route.Targets)
	}

	lastStatus := http.StatusServiceUnavailable
	var lastBody []byte
	lastError := "no eligible upstream site"
	attemptIndex := 0
	siteAttempts := 0

routeLoop:
	for _, target := range route.Targets {
		if siteAttempts >= maxSiteAttempts {
			break
		}
		// Targets without an eligible credential cannot produce an upstream
		// request. Skip them without consuming the site-attempt budget or a
		// half-open recovery lease so later healthy sites remain reachable.
		if len(target.Credentials) == 0 {
			lastError = fmt.Sprintf("site %s has no eligible API key", target.SiteName)
			continue
		}
		nowMS := time.Now().UnixMilli()
		lease := time.Duration(deadlineSeconds)*time.Second + 5*time.Second
		allowed, permitErr := g.store.AcquireRouteSiteTargetPermit(ctx, target.ID, nowMS, lease, false)
		if permitErr != nil {
			slog.Error("cannot acquire V3 site target permit", "target_id", target.ID, "error", permitErr)
			lastStatus = http.StatusInternalServerError
			lastError = "cannot persist site health state"
			break
		}
		if !allowed {
			continue
		}
		siteAttempts++

		buildFailures := 0
		for _, credential := range target.Credentials {
			if ctx.Err() != nil {
				lastError = "request deadline exceeded before another upstream could be attempted"
				break routeLoop
			}
			currentAttempt := attemptIndex
			attemptIndex++
			attemptStarted := time.Now()

			attemptCtx, attemptCancel := context.WithCancel(ctx)
			var watchdog *streamWatchdog
			if meta.Stream {
				watchdog = newStreamWatchdog(firstOutputTimeout, streamIdleTimeout, attemptCancel)
			} else {
				attemptCancel()
				attemptCtx, attemptCancel = context.WithTimeout(ctx, firstOutputTimeout)
			}

			upstreamRequest, buildErr := surface.buildResolved(attemptCtx, target, credential, body)
			if buildErr != nil {
				watchdog.stop()
				attemptCancel()
				buildFailures++
				lastStatus = http.StatusBadGateway
				lastBody = nil
				lastError = buildErr.Error()
				g.addV3Attempt(requestID, currentAttempt, target, credential, "failed", 0,
					"credential_or_endpoint_config", string(health.ClassTargetMisconfigured), lastError,
					attemptStarted, time.Since(attemptStarted), nil)
				continue
			}

			response, requestErr := g.upstream.Do(upstreamRequest)
			if requestErr != nil {
				timeoutPhase := watchdog.stop()
				attemptCancel()
				if r.Context().Err() != nil {
					lastError = "downstream request cancelled"
					g.addV3Attempt(requestID, currentAttempt, target, credential, "failed", 0,
						"downstream_cancelled", "client_cancelled", lastError, attemptStarted, time.Since(attemptStarted), nil)
					break routeLoop
				}
				lastStatus = http.StatusGatewayTimeout
				lastBody = nil
				lastError = v3TransportError(requestErr, timeoutPhase)
				decision := health.Classify(0, nil, requestErr, false, nil)
				g.addV3Attempt(requestID, currentAttempt, target, credential, "failed", 0,
					v3TransportReason(timeoutPhase), string(decision.Class), lastError,
					attemptStarted, time.Since(attemptStarted), nil)
				if healthErr := g.store.RecordRouteSiteTargetFailure(r.Context(), target.ID, decision, requestID, lastError, time.Now().UnixMilli(), 0); healthErr != nil {
					slog.Error("cannot persist V3 transport failure", "request_id", requestID, "target_id", target.ID, "error", healthErr)
				}
				continue routeLoop
			}

			if meta.Stream && response.StatusCode >= 200 && response.StatusCode < 300 {
				committed, firstToken, streamModel, streamUsage, streamErr := proxyDelayedStreamControlled(w, response, attemptStarted, watchdog)
				response.Body.Close()
				timeoutPhase := watchdog.stop()
				attemptCancel()
				if timeoutPhase != "" {
					streamErr = errors.New(v3StreamTimeoutMessage(timeoutPhase))
				}
				latency := time.Since(attemptStarted)
				if streamErr == nil {
					g.recordV3Success(requestID, target, credential)
					g.addV3Attempt(requestID, currentAttempt, target, credential, "success", response.StatusCode,
						"", "", "", attemptStarted, latency, firstToken)
					if streamModel == "" {
						streamModel = target.SourceModel
					}
					if err := finalize(finishParams{model: streamModel, status: "success", httpStatus: response.StatusCode, firstToken: firstToken, tokens: streamUsage, charge: true}); err != nil {
						slog.Error("cannot settle V3 streamed request", "request_id", requestID, "error", err)
					}
					return
				}
				if r.Context().Err() != nil || isDownstreamWriteError(streamErr) {
					lastError = "downstream request cancelled"
					g.addV3Attempt(requestID, currentAttempt, target, credential, "failed", response.StatusCode,
						"downstream_cancelled", "client_cancelled", lastError, attemptStarted, latency, firstToken)
					if committed {
						if streamModel == "" {
							streamModel = target.SourceModel
						}
						_ = finalize(finishParams{model: streamModel, status: "failed", httpStatus: response.StatusCode, firstToken: firstToken, tokens: streamUsage, message: lastError, charge: true})
						return
					}
					break routeLoop
				}
				decision := health.Classify(response.StatusCode, nil, streamErr, committed, response.Header)
				lastStatus = http.StatusBadGateway
				lastBody = nil
				lastError = streamErr.Error()
				g.addV3Attempt(requestID, currentAttempt, target, credential, "failed", response.StatusCode,
					v3StreamReason(timeoutPhase), string(decision.Class), lastError, attemptStarted, latency, firstToken)
				if healthErr := g.store.RecordRouteSiteTargetFailure(r.Context(), target.ID, decision, requestID, lastError, time.Now().UnixMilli(), decision.RetryAfter); healthErr != nil {
					slog.Error("cannot persist V3 stream failure", "request_id", requestID, "target_id", target.ID, "error", healthErr)
				}
				if committed {
					if streamModel == "" {
						streamModel = target.SourceModel
					}
					_ = finalize(finishParams{model: streamModel, status: "failed", httpStatus: response.StatusCode, firstToken: firstToken, tokens: streamUsage, message: lastError, charge: true})
					return
				}
				continue routeLoop
			}

			watchdog.stop()
			responseBody, readErr := readLimited(response.Body, 16<<20)
			response.Body.Close()
			attemptCancel()
			if readErr != nil {
				if r.Context().Err() != nil {
					lastError = "downstream request cancelled"
					g.addV3Attempt(requestID, currentAttempt, target, credential, "failed", response.StatusCode,
						"downstream_cancelled", "client_cancelled", lastError, attemptStarted, time.Since(attemptStarted), nil)
					break routeLoop
				}
				lastStatus = http.StatusBadGateway
				lastBody = nil
				lastError = readErr.Error()
				decision := health.Classify(response.StatusCode, nil, readErr, false, response.Header)
				g.addV3Attempt(requestID, currentAttempt, target, credential, "failed", response.StatusCode,
					"site_read_failure", string(decision.Class), lastError, attemptStarted, time.Since(attemptStarted), nil)
				_ = g.store.RecordRouteSiteTargetFailure(r.Context(), target.ID, decision, requestID, lastError, time.Now().UnixMilli(), decision.RetryAfter)
				continue routeLoop
			}

			decision := health.Classify(response.StatusCode, responseBody, nil, false, response.Header)
			semanticFailure := ""
			if decision.Class == health.ClassNone {
				if validateErr := surface.validateResponse(responseBody); validateErr != nil {
					semanticFailure = validateErr.Error()
					decision = health.ClassifyInvalidSuccess(responseBody)
				}
			}
			if decision.Class == health.ClassNone {
				g.recordV3Success(requestID, target, credential)
				actualModel, tokenUsage := parseUsage(responseBody, target.SourceModel)
				g.addV3Attempt(requestID, currentAttempt, target, credential, "success", response.StatusCode,
					"", "", "", attemptStarted, time.Since(attemptStarted), nil)
				if err := finalize(finishParams{model: actualModel, status: "success", httpStatus: response.StatusCode, tokens: tokenUsage, charge: true}); err != nil {
					slog.Error("cannot settle V3 request", "request_id", requestID, "error", err)
					writeOpenAIError(w, http.StatusInternalServerError, "cannot persist request accounting", "internal_error")
					return
				}
				copyResponseHeaders(w.Header(), response.Header)
				w.WriteHeader(response.StatusCode)
				_, _ = w.Write(responseBody)
				return
			}

			if semanticFailure != "" {
				lastStatus, lastBody, lastError = http.StatusBadGateway, nil, semanticFailure
			} else {
				lastStatus, lastBody, lastError = response.StatusCode, responseBody, compact(responseBody, 500)
			}
			if isV3CredentialLocal(decision) {
				g.recordV3CredentialFailure(credential, decision, response.StatusCode, lastError, time.Now())
				g.addV3Attempt(requestID, currentAttempt, target, credential, "failed", response.StatusCode,
					v3CredentialReason(decision), string(decision.Class), lastError, attemptStarted, time.Since(attemptStarted), nil)
				continue
			}

			g.addV3Attempt(requestID, currentAttempt, target, credential, "failed", response.StatusCode,
				"site_upstream_response", string(decision.Class), lastError, attemptStarted, time.Since(attemptStarted), nil)
			if decision.PenalizeTarget || decision.UnsupportedModel {
				if healthErr := g.store.RecordRouteSiteTargetFailure(r.Context(), target.ID, decision, requestID, lastError, time.Now().UnixMilli(), decision.RetryAfter); healthErr != nil {
					slog.Error("cannot persist V3 site failure", "request_id", requestID, "target_id", target.ID, "error", healthErr)
				}
			}
			if decision.Failover {
				continue routeLoop
			}
			if err := finalize(finishParams{model: target.SourceModel, status: "failed", httpStatus: response.StatusCode, message: lastError}); err != nil {
				slog.Error("cannot release failed V3 reservation", "request_id", requestID, "error", err)
				writeOpenAIError(w, http.StatusInternalServerError, "cannot persist request accounting", "internal_error")
				return
			}
			copyResponseHeaders(w.Header(), response.Header)
			w.WriteHeader(response.StatusCode)
			_, _ = w.Write(responseBody)
			return
		}

		if buildFailures == len(target.Credentials) && buildFailures > 0 {
			decision := health.Decision{Class: health.ClassTargetMisconfigured, Failover: true, PenalizeTarget: true}
			if healthErr := g.store.RecordRouteSiteTargetFailure(r.Context(), target.ID, decision, requestID, lastError, time.Now().UnixMilli(), 0); healthErr != nil {
				slog.Error("cannot persist V3 target configuration failure", "request_id", requestID, "target_id", target.ID, "error", healthErr)
			}
		}
	}

	if err := finalize(finishParams{status: "failed", httpStatus: lastStatus, message: lastError}); err != nil {
		slog.Error("cannot release exhausted V3 reservation", "request_id", requestID, "error", err)
		writeOpenAIError(w, http.StatusInternalServerError, "cannot persist request accounting", "internal_error")
		return
	}
	if len(lastBody) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(lastStatus)
		_, _ = w.Write(lastBody)
		return
	}
	writeOpenAIError(w, lastStatus, lastError, "upstream_unavailable")
}

func (g *Gateway) recordV3Success(requestID string, target store.ResolvedRouteSiteTarget, credential store.InferenceCredentialSecret) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	now := time.Now().UnixMilli()
	if err := g.store.RecordRouteSiteTargetSuccess(ctx, target.ID, now); err != nil {
		slog.Error("cannot persist V3 site success", "request_id", requestID, "target_id", target.ID, "error", err)
	}
	if credential.RuntimeState != "active" {
		if err := g.store.UpdateInferenceCredentialRuntime(ctx, credential.ID, store.InferenceCredentialRuntimeUpdate{
			RuntimeState: "active", LastTestAt: &now, LastTestStatus: "success",
		}); err != nil {
			slog.Error("cannot reactivate V3 credential", "request_id", requestID, "credential_id", credential.ID, "error", err)
		}
	}
}

func (g *Gateway) recordV3CredentialFailure(credential store.InferenceCredentialSecret, decision health.Decision, status int, message string, now time.Time) {
	state := "active"
	var cooldownUntil *int64
	switch decision.Class {
	case health.ClassAuthInvalid, health.ClassPermissionDenied:
		state = "invalid"
	case health.ClassPaymentRequired:
		state = "exhausted"
	case health.ClassRateLimited:
		state = "rate_limited"
		cooldown := decision.RetryAfter
		if cooldown < time.Minute {
			cooldown = time.Minute
		}
		value := now.Add(cooldown).UnixMilli()
		cooldownUntil = &value
	default:
		return
	}
	checkedAt := now.UnixMilli()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := g.store.UpdateInferenceCredentialRuntime(ctx, credential.ID, store.InferenceCredentialRuntimeUpdate{
		RuntimeState: state, CooldownUntil: cooldownUntil, LastTestAt: &checkedAt,
		LastTestStatus: fmt.Sprintf("HTTP %d", status), LastErrorMessage: message,
	}); err != nil {
		slog.Error("cannot persist V3 credential failure", "credential_id", credential.ID, "error", err)
	}
}

func (g *Gateway) addV3Attempt(requestID string, index int, target store.ResolvedRouteSiteTarget, credential store.InferenceCredentialSecret, status string, httpStatus int, reason, class, message string, startedAt time.Time, latency time.Duration, firstToken *int64) {
	targetID, siteID, endpointID, credentialID, siteModelID := target.ID, target.SiteID, target.EndpointID, credential.ID, target.SiteModelID
	latencyMS := latency.Milliseconds()
	var statusPtr *int
	if httpStatus > 0 {
		value := httpStatus
		statusPtr = &value
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := g.store.AddRequestAttempt(ctx, store.RequestAttempt{
		RequestID: requestID, AttemptIndex: index, RoutingGeneration: "v3",
		RouteSiteTargetID: &targetID, SiteID: &siteID, SiteName: target.SiteName,
		EndpointID: &endpointID, EndpointName: target.EndpointName,
		InferenceCredentialID: &credentialID, CredentialName: credential.Name, SiteModelID: &siteModelID,
		UpstreamModel: target.SourceModel, Status: status, HTTPStatus: statusPtr, SwitchReason: reason,
		ErrorClass: class, ErrorMessage: message, LatencyMS: &latencyMS, FirstTokenMS: firstToken,
		CreatedAt: startedAt.UnixMilli(),
	}); err != nil {
		slog.Error("cannot persist V3 request attempt", "request_id", requestID, "attempt", index, "error", err)
	}
}

func isV3CredentialLocal(decision health.Decision) bool {
	switch decision.Class {
	case health.ClassAuthInvalid, health.ClassPermissionDenied, health.ClassPaymentRequired, health.ClassRateLimited:
		return true
	default:
		return false
	}
}

func v3CredentialReason(decision health.Decision) string {
	switch decision.Class {
	case health.ClassAuthInvalid, health.ClassPermissionDenied:
		return "credential_rejected"
	case health.ClassPaymentRequired:
		return "credential_exhausted"
	case health.ClassRateLimited:
		return "credential_rate_limited"
	default:
		return "credential_failure"
	}
}

func v3TransportError(err error, phase streamTimeoutPhase) string {
	if phase != "" {
		return v3StreamTimeoutMessage(phase)
	}
	return err.Error()
}

func v3TransportReason(phase streamTimeoutPhase) string {
	if phase == streamTimeoutFirstOutput {
		return "site_first_output_timeout"
	}
	if phase == streamTimeoutIdle {
		return "site_stream_idle_timeout"
	}
	return "site_transport_failure"
}

func v3StreamReason(phase streamTimeoutPhase) string {
	if phase != "" {
		return v3TransportReason(phase)
	}
	return "site_stream_interrupted"
}

func v3StreamTimeoutMessage(phase streamTimeoutPhase) string {
	if phase == streamTimeoutIdle {
		return "upstream stream exceeded the configured idle timeout"
	}
	return "upstream did not produce semantic output before the configured timeout"
}

func positiveOrDefault(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
