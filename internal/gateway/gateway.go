package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/LuTianTian001/JieShan/internal/billing"
	"github.com/LuTianTian001/JieShan/internal/health"
	"github.com/LuTianTian001/JieShan/internal/routing"
	"github.com/LuTianTian001/JieShan/internal/store"
	"github.com/LuTianTian001/JieShan/internal/upstream"
)

type Gateway struct {
	store    *store.Store
	upstream *upstream.Client
	billing  *billing.Engine
}

func New(s *store.Store, client *upstream.Client, engines ...*billing.Engine) *Gateway {
	var priceEngine *billing.Engine
	if len(engines) > 0 {
		priceEngine = engines[0]
	} else {
		var err error
		priceEngine, err = billing.NewBuiltin()
		if err != nil {
			panic(fmt.Sprintf("load built-in price catalog: %v", err))
		}
	}
	return &Gateway{store: s, upstream: client, billing: priceEngine}
}

func (g *Gateway) Models(w http.ResponseWriter, r *http.Request) {
	key, err := g.authenticate(r, "")
	if err != nil {
		writeOpenAIError(w, http.StatusUnauthorized, err.Error(), "invalid_api_key")
		return
	}
	routes, err := g.store.ListPublicModels(r.Context())
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "cannot load model list", "internal_error")
		return
	}
	data := make([]map[string]any, 0, len(routes))
	for _, route := range routes {
		if !store.KeyAllowsModel(key, route.PublicModel) {
			continue
		}
		data = append(data, map[string]any{"id": route.PublicModel, "object": "model", "created": route.CreatedAt / 1000, "owned_by": "jieshan"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (g *Gateway) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	key, err := g.authenticate(r, "")
	if err != nil {
		status := http.StatusUnauthorized
		code := "invalid_api_key"
		if strings.Contains(strings.ToLower(err.Error()), "rpm") {
			status, code = http.StatusTooManyRequests, "rate_limit_exceeded"
		}
		writeOpenAIError(w, status, err.Error(), code)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeOpenAIError(w, http.StatusRequestEntityTooLarge, "request body exceeds 2 MiB", "request_too_large")
			return
		}
		writeOpenAIError(w, http.StatusBadRequest, "cannot read request", "invalid_request_error")
		return
	}
	meta, err := parseChatMeta(body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	if !store.KeyAllowsModel(key, meta.Model) {
		writeOpenAIError(w, http.StatusForbidden, "model is not allowed for this API key", "model_not_allowed")
		return
	}
	route, err := g.store.RouteByPublicModel(r.Context(), meta.Model)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, "model is not published", "model_not_found")
		return
	}
	settings, err := g.store.GetSettings(r.Context())
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "cannot load gateway settings", "internal_error")
		return
	}
	account, err := g.prepareAccounting(key, meta, body)
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
		ID: requestID, DownstreamKeyID: key.ID, RouteID: route.ID, RouteRevision: route.Revision,
		RequestedModel: meta.Model, ReasoningEffort: meta.ReasoningEffort, ThinkingBudget: meta.ThinkingBudget,
		Stream: meta.Stream, StartedAt: started.UnixMilli(),
	}
	if err := g.store.StartRequestWithReservation(r.Context(), startInput, account.reservedMicroUSD, key.QuotaMicroUSD != nil); err != nil {
		if errors.Is(err, store.ErrQuotaExceeded) {
			writeOpenAIError(w, http.StatusTooManyRequests, "API key quota is insufficient for this request", "insufficient_quota")
			return
		}
		slog.Error("cannot create request reservation", "request_id", requestID, "error", err)
		writeOpenAIError(w, http.StatusInternalServerError, "cannot persist request accounting", "internal_error")
		return
	}

	finalized := false
	pending := finishParams{model: "", status: "failed", httpStatus: http.StatusServiceUnavailable, message: "request ended before completion"}
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
			slog.Error("cannot finalize request accounting", "request_id", requestID, "error", err)
		}
	}()

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(settings.RequestDeadlineSeconds)*time.Second)
	defer cancel()

	byID := make(map[int64]store.RouteTarget, len(route.Targets))
	planning := make([]routing.Target, 0, len(route.Targets))
	for _, target := range route.Targets {
		byID[target.ID] = target
		cooldown := int64(0)
		if target.CooldownUntil != nil {
			cooldown = *target.CooldownUntil
		}
		planning = append(planning, routing.Target{
			ID: target.ID, Position: target.Position, Enabled: target.Enabled, CircuitPhase: target.CircuitPhase,
			CooldownUntil: cooldown, CredentialState: target.CredentialState, CapabilityState: target.CapabilityState,
		})
	}
	candidates := routing.OrderedEligible(planning, time.Now().UnixMilli())
	maxAttempts := settings.MaxAttempts
	if maxAttempts > len(candidates) {
		maxAttempts = len(candidates)
	}
	var lastStatus = http.StatusServiceUnavailable
	var lastBody []byte
	var lastError = "no eligible upstream target"
	attemptIndex := 0
	for _, candidate := range candidates {
		if attemptIndex >= maxAttempts {
			break
		}
		target := byID[candidate.ID]
		nowMS := time.Now().UnixMilli()
		lease := time.Duration(settings.RequestDeadlineSeconds)*time.Second + 5*time.Second
		allowed, permitErr := g.store.AcquireTargetPermit(ctx, target.ID, nowMS, lease)
		if permitErr != nil {
			slog.Error("cannot acquire target health permit", "target_id", target.ID, "error", permitErr)
			lastStatus = http.StatusInternalServerError
			lastError = "cannot persist target health state"
			break
		}
		if !allowed {
			continue
		}
		attemptCtx, attemptCancel, ok := nextAttemptContext(ctx, maxAttempts-attemptIndex)
		if !ok {
			lastError = "request deadline exceeded before another upstream could be attempted"
			break
		}
		currentAttempt := attemptIndex
		attemptIndex++
		attemptStarted := time.Now()
		upstreamRequest, buildErr := g.upstream.BuildChatRequest(attemptCtx, target, body)
		if buildErr != nil {
			attemptCancel()
			decision := health.Decision{Class: health.ClassTargetMisconfigured, Failover: true, PenalizeTarget: true}
			lastError = buildErr.Error()
			g.addAttempt(requestID, currentAttempt, target, "failed", 0, "protocol_or_config", string(decision.Class), lastError, attemptStarted, time.Since(attemptStarted), nil)
			if healthErr := g.store.RecordTargetFailure(r.Context(), target, decision, requestID, lastError, time.Now().UnixMilli(), 0); healthErr != nil {
				slog.Error("cannot persist target configuration failure", "request_id", requestID, "target_id", target.ID, "error", healthErr)
				lastStatus = http.StatusInternalServerError
				lastError = "cannot persist target health state"
				break
			}
			continue
		}
		response, requestErr := g.upstream.Do(upstreamRequest)
		if requestErr != nil {
			attemptCancel()
			if r.Context().Err() != nil {
				lastError = "downstream request cancelled"
				g.addAttempt(requestID, currentAttempt, target, "failed", 0, "downstream_cancelled", "client_cancelled", lastError, attemptStarted, time.Since(attemptStarted), nil)
				break
			}
			decision := health.Classify(0, nil, requestErr, false, nil)
			lastError = requestErr.Error()
			g.addAttempt(requestID, currentAttempt, target, "failed", 0, "transport_failure", string(decision.Class), lastError, attemptStarted, time.Since(attemptStarted), nil)
			if healthErr := g.store.RecordTargetFailure(r.Context(), target, decision, requestID, lastError, time.Now().UnixMilli(), decision.RetryAfter); healthErr != nil {
				slog.Error("cannot persist target failure", "request_id", requestID, "target_id", target.ID, "error", healthErr)
				lastStatus = http.StatusInternalServerError
				lastError = "cannot persist target health state"
				break
			}
			if decision.Failover {
				continue
			}
			break
		}

		if meta.Stream && response.StatusCode >= 200 && response.StatusCode < 300 {
			committed, firstToken, streamModel, streamUsage, streamErr := proxyDelayedStream(w, response, attemptStarted)
			response.Body.Close()
			attemptCancel()
			latency := time.Since(attemptStarted)
			if streamErr == nil {
				if healthErr := g.store.RecordTargetSuccess(r.Context(), target, time.Now().UnixMilli()); healthErr != nil {
					slog.Error("cannot persist target success", "request_id", requestID, "target_id", target.ID, "error", healthErr)
				}
				g.addAttempt(requestID, currentAttempt, target, "success", response.StatusCode, "", "", "", attemptStarted, latency, firstToken)
				if streamModel == "" {
					streamModel = target.UpstreamModel
				}
				if err := finalize(finishParams{model: streamModel, status: "success", httpStatus: response.StatusCode, firstToken: firstToken, tokens: streamUsage, charge: true}); err != nil {
					slog.Error("cannot settle streamed request", "request_id", requestID, "error", err)
				}
				return
			}
			if r.Context().Err() != nil || isDownstreamWriteError(streamErr) {
				lastError = "downstream request cancelled"
				g.addAttempt(requestID, currentAttempt, target, "failed", response.StatusCode, "downstream_cancelled", "client_cancelled", lastError, attemptStarted, latency, firstToken)
				if committed {
					if streamModel == "" {
						streamModel = target.UpstreamModel
					}
					if err := finalize(finishParams{model: streamModel, status: "failed", httpStatus: response.StatusCode, firstToken: firstToken, tokens: streamUsage, message: lastError, charge: true}); err != nil {
						slog.Error("cannot settle cancelled stream", "request_id", requestID, "error", err)
					}
					return
				}
				break
			}
			decision := health.Classify(response.StatusCode, nil, streamErr, committed, response.Header)
			lastError = streamErr.Error()
			g.addAttempt(requestID, currentAttempt, target, "failed", response.StatusCode, "stream_interrupted", string(decision.Class), lastError, attemptStarted, latency, firstToken)
			if healthErr := g.store.RecordTargetFailure(r.Context(), target, decision, requestID, lastError, time.Now().UnixMilli(), decision.RetryAfter); healthErr != nil {
				slog.Error("cannot persist streamed target failure", "request_id", requestID, "target_id", target.ID, "error", healthErr)
				if !committed {
					lastStatus = http.StatusInternalServerError
					lastError = "cannot persist target health state"
					break
				}
			}
			if committed {
				if streamModel == "" {
					streamModel = target.UpstreamModel
				}
				if err := finalize(finishParams{model: streamModel, status: "failed", httpStatus: response.StatusCode, firstToken: firstToken, tokens: streamUsage, message: lastError, charge: true}); err != nil {
					slog.Error("cannot settle interrupted stream", "request_id", requestID, "error", err)
				}
				return
			}
			if r.Context().Err() != nil {
				lastError = "downstream request cancelled"
				break
			}
			if !decision.Failover {
				if err := finalize(finishParams{model: target.UpstreamModel, status: "failed", httpStatus: response.StatusCode, firstToken: firstToken, message: lastError}); err != nil {
					slog.Error("cannot release failed stream reservation", "request_id", requestID, "error", err)
					writeOpenAIError(w, http.StatusInternalServerError, "cannot persist request accounting", "internal_error")
				}
				return
			}
			continue
		}

		responseBody, readErr := readLimited(response.Body, 16<<20)
		response.Body.Close()
		attemptCancel()
		if readErr != nil {
			if r.Context().Err() != nil {
				lastError = "downstream request cancelled"
				g.addAttempt(requestID, currentAttempt, target, "failed", response.StatusCode, "downstream_cancelled", "client_cancelled", lastError, attemptStarted, time.Since(attemptStarted), nil)
				break
			}
			decision := health.Classify(response.StatusCode, nil, readErr, false, response.Header)
			lastError = readErr.Error()
			g.addAttempt(requestID, currentAttempt, target, "failed", response.StatusCode, "read_failure", string(decision.Class), lastError, attemptStarted, time.Since(attemptStarted), nil)
			if healthErr := g.store.RecordTargetFailure(r.Context(), target, decision, requestID, lastError, time.Now().UnixMilli(), decision.RetryAfter); healthErr != nil {
				slog.Error("cannot persist target read failure", "request_id", requestID, "target_id", target.ID, "error", healthErr)
				lastStatus = http.StatusInternalServerError
				lastError = "cannot persist target health state"
				break
			}
			continue
		}
		decision := health.Classify(response.StatusCode, responseBody, nil, false, response.Header)
		semanticFailure := ""
		if decision.Class == health.ClassNone {
			if err := validateChatCompletionResponse(responseBody); err != nil {
				semanticFailure = err.Error()
				decision = health.ClassifyInvalidSuccess(responseBody)
			}
		}
		if decision.Class != health.ClassNone {
			if semanticFailure != "" {
				lastStatus, lastBody, lastError = http.StatusBadGateway, nil, semanticFailure
			} else {
				lastStatus, lastBody, lastError = response.StatusCode, responseBody, compact(responseBody, 500)
			}
			g.addAttempt(requestID, currentAttempt, target, "failed", response.StatusCode, "upstream_response", string(decision.Class), lastError, attemptStarted, time.Since(attemptStarted), nil)
			if healthErr := g.store.RecordTargetFailure(r.Context(), target, decision, requestID, lastError, time.Now().UnixMilli(), decision.RetryAfter); healthErr != nil {
				slog.Error("cannot persist target response failure", "request_id", requestID, "target_id", target.ID, "error", healthErr)
				lastStatus = http.StatusInternalServerError
				lastBody = nil
				lastError = "cannot persist target health state"
				break
			}
			if decision.Failover {
				continue
			}
			if err := finalize(finishParams{model: target.UpstreamModel, status: "failed", httpStatus: response.StatusCode, message: lastError}); err != nil {
				slog.Error("cannot release failed request reservation", "request_id", requestID, "error", err)
				writeOpenAIError(w, http.StatusInternalServerError, "cannot persist request accounting", "internal_error")
				return
			}
			copyResponseHeaders(w.Header(), response.Header)
			w.WriteHeader(response.StatusCode)
			_, _ = w.Write(responseBody)
			return
		}

		if healthErr := g.store.RecordTargetSuccess(r.Context(), target, time.Now().UnixMilli()); healthErr != nil {
			slog.Error("cannot persist target success", "request_id", requestID, "target_id", target.ID, "error", healthErr)
		}
		actualModel, tokenUsage := parseUsage(responseBody, target.UpstreamModel)
		g.addAttempt(requestID, currentAttempt, target, "success", response.StatusCode, "", "", "", attemptStarted, time.Since(attemptStarted), nil)
		if err := finalize(finishParams{model: actualModel, status: "success", httpStatus: response.StatusCode, tokens: tokenUsage, charge: true}); err != nil {
			slog.Error("cannot settle successful request", "request_id", requestID, "error", err)
			writeOpenAIError(w, http.StatusInternalServerError, "cannot persist request accounting", "internal_error")
			return
		}
		copyResponseHeaders(w.Header(), response.Header)
		w.WriteHeader(response.StatusCode)
		_, _ = w.Write(responseBody)
		return
	}

	if err := finalize(finishParams{status: "failed", httpStatus: lastStatus, message: lastError}); err != nil {
		slog.Error("cannot release exhausted request reservation", "request_id", requestID, "error", err)
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

func (g *Gateway) authenticate(r *http.Request, model string) (store.DownstreamKey, error) {
	raw := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if raw == "" {
		raw = strings.TrimSpace(r.Header.Get("x-api-key"))
	}
	if raw == "" {
		return store.DownstreamKey{}, errors.New("missing API key")
	}
	return g.store.AuthenticateDownstreamKey(r.Context(), raw, model)
}

type chatMeta struct {
	Model           string
	Stream          bool
	ReasoningEffort string
	ThinkingBudget  *int64
	MaxOutputTokens int64
}

const (
	defaultMaxOutputTokens = int64(4096)
	maxRequestedTokens     = int64(1_000_000)
	inputTokenOverhead     = int64(1024)
)

func parseChatMeta(body []byte) (chatMeta, error) {
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return chatMeta{}, fmt.Errorf("invalid JSON body")
	}
	model, _ := payload["model"].(string)
	model = strings.TrimSpace(model)
	if model == "" {
		return chatMeta{}, fmt.Errorf("model is required")
	}
	meta := chatMeta{Model: model, MaxOutputTokens: defaultMaxOutputTokens}
	meta.Stream, _ = payload["stream"].(bool)
	meta.ReasoningEffort, _ = payload["reasoning_effort"].(string)
	outputLimitSeen := false
	for _, field := range []string{"max_tokens", "max_completion_tokens"} {
		value, exists := payload[field]
		if !exists {
			continue
		}
		limit, ok := numberInt64(value)
		if !ok || limit > maxRequestedTokens {
			return chatMeta{}, fmt.Errorf("%s must be an integer between 0 and %d", field, maxRequestedTokens)
		}
		if !outputLimitSeen || limit > meta.MaxOutputTokens {
			meta.MaxOutputTokens = limit
		}
		outputLimitSeen = true
	}
	if outputLimitSeen && meta.MaxOutputTokens == defaultMaxOutputTokens {
		// Preserve an explicit value below the default instead of reserving the
		// default maximum.
		meta.MaxOutputTokens = 0
		for _, field := range []string{"max_tokens", "max_completion_tokens"} {
			if value, exists := payload[field]; exists {
				limit, _ := numberInt64(value)
				if limit > meta.MaxOutputTokens {
					meta.MaxOutputTokens = limit
				}
			}
		}
	}
	thinkingLimits := make([]any, 0, 3)
	if value, exists := payload["thinking_budget"]; exists {
		thinkingLimits = append(thinkingLimits, value)
	}
	if thinking, ok := payload["thinking"].(map[string]any); ok {
		if value, exists := thinking["budget_tokens"]; exists {
			thinkingLimits = append(thinkingLimits, value)
		}
	}
	if reasoning, ok := payload["reasoning"].(map[string]any); ok {
		if value, exists := reasoning["max_tokens"]; exists {
			thinkingLimits = append(thinkingLimits, value)
		}
	}
	for _, raw := range thinkingLimits {
		limit, ok := numberInt64(raw)
		if !ok || limit > maxRequestedTokens {
			return chatMeta{}, fmt.Errorf("thinking budget must be an integer between 0 and %d", maxRequestedTokens)
		}
		if meta.ThinkingBudget == nil || limit > *meta.ThinkingBudget {
			copy := limit
			meta.ThinkingBudget = &copy
		}
	}
	return meta, nil
}

type requestAccounting struct {
	keyID            int64
	reservedMicroUSD int64
	maximum          billing.Usage
	reservation      *billing.Reservation
}

func (g *Gateway) prepareAccounting(key store.DownstreamKey, meta chatMeta, body []byte) (requestAccounting, error) {
	if g.billing == nil {
		return requestAccounting{}, fmt.Errorf("billing engine is unavailable")
	}
	if int64(len(body)) > math.MaxInt64-inputTokenOverhead {
		return requestAccounting{}, fmt.Errorf("request is too large to meter")
	}
	maximum := billing.Usage{InputTokens: int64(len(body)) + inputTokenOverhead}
	completionLimit := meta.MaxOutputTokens
	if meta.ThinkingBudget != nil {
		if *meta.ThinkingBudget > completionLimit {
			completionLimit = *meta.ThinkingBudget
		}
		maximum.ReasoningTokens = *meta.ThinkingBudget
	}
	maximum.OutputTokens = completionLimit - maximum.ReasoningTokens
	reservation, err := g.billing.Reserve(meta.Model, maximum)
	if err != nil {
		if key.QuotaMicroUSD == nil && isPricingUnavailable(err) {
			return requestAccounting{keyID: key.ID, maximum: maximum}, nil
		}
		return requestAccounting{}, fmt.Errorf("price %q: %w", meta.Model, err)
	}
	reserved := int64(0)
	if key.QuotaMicroUSD != nil {
		reserved = int64(reservation.ReservedMicroUSD)
		if reserved == 0 {
			reserved = 1
			reservation.ReservedMicroUSD = billing.MicroUSD(reserved)
		}
	}
	return requestAccounting{keyID: key.ID, reservedMicroUSD: reserved, maximum: maximum, reservation: &reservation}, nil
}

func isPricingUnavailable(err error) bool {
	return errors.Is(err, billing.ErrModelNotFound) || errors.Is(err, billing.ErrModelUnpriced) ||
		errors.Is(err, billing.ErrCategoryUnpriced) || errors.Is(err, billing.ErrOutsidePriceRange)
}

func nextAttemptContext(parent context.Context, attemptsRemaining int) (context.Context, context.CancelFunc, bool) {
	if attemptsRemaining < 1 {
		return nil, nil, false
	}
	deadline, ok := parent.Deadline()
	if !ok {
		ctx, cancel := context.WithCancel(parent)
		return ctx, cancel, true
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return nil, nil, false
	}
	budget := remaining / time.Duration(attemptsRemaining)
	if budget <= 0 {
		return nil, nil, false
	}
	ctx, cancel := context.WithTimeout(parent, budget)
	return ctx, cancel, true
}

type downstreamWriteError struct{ err error }

func (e *downstreamWriteError) Error() string { return e.err.Error() }
func (e *downstreamWriteError) Unwrap() error { return e.err }

func isDownstreamWriteError(err error) bool {
	var target *downstreamWriteError
	return errors.As(err, &target)
}

func proxyDelayedStream(w http.ResponseWriter, response *http.Response, attemptStarted time.Time) (bool, *int64, string, usage, error) {
	reader := bufio.NewReaderSize(response.Body, 64<<10)
	var buffered bytes.Buffer
	var committed bool
	var firstToken *int64
	var actualModel string
	var observed usage
	for {
		lineBytes, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			return committed, firstToken, actualModel, observed, errors.New("upstream stream line exceeds 64 KiB")
		}
		line := string(lineBytes)
		if line != "" {
			if raw, ok := sseData(line); ok {
				model, candidate := parseUsage(raw, "")
				if model != "" {
					actualModel = model
				}
				if candidate.complete() {
					observed = candidate
				}
			}
			if !committed {
				buffered.WriteString(line)
				if buffered.Len() > 256<<10 {
					return false, nil, actualModel, observed, errors.New("upstream stream produced too much keepalive data")
				}
			}
			if !committed && semanticSSELine(line) {
				first := time.Since(attemptStarted).Milliseconds()
				firstToken = &first
				copyResponseHeaders(w.Header(), response.Header)
				w.WriteHeader(response.StatusCode)
				if _, writeErr := w.Write(buffered.Bytes()); writeErr != nil {
					return true, firstToken, actualModel, observed, &downstreamWriteError{err: writeErr}
				}
				committed = true
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			} else if committed {
				if _, writeErr := w.Write(lineBytes); writeErr != nil {
					return true, firstToken, actualModel, observed, &downstreamWriteError{err: writeErr}
				}
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if committed {
					return true, firstToken, actualModel, observed, nil
				}
				return false, nil, actualModel, observed, errors.New("upstream stream ended before semantic output")
			}
			return committed, firstToken, actualModel, observed, err
		}
	}
}

func validateChatCompletionResponse(body []byte) error {
	var payload struct {
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			Message *struct {
				Content          json.RawMessage   `json:"content"`
				ReasoningContent json.RawMessage   `json:"reasoning_content"`
				Refusal          json.RawMessage   `json:"refusal"`
				ToolCalls        []json.RawMessage `json:"tool_calls"`
				FunctionCall     json.RawMessage   `json:"function_call"`
			} `json:"message"`
			Text json.RawMessage `json:"text"`
		} `json:"choices"`
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return errors.New("upstream returned an empty chat completion")
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return errors.New("upstream returned a non-JSON chat completion")
	}
	if nonNullJSON(payload.Error) {
		return errors.New("upstream returned an error envelope with a success status")
	}
	if len(payload.Choices) == 0 {
		return errors.New("upstream chat completion did not contain choices")
	}
	for _, choice := range payload.Choices {
		if semanticJSONValue(choice.Text) {
			return nil
		}
		if choice.Message == nil {
			continue
		}
		if semanticJSONValue(choice.Message.Content) || semanticJSONValue(choice.Message.ReasoningContent) || semanticJSONValue(choice.Message.Refusal) ||
			len(choice.Message.ToolCalls) > 0 || nonEmptyJSONObject(choice.Message.FunctionCall) {
			return nil
		}
	}
	return errors.New("upstream chat completion contained no semantic model output")
}

func nonNullJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func semanticJSONValue(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	var text string
	if json.Unmarshal(trimmed, &text) == nil {
		return strings.TrimSpace(text) != ""
	}
	var items []json.RawMessage
	return json.Unmarshal(trimmed, &items) == nil && len(items) > 0
}

func nonEmptyJSONObject(raw json.RawMessage) bool {
	var value map[string]json.RawMessage
	return json.Unmarshal(raw, &value) == nil && len(value) > 0
}

func sseData(line string) ([]byte, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "data:") {
		return nil, false
	}
	raw := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	if raw == "" || raw == "[DONE]" {
		return nil, false
	}
	return []byte(raw), true
}

func semanticSSELine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, ":") || trimmed == "data: [DONE]" {
		return false
	}
	if !strings.HasPrefix(trimmed, "data:") {
		return false
	}
	raw := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	if raw == "" {
		return false
	}
	var payload map[string]any
	if json.Unmarshal([]byte(raw), &payload) != nil {
		return false
	}
	if delta, _ := payload["delta"].(string); delta != "" {
		return true
	}
	if output, _ := payload["output_text"].(string); output != "" {
		return true
	}
	if choices, ok := payload["choices"].([]any); ok {
		for _, rawChoice := range choices {
			choice, _ := rawChoice.(map[string]any)
			delta, _ := choice["delta"].(map[string]any)
			for _, key := range []string{"content", "reasoning_content", "refusal"} {
				if value, _ := delta[key].(string); value != "" {
					return true
				}
			}
			if calls, ok := delta["tool_calls"].([]any); ok && len(calls) > 0 {
				return true
			}
			if call, ok := delta["function_call"].(map[string]any); ok && len(call) > 0 {
				return true
			}
		}
	}
	if delta, ok := payload["delta"].(map[string]any); ok {
		for _, key := range []string{"text", "partial_json"} {
			if value, _ := delta[key].(string); value != "" {
				return true
			}
		}
	}
	return false
}

type usage struct {
	Input, CacheRead, Output, Reasoning *int64
}

func parseUsage(body []byte, fallbackModel string) (string, usage) {
	var payload struct {
		Model string `json:"model"`
		Usage *struct {
			PromptTokens     *int64 `json:"prompt_tokens"`
			InputTokens      *int64 `json:"input_tokens"`
			CompletionTokens *int64 `json:"completion_tokens"`
			OutputTokens     *int64 `json:"output_tokens"`
			PromptDetails    struct {
				CachedTokens *int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			InputDetails struct {
				CachedTokens *int64 `json:"cached_tokens"`
			} `json:"input_tokens_details"`
			CompletionDetails struct {
				ReasoningTokens *int64 `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
			OutputDetails struct {
				ReasoningTokens *int64 `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return fallbackModel, usage{}
	}
	payload.Model = strings.TrimSpace(payload.Model)
	if payload.Model == "" {
		payload.Model = fallbackModel
	}
	if payload.Usage == nil {
		return payload.Model, usage{}
	}
	promptTotal := firstInt64(payload.Usage.PromptTokens, payload.Usage.InputTokens)
	cacheRead := firstInt64(payload.Usage.PromptDetails.CachedTokens, payload.Usage.InputDetails.CachedTokens)
	completionTotal := firstInt64(payload.Usage.CompletionTokens, payload.Usage.OutputTokens)
	reasoning := firstInt64(payload.Usage.CompletionDetails.ReasoningTokens, payload.Usage.OutputDetails.ReasoningTokens)
	input, cached, promptOK := canonicalTokenPair(promptTotal, cacheRead)
	output, reasoned, completionOK := canonicalTokenPair(completionTotal, reasoning)
	if !promptOK || !completionOK {
		return payload.Model, usage{}
	}
	return payload.Model, usage{Input: input, CacheRead: cached, Output: output, Reasoning: reasoned}
}

func firstInt64(values ...*int64) *int64 {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func canonicalTokenPair(total, subset *int64) (*int64, *int64, bool) {
	if total == nil || *total < 0 {
		return nil, nil, false
	}
	detail := int64(0)
	if subset != nil {
		if *subset < 0 || *subset > *total {
			return nil, nil, false
		}
		detail = *subset
	}
	base := *total - detail
	return int64Value(base), int64Value(detail), true
}

func (u usage) complete() bool {
	if u.Input == nil || u.CacheRead == nil || u.Output == nil || u.Reasoning == nil {
		return false
	}
	return *u.Input > 0 || *u.CacheRead > 0 || *u.Output > 0 || *u.Reasoning > 0
}

func (u usage) billingUsage() billing.Usage {
	return billing.Usage{InputTokens: *u.Input, CacheReadTokens: *u.CacheRead, OutputTokens: *u.Output, ReasoningTokens: *u.Reasoning}
}

func usageFromBilling(value billing.Usage) usage {
	return usage{Input: int64Value(value.InputTokens), CacheRead: int64Value(value.CacheReadTokens), Output: int64Value(value.OutputTokens), Reasoning: int64Value(value.ReasoningTokens)}
}

func int64Value(value int64) *int64 {
	copy := value
	return &copy
}

func (g *Gateway) addAttempt(requestID string, index int, target store.RouteTarget, status string, httpStatus int, reason, class, message string, startedAt time.Time, latency time.Duration, firstToken *int64) {
	targetID, upstreamID := target.ID, target.UpstreamID
	latencyMS := latency.Milliseconds()
	var statusPtr *int
	if httpStatus > 0 {
		value := httpStatus
		statusPtr = &value
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := g.store.AddRequestAttempt(ctx, store.RequestAttempt{RequestID: requestID, AttemptIndex: index, TargetID: &targetID, UpstreamID: &upstreamID,
		UpstreamModel: target.UpstreamModel, Status: status, HTTPStatus: statusPtr, SwitchReason: reason, ErrorClass: class,
		ErrorMessage: message, LatencyMS: &latencyMS, FirstTokenMS: firstToken, CreatedAt: startedAt.UnixMilli()}); err != nil {
		slog.Error("cannot persist request attempt", "request_id", requestID, "attempt", index, "error", err)
	}
}

type finishParams struct {
	model      string
	status     string
	httpStatus int
	firstToken *int64
	tokens     usage
	message    string
	charge     bool
}

func (g *Gateway) finish(requestID string, started time.Time, account requestAccounting, params finishParams) error {
	tokens := params.tokens
	message := params.message
	charged := int64(0)
	priceSnapshot := ""
	if account.reservation != nil {
		actual := billing.Usage{}
		usedFallback := false
		if params.charge {
			if tokens.complete() {
				actual = tokens.billingUsage()
			} else {
				actual = account.maximum
				tokens = usageFromBilling(actual)
				usedFallback = true
			}
		}
		settlement, err := account.reservation.Settle(actual)
		if err != nil && params.charge && !usedFallback {
			actual = account.maximum
			tokens = usageFromBilling(actual)
			usedFallback = true
			settlement, err = account.reservation.Settle(actual)
		}
		if err != nil {
			return fmt.Errorf("calculate request settlement: %w", err)
		}
		charged = int64(settlement.ChargedMicroUSD)
		if usedFallback {
			message = appendMessage(message, "upstream usage missing or invalid; billed the conservative reservation estimate")
		}
		raw, err := settlement.Quote.SnapshotJSON()
		if err != nil {
			return fmt.Errorf("encode price snapshot: %w", err)
		}
		priceSnapshot = string(raw)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return g.store.FinishRequestAndSettle(ctx, requestID, account.keyID, account.reservedMicroUSD, charged, store.RequestFinish{
		ActualModel: params.model, Status: params.status, HTTPStatus: params.httpStatus,
		FirstTokenMS: params.firstToken, DurationMS: time.Since(started).Milliseconds(), InputTokens: tokens.Input,
		CacheReadTokens: tokens.CacheRead, OutputTokens: tokens.Output, ReasoningTokens: tokens.Reasoning,
		CostMicroUSD: charged, PriceSnapshotJSON: priceSnapshot, ErrorMessage: message, FinishedAt: time.Now().UnixMilli(),
	})
}

func appendMessage(existing, addition string) string {
	if existing == "" {
		return addition
	}
	return existing + "; " + addition
}

func readLimited(reader io.Reader, max int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > max {
		return nil, fmt.Errorf("upstream response exceeds %d bytes", max)
	}
	return body, nil
}

func copyResponseHeaders(dst, src http.Header) {
	for key, values := range src {
		switch strings.ToLower(key) {
		case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade", "set-cookie", "content-length":
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func numberInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), typed >= 0
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil && parsed >= 0
	default:
		return 0, false
	}
}

func newID() string {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

func compact(body []byte, max int) string {
	value := strings.TrimSpace(string(body))
	if len(value) > max {
		return value[:max] + "..."
	}
	return value
}

func writeOpenAIError(w http.ResponseWriter, status int, message, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message, "type": code, "code": code}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
