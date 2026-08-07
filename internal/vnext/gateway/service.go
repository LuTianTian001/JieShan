package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/capacity"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	"github.com/LuTianTian001/JieShan/internal/vnext/resolver"
	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const (
	defaultMaxResponseBytes       = int64(16 << 20)
	defaultMaxPrecommitStreamByte = int64(512 << 10)
	defaultAccountingTimeout      = 5 * time.Second
)

var (
	ErrInvalidRequest        = errors.New("invalid downstream request")
	ErrNoAvailableUpstream   = errors.New("no upstream target is currently available")
	ErrRuntimeUnavailable    = errors.New("gateway runtime is unavailable")
	ErrDownstreamClosed      = errors.New("downstream connection closed")
	ErrQuotaExceeded         = errors.New("downstream key quota exceeded")
	ErrPricingUnavailable    = errors.New("official pricing is unavailable")
	ErrRequestAlreadyStarted = errors.New("request ID has already been admitted")
	ErrFirstOutputTimeout    = errors.New("upstream first output timed out")
	ErrStreamIdleTimeout     = errors.New("upstream stream became idle")
	ErrRequestTimeout        = errors.New("gateway request timed out")
)

type RouteResolver interface {
	Resolve(context.Context, string, string, protocol.Protocol, protocol.Surface) (resolver.Resolution, error)
}

type AdapterRegistry interface {
	Components(protocol.Protocol, protocol.Surface) (protocol.AdapterComponents, error)
}

type HealthRepository interface {
	AcquireTargetAttempt(context.Context, int64, routing.Revision, routing.HealthPolicy, time.Time) (vnextstore.TargetAttemptPermit, error)
	ApplyTargetHealthEvent(context.Context, int64, routing.HealthPolicy, routing.HealthEvent) (vnextstore.TargetHealthSnapshot, routing.ApplyResult, error)
}

type CapacityManager interface {
	capacity.Acquirer
	ReportThrottle(capacity.ThrottleSignal) error
}

type SecretMaterial struct {
	Credential string
	Headers    http.Header
}

// SecretProvider materializes only the credential selected by the compiled
// plan and endpoint-bound secret headers. Implementations must validate the
// site, endpoint, and credential ownership before decrypting anything.
type SecretProvider interface {
	Materialize(context.Context, resolver.EndpointMetadata, routing.CredentialID) (SecretMaterial, error)
}

type CredentialEffectEvent struct {
	RequestID    string
	SiteID       int64
	EndpointID   int64
	TargetID     routing.TargetID
	CredentialID routing.CredentialID
	Effect       routing.CredentialEffect
	OccurredAt   time.Time
	RetryAfter   time.Duration
	HTTPStatus   int
	ErrorCode    string
}

type CredentialEffectStore interface {
	ApplyCredentialEffect(context.Context, CredentialEffectEvent) error
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type StreamSink interface {
	Commit(http.Header) error
	Write([]byte) error
}

type Options struct {
	HealthPolicy            routing.HealthPolicy
	FirstOutputTimeout      time.Duration
	StreamIdleTimeout       time.Duration
	RequestTimeout          time.Duration
	MaxAttempts             int
	PolicyProvider          RuntimePolicyProvider
	Capacity                CapacityManager
	MaxResponseBytes        int64
	MaxPrecommitStreamBytes int64
	DefaultMaxOutputTokens  int64
	AccountingTimeout       time.Duration
	Now                     func() time.Time
}

type Service struct {
	resolver               RouteResolver
	registry               AdapterRegistry
	health                 HealthRepository
	secrets                SecretProvider
	effects                CredentialEffectStore
	accounting             AccountingRepository
	prices                 PriceBook
	planner                ReservationPlanner
	client                 HTTPDoer
	policyProvider         RuntimePolicyProvider
	capacity               CapacityManager
	maxBody                int64
	maxBuffer              int64
	defaultMaxOutputTokens int64
	accountingTimeout      time.Duration
	now                    func() time.Time
}

func New(
	routeResolver RouteResolver,
	registry AdapterRegistry,
	health HealthRepository,
	secrets SecretProvider,
	effects CredentialEffectStore,
	accounting AccountingRepository,
	prices PriceBook,
	planner ReservationPlanner,
	client HTTPDoer,
	options Options,
) (*Service, error) {
	if routeResolver == nil || registry == nil || health == nil || secrets == nil || effects == nil || options.Capacity == nil ||
		accounting == nil || prices == nil || planner == nil || client == nil {
		return nil, errors.New("gateway resolver, registry, health store, capacity manager, secret provider, credential effects, accounting, pricing, reservation planner, and client are required")
	}
	initialPolicy := normalizeRuntimePolicy(RuntimePolicy{
		HealthPolicy: options.HealthPolicy, FirstOutputTimeout: options.FirstOutputTimeout,
		StreamIdleTimeout: options.StreamIdleTimeout, RequestTimeout: options.RequestTimeout,
		MaxAttempts: options.MaxAttempts,
	})
	if options.PolicyProvider == nil {
		options.PolicyProvider = StaticRuntimePolicyProvider{Policy: initialPolicy}
	}
	if options.MaxResponseBytes <= 0 {
		options.MaxResponseBytes = defaultMaxResponseBytes
	}
	if options.MaxPrecommitStreamBytes <= 0 {
		options.MaxPrecommitStreamBytes = defaultMaxPrecommitStreamByte
	}
	if options.MaxPrecommitStreamBytes > options.MaxResponseBytes {
		return nil, errors.New("precommit stream buffer cannot exceed the response limit")
	}
	if options.DefaultMaxOutputTokens <= 0 {
		options.DefaultMaxOutputTokens = defaultMaxOutputTokens
	}
	if options.AccountingTimeout <= 0 {
		options.AccountingTimeout = defaultAccountingTimeout
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Service{
		resolver: routeResolver, registry: registry, health: health, secrets: secrets,
		effects: effects, accounting: accounting, prices: prices, planner: planner,
		client: client, policyProvider: options.PolicyProvider, capacity: options.Capacity,
		maxBody: options.MaxResponseBytes, maxBuffer: options.MaxPrecommitStreamBytes,
		defaultMaxOutputTokens: options.DefaultMaxOutputTokens,
		accountingTimeout:      options.AccountingTimeout, now: options.Now,
	}, nil
}

func (service *Service) runtimePolicy() RuntimePolicy {
	if service == nil || service.policyProvider == nil {
		return normalizeRuntimePolicy(RuntimePolicy{})
	}
	return normalizeRuntimePolicy(service.policyProvider.Snapshot())
}

type Input struct {
	RequestID       string
	DownstreamKey   string
	PublicModel     string
	IngressProtocol protocol.Protocol
	IngressSurface  protocol.Surface
	Payload         []byte
	Stream          bool
}

type Attempt struct {
	Index                        int
	PublishedModelTargetID       int64
	PublishedModelTargetRevision int64
	TargetID                     routing.TargetID
	TargetRevision               routing.Revision
	SiteID                       int64
	SiteName                     string
	EndpointID                   int64
	EndpointName                 string
	CredentialID                 routing.CredentialID
	CredentialName               string
	SourceModel                  string
	ResponseModel                string
	WireProtocol                 protocol.Protocol
	Surface                      protocol.Surface
	PermitMode                   routing.PermitMode
	StartedAt                    time.Time
	FinishedAt                   time.Time
	HTTPStatus                   int
	FirstTokenMS                 *int64
	Outcome                      string
	FailureKind                  routing.FailureKind
	ErrorCode                    string
	ErrorClass                   string
	SwitchReason                 string
	ResponseCommitted            bool
	StateUpdateFailed            bool
}

type Result struct {
	RequestID           string
	DownstreamKeyID     int64
	PublicModel         string
	UpstreamModel       string
	TargetID            routing.TargetID
	CredentialID        routing.CredentialID
	StatusCode          int
	Header              http.Header
	Body                []byte
	Usage               protocol.Usage
	PriceCatalogVersion string
	PriceSKU            string
	ReservationNanoUSD  int64
	OfficialCostNanoUSD int64
	ChargedNanoUSD      int64
	QuotaCapped         bool
	MeteringStatus      string
	MeteringErrorCode   string
	Stream              bool
	Attempts            []Attempt
}

func (service *Service) Execute(ctx context.Context, input Input, sink StreamSink) (result Result, executionErr error) {
	result = Result{
		RequestID: normalizedRequestID(input.RequestID), PublicModel: strings.TrimSpace(input.PublicModel),
		Stream: input.Stream, MeteringStatus: "pending",
	}
	input.RequestID = result.RequestID
	if ctx == nil {
		return result, ErrInvalidRequest
	}
	if err := validateInput(input, sink); err != nil {
		return result, err
	}
	policy := service.runtimePolicy()
	requestCtx, cancelRequest := context.WithTimeoutCause(ctx, policy.RequestTimeout, ErrRequestTimeout)
	defer cancelRequest()
	resolution, err := service.resolver.Resolve(
		requestCtx,
		input.DownstreamKey,
		result.PublicModel,
		input.IngressProtocol,
		input.IngressSurface,
	)
	if err != nil {
		if timeoutErr := requestContextError(requestCtx); timeoutErr != nil {
			return result, timeoutErr
		}
		return result, err
	}
	result.DownstreamKeyID = resolution.DownstreamKeyID
	startedAt := service.now().UTC()
	routeCandidates, err := requestRouteCandidateSnapshots(resolution, startedAt)
	if err != nil {
		return result, fmt.Errorf("%w: snapshot effective route", ErrRuntimeUnavailable)
	}
	cursor := resolution.NewCursor(startedAt)
	candidate, ok := cursor.First()
	if !ok {
		return result, ErrNoAvailableUpstream
	}
	acquireCapacity := func() (*capacity.Permit, error) {
		admissionCandidates, candidateErr := capacityCandidates(resolution, cursor.RemainingTargets())
		if candidateErr != nil {
			return nil, fmt.Errorf("%w: build capacity candidates", ErrRuntimeUnavailable)
		}
		capacityPermit, capacityErr := service.capacity.Acquire(requestCtx, capacity.Request{
			KeyID: capacity.KeyID(resolution.DownstreamKeyID), Candidates: admissionCandidates,
		})
		if capacityErr != nil {
			if timeoutErr := requestContextError(requestCtx); timeoutErr != nil {
				return nil, timeoutErr
			}
			return nil, capacityErr
		}
		capacityPermit.ReleaseOnDone(requestCtx)
		for candidate.Target.ID != routing.TargetID(capacityPermit.TargetID) {
			candidate, ok = cursor.SkipTarget()
			if !ok {
				capacityPermit.Release()
				return nil, fmt.Errorf("%w: capacity selected an ineligible target", ErrRuntimeUnavailable)
			}
		}
		return capacityPermit, nil
	}
	firstCapacityPermit, err := acquireCapacity()
	if err != nil {
		return result, err
	}
	accountingState, err := service.startAccounting(
		requestCtx,
		input,
		resolution.PublishedModelID,
		resolution.PublishedModelRevision,
		resolution.RoutingProfileID,
		resolution.RoutingProfileName,
		resolution.SourceProfileID,
		resolution.SourceProfileName,
		resolution.RouteRevision,
		routeCandidates,
		resolution.DownstreamKeyID,
		resolution.OfficialPriceSKU,
		startedAt,
	)
	if err != nil {
		firstCapacityPermit.Release()
		return result, err
	}
	result.PriceCatalogVersion = accountingState.quote.CatalogVersion
	result.PriceSKU = accountingState.quote.SKU
	result.ReservationNanoUSD = accountingState.reservationNanoUSD
	defer func() {
		settlementErr := service.settleAccounting(requestCtx, accountingState, &result, executionErr)
		if settlementErr == nil {
			return
		}
		wrapped := fmt.Errorf("%w: settle downstream accounting", ErrRuntimeUnavailable)
		if errors.Is(settlementErr, ErrPricingUnavailable) {
			wrapped = fmt.Errorf("%w: settle frozen official price", ErrPricingUnavailable)
		}
		if executionErr == nil {
			executionErr = wrapped
		} else {
			executionErr = errors.Join(executionErr, wrapped)
		}
	}()

	appendAttempt := func(attempt Attempt, persist bool) error {
		result.Attempts = append(result.Attempts, attempt)
		if !persist {
			return nil
		}
		attemptCopy := attempt
		accountingState.lastAttempt = &attemptCopy
		if err := service.recordAttempt(requestCtx, result.RequestID, attempt); err != nil {
			return fmt.Errorf("%w: record upstream attempt", ErrRuntimeUnavailable)
		}
		index := attempt.Index
		accountingState.lastAttemptIndex = &index
		return nil
	}

	attemptsStarted := 0
	for ok {
		if attemptsStarted >= policy.MaxAttempts {
			firstCapacityPermit.Release()
			return result, ErrNoAvailableUpstream
		}
		capacityPermit := firstCapacityPermit
		firstCapacityPermit = nil
		if capacityPermit == nil {
			capacityPermit, err = acquireCapacity()
			if err != nil {
				return result, err
			}
		}
		metadata, exists := resolution.Endpoints[candidate.Target.ID]
		if !exists {
			capacityPermit.Release()
			return result, fmt.Errorf("%w: resolved endpoint metadata is missing", ErrRuntimeUnavailable)
		}
		attempt := Attempt{
			Index: len(result.Attempts), PublishedModelTargetID: metadata.PublishedModelTargetID,
			PublishedModelTargetRevision: metadata.PublishedModelTargetRevision,
			TargetID:                     candidate.Target.ID, TargetRevision: candidate.Target.Revision,
			SiteID: metadata.SiteID, SiteName: metadata.SiteName,
			EndpointID: metadata.EndpointID, EndpointName: metadata.EndpointName,
			CredentialID: candidate.Credential.ID, CredentialName: metadata.CredentialNames[candidate.Credential.ID],
			SourceModel: metadata.SourceModel, WireProtocol: metadata.Protocol, Surface: metadata.Surface,
			PermitMode: candidate.PermitMode, StartedAt: service.now().UTC(),
		}

		permit, acquireErr := service.health.AcquireTargetAttempt(
			requestCtx,
			int64(candidate.Target.ID),
			candidate.Target.Revision,
			policy.HealthPolicy,
			attempt.StartedAt,
		)
		if acquireErr != nil {
			capacityPermit.Release()
			attempt.FinishedAt = service.now().UTC()
			attempt.Outcome = "state_error"
			attempt.StateUpdateFailed = true
			_ = appendAttempt(attempt, false)
			return result, fmt.Errorf("%w: acquire target permit", ErrRuntimeUnavailable)
		}
		attempt.PermitMode = permit.Permit.Mode
		if !permit.Permit.Allowed {
			capacityPermit.Release()
			attempt.FinishedAt = service.now().UTC()
			attempt.Outcome = "skipped"
			attempt.ErrorCode = string(permit.Permit.Reason)
			_ = appendAttempt(attempt, false)
			if err := service.markRouteCandidateSkipped(requestCtx, result.RequestID, candidate.Target.ID, string(permit.Permit.Reason)); err != nil {
				return result, fmt.Errorf("%w: record skipped route candidate", ErrRuntimeUnavailable)
			}
			candidate, ok = cursor.SkipTarget()
			continue
		}
		attemptsStarted++

		material, materialErr := service.secrets.Materialize(requestCtx, metadata, candidate.Credential.ID)
		if materialErr != nil || strings.TrimSpace(material.Credential) == "" {
			capacityPermit.Release()
			failure := routing.Failure{Kind: routing.FailureCredentialAuth}
			attempt.ErrorCode = "credential_unavailable"
			attempt.ErrorClass = string(routing.FailureCredentialAuth)
			service.finishFailure(requestCtx, policy.HealthPolicy, result.RequestID, metadata, candidate, permit.Sequence, failure, &attempt)
			if err := appendAttempt(attempt, true); err != nil {
				return result, err
			}
			candidate, ok = cursor.Advance(failure)
			continue
		}

		components, componentsErr := service.registry.Components(metadata.Protocol, metadata.Surface)
		if componentsErr != nil || components.RequestEncoder == nil || components.ResponseDecoder == nil ||
			components.StreamDecoder == nil || components.UsageExtractor == nil || components.ErrorDecoder == nil {
			capacityPermit.Release()
			failure := routing.Failure{Kind: routing.FailureTargetMisconfigured}
			attempt.ErrorCode = "adapter_unavailable"
			attempt.ErrorClass = string(routing.FailureTargetMisconfigured)
			service.finishFailure(requestCtx, policy.HealthPolicy, result.RequestID, metadata, candidate, permit.Sequence, failure, &attempt)
			if err := appendAttempt(attempt, true); err != nil {
				return result, err
			}
			candidate, ok = cursor.Advance(failure)
			continue
		}

		encoded, encodeErr := components.RequestEncoder.EncodeRequest(requestCtx, protocol.RequestBuildInput{
			Protocol: metadata.Protocol,
			Surface:  metadata.Surface,
			BaseURL:  metadata.BaseURL,
			Model:    metadata.SourceModel,
			Payload:  append([]byte(nil), input.Payload...),
			Auth:     protocol.AuthInput{Scheme: metadata.AuthScheme, Secret: material.Credential},
		})
		if encodeErr != nil {
			capacityPermit.Release()
			attempt.FinishedAt = service.now().UTC()
			attempt.Outcome = "rejected"
			attempt.FailureKind = routing.FailureClientInvalid
			attempt.ErrorCode = "invalid_payload"
			attempt.ErrorClass = string(routing.FailureClientInvalid)
			if err := appendAttempt(attempt, true); err != nil {
				return result, err
			}
			return result, ErrInvalidRequest
		}

		attemptCtx, cancelAttempt := context.WithCancelCause(requestCtx)
		watchdog := newAttemptWatchdog(cancelAttempt, policy.FirstOutputTimeout, policy.StreamIdleTimeout)
		request, requestErr := http.NewRequestWithContext(attemptCtx, encoded.Method, encoded.URL, bytes.NewReader(encoded.Body))
		if requestErr == nil {
			request.Header = encoded.Header.Clone()
			requestErr = mergeEndpointHeaders(request.Header, material.Headers)
		}
		if requestErr != nil {
			capacityPermit.Release()
			watchdog.Stop()
			cancelAttempt(nil)
			failure := routing.Failure{Kind: routing.FailureTargetMisconfigured}
			attempt.ErrorCode = "invalid_endpoint_request"
			attempt.ErrorClass = string(routing.FailureTargetMisconfigured)
			service.finishFailure(requestCtx, policy.HealthPolicy, result.RequestID, metadata, candidate, permit.Sequence, failure, &attempt)
			if err := appendAttempt(attempt, true); err != nil {
				return result, err
			}
			candidate, ok = cursor.Advance(failure)
			continue
		}

		response, requestErr := service.client.Do(request)
		if requestErr != nil || response == nil || response.Body == nil {
			if response != nil && response.Body != nil {
				_ = response.Body.Close()
			}
			capacityPermit.Release()
			failure, errorCode, terminalErr, classified := contextFailure(attemptCtx, false)
			watchdog.Stop()
			cancelAttempt(nil)
			if !classified {
				failure = routing.Failure{Kind: routing.FailureTransport}
				errorCode = "upstream_transport"
			}
			attempt.ErrorCode = errorCode
			attempt.ErrorClass = string(failure.Kind)
			service.finishFailure(requestCtx, policy.HealthPolicy, result.RequestID, metadata, candidate, permit.Sequence, failure, &attempt)
			if failure.Kind == routing.FailureDownstreamCanceled {
				attempt.Outcome = "cancelled"
			}
			if err := appendAttempt(attempt, true); err != nil {
				return result, err
			}
			if terminalErr != nil {
				return result, terminalErr
			}
			candidate, ok = cursor.Advance(failure)
			continue
		}
		response.Body = capacityPermit.WrapReadCloser(response.Body)

		var executionErr error
		if input.Stream {
			executionErr = service.consumeStream(attemptCtx, response, components, sink, watchdog, policy.HealthPolicy, &result, &attempt, metadata, candidate, permit.Sequence)
		} else {
			executionErr = service.consumeResponse(attemptCtx, response, components, watchdog, policy.HealthPolicy, &result, &attempt, metadata, candidate, permit.Sequence)
		}
		watchdog.Stop()
		cancelAttempt(nil)
		if err := appendAttempt(attempt, true); err != nil {
			return result, err
		}
		if executionErr == nil {
			return result, nil
		}
		var capacityRetry *capacityRetryError
		if errors.As(executionErr, &capacityRetry) {
			if timeoutErr := requestContextError(requestCtx); timeoutErr != nil {
				return result, timeoutErr
			}
			candidate, ok = cursor.SkipTarget()
			continue
		}
		var retry *retryError
		if !errors.As(executionErr, &retry) {
			return result, executionErr
		}
		if timeoutErr := requestContextError(requestCtx); timeoutErr != nil {
			return result, timeoutErr
		}
		candidate, ok = cursor.Advance(retry.failure)
	}
	return result, ErrNoAvailableUpstream
}

func capacityCandidates(resolution resolver.Resolution, candidates []routing.Candidate) ([]capacity.Candidate, error) {
	result := make([]capacity.Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		metadata, exists := resolution.Endpoints[candidate.Target.ID]
		if !exists || metadata.SiteID <= 0 {
			return nil, errors.New("eligible target metadata is unavailable")
		}
		result = append(result, capacity.Candidate{
			TargetID: capacity.TargetID(candidate.Target.ID), SiteID: capacity.SiteID(metadata.SiteID),
		})
	}
	if len(result) == 0 {
		return nil, errors.New("no eligible target remains")
	}
	return result, nil
}

func validateInput(input Input, sink StreamSink) error {
	if strings.TrimSpace(input.DownstreamKey) == "" || strings.TrimSpace(input.PublicModel) == "" || len(input.Payload) == 0 {
		return ErrInvalidRequest
	}
	if err := protocol.ValidatePair(input.IngressProtocol, input.IngressSurface); err != nil {
		return ErrInvalidRequest
	}
	if input.Stream && sink == nil {
		return errors.New("stream sink is required for a streaming request")
	}
	return nil
}

func (service *Service) finishFailure(
	ctx context.Context,
	policy routing.HealthPolicy,
	requestID string,
	metadata resolver.EndpointMetadata,
	candidate routing.Candidate,
	sequence uint64,
	failure routing.Failure,
	attempt *Attempt,
) {
	stateCtx, cancel := service.stateUpdateContext(ctx)
	defer cancel()
	finishedAt := service.now().UTC()
	attempt.FinishedAt = finishedAt
	attempt.Outcome = "failed"
	attempt.FailureKind = failure.Kind
	attempt.ResponseCommitted = failure.ResponseCommitted
	disposition := failure.Disposition()
	if disposition.Retry != routing.RetryStop {
		attempt.SwitchReason = string(disposition.Retry)
	}
	if disposition.CredentialEffect != routing.CredentialEffectNone {
		if err := service.effects.ApplyCredentialEffect(stateCtx, CredentialEffectEvent{
			RequestID: requestID, SiteID: metadata.SiteID, EndpointID: metadata.EndpointID,
			TargetID: candidate.Target.ID, CredentialID: candidate.Credential.ID,
			Effect: disposition.CredentialEffect, OccurredAt: finishedAt,
			RetryAfter: failure.RetryAfter, HTTPStatus: attempt.HTTPStatus, ErrorCode: attempt.ErrorCode,
		}); err != nil {
			attempt.StateUpdateFailed = true
		}
	}
	if disposition.PenalizeTarget {
		_, _, err := service.health.ApplyTargetHealthEvent(stateCtx, int64(candidate.Target.ID), policy, routing.HealthEvent{
			Revision: candidate.Target.Revision, Sequence: sequence, OccurredAt: finishedAt,
			Outcome: routing.HealthFailure, IncidentID: fmt.Sprintf("%s:%d", requestID, attempt.Index), Failure: failure,
		})
		if err != nil {
			attempt.StateUpdateFailed = true
		}
	}
}

func (service *Service) finishSuccess(
	ctx context.Context,
	policy routing.HealthPolicy,
	metadata resolver.EndpointMetadata,
	candidate routing.Candidate,
	sequence uint64,
	attempt *Attempt,
) {
	stateCtx, cancel := service.stateUpdateContext(ctx)
	defer cancel()
	finishedAt := service.now().UTC()
	attempt.FinishedAt = finishedAt
	attempt.Outcome = "succeeded"
	_, _, err := service.health.ApplyTargetHealthEvent(stateCtx, int64(candidate.Target.ID), policy, routing.HealthEvent{
		Revision: candidate.Target.Revision, Sequence: sequence, OccurredAt: finishedAt, Outcome: routing.HealthSuccess,
	})
	if err != nil {
		attempt.StateUpdateFailed = true
	}
}

func (service *Service) stateUpdateContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx != nil && ctx.Err() == nil {
		return ctx, func() {}
	}
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(base, service.accountingTimeout)
}

func readResponseBody(body io.Reader, limit int64) ([]byte, error) {
	if body == nil {
		return nil, errors.New("upstream response body is missing")
	}
	value, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, errors.New("upstream response body could not be read")
	}
	if int64(len(value)) > limit {
		return nil, errors.New("upstream response body exceeds the gateway limit")
	}
	return value, nil
}

var fallbackRequestSequence atomic.Uint64

func normalizedRequestID(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err == nil {
		return "req_" + hex.EncodeToString(random)
	}
	return fmt.Sprintf("req_fallback_%d_%d", time.Now().UTC().UnixNano(), fallbackRequestSequence.Add(1))
}
