package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/pricing"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	"github.com/LuTianTian001/JieShan/internal/vnext/resolver"
	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

type AccountingRepository interface {
	StartRequestWithQuotaReservation(context.Context, vnextstore.RequestStart) (vnextstore.RequestStartResult, error)
	RecordRequestAttempt(context.Context, vnextstore.RequestAttemptWrite) error
	MarkRequestRouteCandidateSkipped(context.Context, string, int64, string) error
	SettleRequest(context.Context, string, vnextstore.RequestSettlement) (vnextstore.RequestSettlementResult, error)
}

type PriceBook interface {
	Quote(string, pricing.Usage) (pricing.Quote, error)
	Charge(string, string, pricing.Usage) (pricing.Charge, error)
}

type requestAccounting struct {
	startedAt            time.Time
	quote                pricing.Quote
	plan                 ReservationPlan
	reservationNanoUSD   int64
	billingMultiplierBPS int
	lastAttemptIndex     *int
	lastAttempt          *Attempt
	settlementComplete   bool
}

func requestRouteCandidateSnapshots(resolution resolver.Resolution, at time.Time) ([]vnextstore.RequestRouteCandidateWrite, error) {
	targets := resolution.Plan.Targets()
	items := make([]vnextstore.RequestRouteCandidateWrite, 0, len(targets))
	for _, target := range targets {
		metadata, ok := resolution.Endpoints[target.ID]
		if !ok {
			return nil, fmt.Errorf("target %d endpoint metadata is missing", target.ID)
		}
		credentials := make([]vnextstore.RequestRouteCredentialSnapshot, 0, len(target.Credentials))
		eligibleCredentials := 0
		for _, credential := range target.Credentials {
			state := credential.State
			if state == "" {
				state = routing.CredentialReady
			}
			name := strings.TrimSpace(metadata.CredentialNames[credential.ID])
			if name == "" {
				name = fmt.Sprintf("API Key #%d", credential.ID)
			}
			var coolingUntil *int64
			if !credential.CooldownUntil.IsZero() {
				value := credential.CooldownUntil.UnixMilli()
				coolingUntil = &value
			}
			credentials = append(credentials, vnextstore.RequestRouteCredentialSnapshot{
				ID: int64(credential.ID), Name: name, Position: credential.Position,
				RuntimeState: string(state), CoolingUntil: coolingUntil,
			})
			if routeCredentialEligible(credential, at) {
				eligibleCredentials++
			}
		}
		initialEligibility := "eligible"
		initialReason := "ready"
		eligibility := routing.EvaluateTarget(resolution.Health[target.ID], target.Revision, at)
		switch {
		case !target.Enabled:
			initialEligibility, initialReason = "skipped", "target_disabled"
		case !eligibility.Eligible:
			initialEligibility, initialReason = "skipped", string(eligibility.Reason)
		case eligibleCredentials == 0:
			initialEligibility, initialReason = "skipped", "no_eligible_credentials"
		case eligibility.Mode == routing.PermitHalfOpen:
			initialReason = "half_open"
		}
		items = append(items, vnextstore.RequestRouteCandidateWrite{
			Position: target.Position, PublishedModelTargetID: metadata.PublishedModelTargetID,
			PublishedModelTargetRevision: metadata.PublishedModelTargetRevision,
			ProviderModelTargetID:        int64(target.ID), ProviderModelTargetRevision: int64(target.Revision),
			SiteID: metadata.SiteID, SiteName: metadata.SiteName,
			EndpointID: metadata.EndpointID, EndpointName: metadata.EndpointName,
			SourceModel: metadata.SourceModel, WireProtocol: string(metadata.Protocol), APISurface: string(metadata.Surface),
			Credentials: credentials, InitialEligibility: initialEligibility, InitialReason: initialReason,
		})
	}
	if len(items) == 0 {
		return nil, errors.New("effective route contains no candidates")
	}
	return items, nil
}

func routeCredentialEligible(credential routing.Credential, at time.Time) bool {
	if !credential.Enabled {
		return false
	}
	switch credential.State {
	case "", routing.CredentialReady:
		return true
	case routing.CredentialCooling:
		return !credential.CooldownUntil.After(at)
	default:
		return false
	}
}

func (service *Service) startAccounting(
	ctx context.Context,
	input Input,
	publishedModelID int64,
	publishedModelRevision int64,
	effectiveProfileID int64,
	effectiveProfileName string,
	sourceProfileID int64,
	sourceProfileName string,
	routeRevision int64,
	routeCandidates []vnextstore.RequestRouteCandidateWrite,
	downstreamKeyID int64,
	officialPriceSKU string,
	startedAt time.Time,
) (*requestAccounting, error) {
	plan, err := service.planner.PlanReservation(ctx, ReservationInput{
		Protocol:               input.IngressProtocol,
		Surface:                input.IngressSurface,
		Payload:                append([]byte(nil), input.Payload...),
		DefaultMaxOutputTokens: service.defaultMaxOutputTokens,
	})
	if err != nil {
		if errors.Is(err, ErrInvalidRequest) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: plan quota reservation", ErrRuntimeUnavailable)
	}
	if err := validateReservationPlan(plan); err != nil {
		return nil, fmt.Errorf("%w: invalid reservation plan: %v", ErrRuntimeUnavailable, err)
	}

	quote, err := service.prices.Quote(officialPriceSKU, clonePricingUsage(plan.MaximumUsage))
	if err != nil {
		return nil, fmt.Errorf("%w: quote official price", ErrPricingUnavailable)
	}
	if quote.ReservationNanoUSD <= 0 || strings.TrimSpace(quote.CatalogVersion) == "" || strings.TrimSpace(quote.SKU) == "" {
		return nil, fmt.Errorf("%w: invalid official price quote", ErrPricingUnavailable)
	}

	startResult, err := service.accounting.StartRequestWithQuotaReservation(ctx, vnextstore.RequestStart{
		ID: normalizedRequestID(input.RequestID), DownstreamKeyID: downstreamKeyID,
		PublishedModelID: publishedModelID, PublishedModelRevision: publishedModelRevision,
		EffectiveRoutingProfileID: effectiveProfileID, EffectiveRoutingProfileName: effectiveProfileName,
		SourceRoutingProfileID: sourceProfileID, SourceRoutingProfileName: sourceProfileName,
		RouteRevision: routeRevision, RouteCandidates: routeCandidates,
		PublicModel:          strings.TrimSpace(input.PublicModel),
		APISurface:           string(input.IngressSurface),
		ReasoningEffort:      plan.ReasoningEffort,
		ThinkingBudgetTokens: cloneInt64Pointer(plan.ThinkingBudgetTokens),
		Stream:               input.Stream,
		PriceCatalogVersion:  quote.CatalogVersion,
		PriceSKU:             quote.SKU,
		ReservationNanoUSD:   quote.ReservationNanoUSD,
		StartedAt:            startedAt.UnixMilli(),
	})
	if err != nil {
		if errors.Is(err, vnextstore.ErrQuotaExceeded) {
			return nil, ErrQuotaExceeded
		}
		return nil, fmt.Errorf("%w: reserve downstream quota", ErrRuntimeUnavailable)
	}
	if startResult.AlreadyStarted {
		return nil, ErrRequestAlreadyStarted
	}
	if startResult.ReservationNanoUSD < 0 || startResult.BillingMultiplierBPS < 0 {
		return nil, fmt.Errorf("%w: invalid downstream billing snapshot", ErrRuntimeUnavailable)
	}
	return &requestAccounting{
		startedAt: startedAt, quote: quote, plan: plan,
		reservationNanoUSD:   startResult.ReservationNanoUSD,
		billingMultiplierBPS: startResult.BillingMultiplierBPS,
	}, nil
}

func (service *Service) recordAttempt(ctx context.Context, requestID string, attempt Attempt) error {
	if attempt.FinishedAt.IsZero() || attempt.StartedAt.IsZero() {
		return errors.New("attempt timing is incomplete")
	}
	status := "failed"
	switch attempt.Outcome {
	case "succeeded":
		status = "success"
	case "cancelled":
		status = "cancelled"
	}
	var httpStatus *int
	if attempt.HTTPStatus > 0 {
		value := attempt.HTTPStatus
		httpStatus = &value
	}
	write := vnextstore.RequestAttemptWrite{
		RequestID: requestID, AttemptIndex: attempt.Index,
		PublishedModelTargetID:       attempt.PublishedModelTargetID,
		PublishedModelTargetRevision: attempt.PublishedModelTargetRevision,
		ProviderModelTargetID:        int64(attempt.TargetID),
		ProviderModelTargetRevision:  int64(attempt.TargetRevision),
		SiteID:                       attempt.SiteID,
		EndpointID:                   attempt.EndpointID,
		CredentialID:                 int64(attempt.CredentialID),
		SiteName:                     attempt.SiteName,
		EndpointName:                 attempt.EndpointName,
		CredentialName:               attempt.CredentialName,
		SourceModel:                  attempt.SourceModel,
		ResponseModel:                attempt.ResponseModel,
		WireProtocol:                 string(attempt.WireProtocol),
		APISurface:                   string(attempt.Surface),
		Status:                       status,
		HTTPStatus:                   httpStatus,
		FailureKind:                  accountingCode(string(attempt.FailureKind), "unknown"),
		ErrorCode:                    accountingCode(attempt.ErrorCode, "attempt_failed"),
		SwitchReason:                 accountingCode(attempt.SwitchReason, ""),
		FirstTokenMS:                 cloneInt64Pointer(attempt.FirstTokenMS),
		DurationMS:                   elapsedMilliseconds(attempt.StartedAt, attempt.FinishedAt),
		StartedAt:                    attempt.StartedAt.UnixMilli(),
		FinishedAt:                   attempt.FinishedAt.UnixMilli(),
	}
	if status == "success" {
		write.FailureKind = ""
		write.ErrorCode = ""
		write.SwitchReason = ""
	}
	accountingCtx, cancel := service.detachedAccountingContext(ctx)
	defer cancel()
	return service.accounting.RecordRequestAttempt(accountingCtx, write)
}

func (service *Service) markRouteCandidateSkipped(ctx context.Context, requestID string, targetID routing.TargetID, reason string) error {
	accountingCtx, cancel := service.detachedAccountingContext(ctx)
	defer cancel()
	return service.accounting.MarkRequestRouteCandidateSkipped(
		accountingCtx, requestID, int64(targetID), accountingCode(reason, "candidate_skipped"),
	)
}

func (service *Service) settleAccounting(
	ctx context.Context,
	state *requestAccounting,
	result *Result,
	executionErr error,
) error {
	if state == nil || state.settlementComplete {
		return nil
	}
	finishedAt := service.now().UTC()
	status := "success"
	errorCode := ""
	if executionErr != nil {
		status = "failed"
		errorCode = settlementErrorCode(executionErr, state.lastAttempt)
		if isCancellation(executionErr, ctx) {
			status = "cancelled"
		}
	}

	usage := protocolUsageToPricing(result.Usage)
	charge := pricing.Charge{CatalogVersion: state.quote.CatalogVersion, SKU: state.quote.SKU}
	var chargeErr error
	meteringStatus := "not_applicable"
	meteringErrorCode := ""
	if len(usage) > 0 {
		charge, chargeErr = service.prices.Charge(state.quote.CatalogVersion, state.quote.SKU, usage)
		if chargeErr != nil {
			meteringStatus = "unavailable"
			meteringErrorCode = "pricing_settlement_failed"
			charge = pricing.Charge{CatalogVersion: state.quote.CatalogVersion, SKU: state.quote.SKU}
		} else {
			meteringStatus = "metered"
		}
	} else if status == "success" {
		meteringStatus = "unavailable"
		meteringErrorCode = "usage_unavailable"
	}
	result.MeteringStatus = meteringStatus
	result.MeteringErrorCode = meteringErrorCode

	settlement := vnextstore.RequestSettlement{
		Status:              status,
		MeteringStatus:      meteringStatus,
		MeteringErrorCode:   meteringErrorCode,
		UnattemptedReason:   routeCompletionReason(status, executionErr),
		FinalAttemptIndex:   cloneIntPointer(state.lastAttemptIndex),
		DurationMS:          elapsedMilliseconds(state.startedAt, finishedAt),
		InputTokens:         cloneInt64Pointer(result.Usage.InputTokens),
		OutputTokens:        cloneInt64Pointer(result.Usage.OutputTokens),
		CacheReadTokens:     cloneInt64Pointer(result.Usage.CacheReadTokens),
		CacheWriteTokens:    cloneInt64Pointer(result.Usage.CacheWriteTokens),
		CacheWrite5MTokens:  cloneInt64Pointer(result.Usage.CacheWrite5MTokens),
		CacheWrite1HTokens:  cloneInt64Pointer(result.Usage.CacheWrite1HTokens),
		ReasoningTokens:     cloneInt64Pointer(result.Usage.ReasoningTokens),
		OfficialCostNanoUSD: charge.NanoUSD,
		ErrorCode:           accountingCode(errorCode, "request_failed"),
		FinishedAt:          finishedAt.UnixMilli(),
	}
	if state.lastAttempt != nil {
		if state.lastAttempt.HTTPStatus > 0 {
			value := state.lastAttempt.HTTPStatus
			settlement.HTTPStatus = &value
		}
		if result.Stream && state.lastAttempt.FirstTokenMS != nil {
			value := elapsedMilliseconds(state.startedAt, state.lastAttempt.StartedAt) + *state.lastAttempt.FirstTokenMS
			settlement.FirstTokenMS = &value
		}
	}
	if status == "success" {
		settlement.ErrorCode = ""
	}
	if meteringStatus != "metered" {
		settlement.InputTokens = nil
		settlement.OutputTokens = nil
		settlement.CacheReadTokens = nil
		settlement.CacheWriteTokens = nil
		settlement.CacheWrite5MTokens = nil
		settlement.CacheWrite1HTokens = nil
		settlement.ReasoningTokens = nil
		settlement.OfficialCostNanoUSD = 0
	}

	accountingCtx, cancel := service.detachedAccountingContext(ctx)
	defer cancel()
	settled, settleErr := service.accounting.SettleRequest(accountingCtx, result.RequestID, settlement)
	if settleErr != nil {
		if chargeErr != nil {
			return errors.Join(fmt.Errorf("%w: calculate frozen official charge", ErrPricingUnavailable), settleErr)
		}
		return settleErr
	}
	state.settlementComplete = true
	result.PriceCatalogVersion = state.quote.CatalogVersion
	result.PriceSKU = state.quote.SKU
	result.ReservationNanoUSD = state.reservationNanoUSD
	result.OfficialCostNanoUSD = charge.NanoUSD
	result.ChargedNanoUSD = settled.ChargedNanoUSD
	result.QuotaCapped = settled.QuotaCapped
	return nil
}

func routeCompletionReason(status string, err error) string {
	switch {
	case status == "success":
		return "request_succeeded"
	case status == "cancelled":
		return "request_cancelled"
	case errors.Is(err, ErrRequestTimeout):
		return "request_timeout"
	case errors.Is(err, ErrNoAvailableUpstream):
		return "candidates_exhausted"
	case errors.Is(err, ErrRuntimeUnavailable):
		return "runtime_unavailable"
	default:
		return "request_failed"
	}
}

func (service *Service) detachedAccountingContext(ctx context.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(base, service.accountingTimeout)
}

func protocolUsageToPricing(usage protocol.Usage) pricing.Usage {
	result := make(pricing.Usage)
	appendUsage := func(class pricing.TokenClass, value *int64) {
		if value != nil {
			result[class] = *value
		}
	}
	appendUsage(pricing.TokenInput, usage.InputTokens)
	appendUsage(pricing.TokenOutput, usage.OutputTokens)
	appendUsage(pricing.TokenCacheRead, usage.CacheReadTokens)
	if usage.CacheWrite5MTokens != nil || usage.CacheWrite1HTokens != nil {
		appendUsage(pricing.TokenCacheWrite5m, usage.CacheWrite5MTokens)
		appendUsage(pricing.TokenCacheWrite1h, usage.CacheWrite1HTokens)
	} else {
		appendUsage(pricing.TokenCacheWrite, usage.CacheWriteTokens)
	}
	appendUsage(pricing.TokenReasoning, usage.ReasoningTokens)
	return result
}

func settlementErrorCode(err error, attempt *Attempt) string {
	if attempt != nil && strings.TrimSpace(attempt.ErrorCode) != "" {
		return attempt.ErrorCode
	}
	switch {
	case errors.Is(err, ErrRequestTimeout):
		return "request_timeout"
	case errors.Is(err, ErrFirstOutputTimeout):
		return "first_output_timeout"
	case errors.Is(err, ErrStreamIdleTimeout):
		return "stream_idle_timeout"
	case errors.Is(err, ErrDownstreamClosed), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "downstream_canceled"
	case errors.Is(err, ErrInvalidRequest):
		return "invalid_request"
	case errors.Is(err, ErrCommittedStreamFailed):
		return "stream_incomplete"
	case errors.Is(err, ErrNoAvailableUpstream):
		return "no_available_upstream"
	default:
		return "gateway_runtime_error"
	}
}

func isCancellation(err error, ctx context.Context) bool {
	if errors.Is(err, ErrRequestTimeout) || errors.Is(err, ErrFirstOutputTimeout) || errors.Is(err, ErrStreamIdleTimeout) ||
		(ctx != nil && errors.Is(context.Cause(ctx), ErrRequestTimeout)) {
		return false
	}
	return errors.Is(err, ErrDownstreamClosed) || errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) || (ctx != nil && ctx.Err() != nil)
}

func elapsedMilliseconds(start, finish time.Time) int64 {
	if finish.Before(start) {
		return 0
	}
	return finish.Sub(start).Milliseconds()
}

func cloneInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func accountingCode(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, character := range value {
		allowed := character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			character == '.' || character == '_' || character == ':' || character == '-'
		if allowed {
			builder.WriteRune(character)
		} else {
			builder.WriteByte('_')
		}
		if builder.Len() == 128 {
			break
		}
	}
	value = strings.Trim(builder.String(), "._:-")
	if value == "" {
		return fallback
	}
	return value
}

var _ AccountingRepository = (*vnextstore.Store)(nil)
var _ PriceBook = (*pricing.Book)(nil)
