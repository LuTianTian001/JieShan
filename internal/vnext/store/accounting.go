package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
)

var (
	ErrQuotaExceeded          = errors.New("downstream key quota exceeded")
	ErrRequestConflict        = errors.New("request ID conflicts with different immutable data")
	ErrRequestNotRunning      = errors.New("request is not running")
	ErrSettlementConflict     = errors.New("request is already settled with different data")
	ErrAttemptConflict        = errors.New("request attempt conflicts with an existing attempt")
	ErrInvalidAccountingState = errors.New("invalid downstream accounting state")
)

var accountingCodePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,127}$`)

type RequestStart struct {
	ID                          string
	DownstreamKeyID             int64
	PublishedModelID            int64
	PublishedModelRevision      int64
	EffectiveRoutingProfileID   int64
	EffectiveRoutingProfileName string
	SourceRoutingProfileID      int64
	SourceRoutingProfileName    string
	RouteRevision               int64
	RouteCandidates             []RequestRouteCandidateWrite
	PublicModel                 string
	APISurface                  string
	ReasoningEffort             string
	ThinkingBudgetTokens        *int64
	Stream                      bool
	PriceCatalogVersion         string
	PriceSKU                    string
	ReservationNanoUSD          int64
	StartedAt                   int64
}

type RequestStartResult struct {
	AlreadyStarted       bool
	ReservationNanoUSD   int64
	BillingMultiplierBPS int
}

type RequestSettlement struct {
	Status              string
	MeteringStatus      string
	MeteringErrorCode   string
	UnattemptedReason   string
	FinalAttemptIndex   *int
	HTTPStatus          *int
	FirstTokenMS        *int64
	DurationMS          int64
	InputTokens         *int64
	OutputTokens        *int64
	CacheReadTokens     *int64
	CacheWriteTokens    *int64
	CacheWrite5MTokens  *int64
	CacheWrite1HTokens  *int64
	ReasoningTokens     *int64
	OfficialCostNanoUSD int64
	ErrorCode           string
	FinishedAt          int64
}

type RequestSettlementResult struct {
	ChargedNanoUSD int64
	QuotaCapped    bool
	AlreadySettled bool
}

type RequestLog struct {
	ID                          string
	DownstreamKeyID             int64
	DownstreamKeyName           string
	PublishedModelID            int64
	PublishedModelRevision      int64
	EffectiveRoutingProfileID   int64
	EffectiveRoutingProfileName string
	SourceRoutingProfileID      int64
	SourceRoutingProfileName    string
	RouteRevision               int64
	PublicModel                 string
	APISurface                  string
	ReasoningEffort             string
	ThinkingBudgetTokens        *int64
	Stream                      bool
	PriceCatalogVersion         string
	PriceSKU                    string
	BillingMultiplierBPS        int
	ReservationNanoUSD          int64
	Status                      string
	MeteringStatus              string
	MeteringErrorCode           string
	FinalAttemptIndex           *int
	HTTPStatus                  *int
	FirstTokenMS                *int64
	DurationMS                  *int64
	InputTokens                 *int64
	OutputTokens                *int64
	CacheReadTokens             *int64
	CacheWriteTokens            *int64
	CacheWrite5MTokens          *int64
	CacheWrite1HTokens          *int64
	ReasoningTokens             *int64
	OfficialCostNanoUSD         int64
	ChargedNanoUSD              int64
	QuotaCapped                 bool
	ErrorCode                   string
	StartedAt                   int64
	FinishedAt                  *int64
	FinalAttempt                *RequestAttempt
}

type RequestRouteCredentialSnapshot struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Position     int    `json:"position"`
	RuntimeState string `json:"runtimeState"`
	CoolingUntil *int64 `json:"coolingUntil"`
}

type RequestRouteCandidateWrite struct {
	Position                     int
	PublishedModelTargetID       int64
	PublishedModelTargetRevision int64
	ProviderModelTargetID        int64
	ProviderModelTargetRevision  int64
	SiteID                       int64
	SiteName                     string
	EndpointID                   int64
	EndpointName                 string
	SourceModel                  string
	WireProtocol                 string
	APISurface                   string
	Credentials                  []RequestRouteCredentialSnapshot
	InitialEligibility           string
	InitialReason                string
}

type RequestRouteCandidate struct {
	RequestRouteCandidateWrite
	RequestID         string
	Disposition       string
	DispositionReason string
	AttemptCount      int
	FirstAttemptIndex *int
	LastAttemptIndex  *int
}

type RequestAttemptWrite struct {
	RequestID                    string
	AttemptIndex                 int
	PublishedModelTargetID       int64
	PublishedModelTargetRevision int64
	ProviderModelTargetID        int64
	ProviderModelTargetRevision  int64
	SiteID                       int64
	EndpointID                   int64
	CredentialID                 int64
	SiteName                     string
	EndpointName                 string
	CredentialName               string
	SourceModel                  string
	ResponseModel                string
	WireProtocol                 string
	APISurface                   string
	Status                       string
	HTTPStatus                   *int
	FailureKind                  string
	ErrorCode                    string
	SwitchReason                 string
	FirstTokenMS                 *int64
	DurationMS                   int64
	StartedAt                    int64
	FinishedAt                   int64
}

type RequestAttempt struct {
	ID                           int64
	RequestID                    string
	AttemptIndex                 int
	PublishedModelTargetID       int64
	PublishedModelTargetRevision int64
	ProviderModelTargetID        int64
	ProviderModelTargetRevision  int64
	SiteID                       int64
	EndpointID                   int64
	CredentialID                 int64
	SiteName                     string
	EndpointName                 string
	CredentialName               string
	SourceModel                  string
	ResponseModel                string
	WireProtocol                 string
	APISurface                   string
	Status                       string
	HTTPStatus                   *int
	FailureKind                  string
	ErrorCode                    string
	SwitchReason                 string
	FirstTokenMS                 *int64
	DurationMS                   int64
	StartedAt                    int64
	FinishedAt                   int64
}

type QuotaLedgerEntry struct {
	ID                   int64
	DownstreamKeyID      int64
	RequestID            string
	EventType            string
	ReservedDeltaNanoUSD int64
	UsedDeltaNanoUSD     int64
	PriceCatalogVersion  string
	PriceSKU             string
	CreatedAt            int64
}

// StartRequestWithQuotaReservation atomically admits a request, reserves its
// maximum charge, freezes catalog identity, and creates the running log row.
func (s *Store) StartRequestWithQuotaReservation(ctx context.Context, input RequestStart) (RequestStartResult, error) {
	input = normalizeRequestStart(input)
	if err := validateRequestStart(input); err != nil {
		return RequestStartResult{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return RequestStartResult{}, err
	}
	defer tx.Rollback()

	existing, err := getRequestLog(ctx, tx, input.ID)
	if err == nil {
		matches, matchErr := sameRequestStartWithCandidates(ctx, tx, existing, input)
		if matchErr != nil {
			return RequestStartResult{}, matchErr
		}
		if matches {
			return RequestStartResult{
				AlreadyStarted: true, ReservationNanoUSD: existing.ReservationNanoUSD,
				BillingMultiplierBPS: existing.BillingMultiplierBPS,
			}, nil
		}
		return RequestStartResult{}, ErrRequestConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RequestStartResult{}, err
	}

	var keyName string
	var hourlyQuota sql.NullInt64
	var billingMultiplierBPS int
	reserveErr := tx.QueryRowContext(ctx, `UPDATE downstream_keys SET updated_at=updated_at
WHERE id=? AND enabled=1 AND (expires_at IS NULL OR expires_at>?)
RETURNING name,hourly_quota_nano_usd,billing_multiplier_bps`, input.DownstreamKeyID, input.StartedAt).
		Scan(&keyName, &hourlyQuota, &billingMultiplierBPS)
	if errors.Is(reserveErr, sql.ErrNoRows) {
		_ = tx.Rollback()
		return s.classifyStartAfterRollback(ctx, input, ErrQuotaExceeded)
	}
	if reserveErr != nil {
		return RequestStartResult{}, reserveErr
	}
	reservationNanoUSD, err := scaleNanoUSD(input.ReservationNanoUSD, billingMultiplierBPS, true)
	if err != nil {
		return RequestStartResult{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE downstream_keys SET
reserved_nano_usd=reserved_nano_usd+?,last_used_at=?,updated_at=?
WHERE id=? AND reserved_nano_usd<=?-?
  AND (quota_nano_usd IS NULL OR
    (used_nano_usd<=quota_nano_usd
      AND reserved_nano_usd<=quota_nano_usd-used_nano_usd
      AND ?<=quota_nano_usd-used_nano_usd-reserved_nano_usd))`,
		reservationNanoUSD, input.StartedAt, input.StartedAt, input.DownstreamKeyID,
		int64(math.MaxInt64), reservationNanoUSD, reservationNanoUSD)
	if err != nil {
		return RequestStartResult{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return RequestStartResult{}, err
	}
	if changed != 1 {
		_ = tx.Rollback()
		return s.classifyStartAfterRollback(ctx, input, ErrQuotaExceeded)
	}
	hourWindow, err := hourlyWindowStart(input.StartedAt)
	if err != nil {
		return RequestStartResult{}, err
	}
	if err := reserveHourlyUsageTx(ctx, tx, input.DownstreamKeyID, hourWindow, input.StartedAt,
		hourlyQuota, reservationNanoUSD); err != nil {
		if errors.Is(err, ErrQuotaExceeded) {
			_ = tx.Rollback()
			return s.classifyStartAfterRollback(ctx, input, ErrQuotaExceeded)
		}
		return RequestStartResult{}, err
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO request_logs(
id,downstream_key_id,downstream_key_name_snapshot,published_model_id,published_model_revision,
effective_routing_profile_id,effective_routing_profile_name_snapshot,
source_routing_profile_id,source_routing_profile_name_snapshot,
route_revision,public_model,api_surface,reasoning_effort,thinking_budget_tokens,is_stream,price_catalog_version,
price_sku,billing_multiplier_bps,reservation_nano_usd,status,started_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'running',?)`, input.ID, input.DownstreamKeyID, keyName,
		input.PublishedModelID, input.PublishedModelRevision,
		input.EffectiveRoutingProfileID, input.EffectiveRoutingProfileName,
		input.SourceRoutingProfileID, input.SourceRoutingProfileName, input.RouteRevision, input.PublicModel, input.APISurface,
		nullableString(input.ReasoningEffort), input.ThinkingBudgetTokens, boolInt(input.Stream),
		input.PriceCatalogVersion, input.PriceSKU, billingMultiplierBPS, reservationNanoUSD, input.StartedAt)
	if err != nil {
		_ = tx.Rollback()
		if isUniqueConstraintError(err) {
			return s.classifyStartAfterRollback(ctx, input, ErrRequestConflict)
		}
		return RequestStartResult{}, err
	}
	for _, candidate := range input.RouteCandidates {
		credentialsJSON, marshalErr := json.Marshal(candidate.Credentials)
		if marshalErr != nil {
			return RequestStartResult{}, marshalErr
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO request_route_candidates(
request_id,position,published_model_target_id,published_model_target_revision,
provider_model_target_id,provider_model_target_revision,site_id,site_name_snapshot,endpoint_id,
endpoint_name_snapshot,source_model,wire_protocol,api_surface,credentials_json,
initial_eligibility,initial_reason,disposition,disposition_reason)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, input.ID, candidate.Position,
			candidate.PublishedModelTargetID, candidate.PublishedModelTargetRevision,
			candidate.ProviderModelTargetID, candidate.ProviderModelTargetRevision,
			candidate.SiteID, candidate.SiteName, candidate.EndpointID, candidate.EndpointName,
			candidate.SourceModel, candidate.WireProtocol, candidate.APISurface, string(credentialsJSON),
			candidate.InitialEligibility, candidate.InitialReason,
			initialCandidateDisposition(candidate), initialCandidateDispositionReason(candidate)); err != nil {
			return RequestStartResult{}, err
		}
	}
	if reservationNanoUSD > 0 {
		if err := insertQuotaLedgerTx(ctx, tx, QuotaLedgerEntry{
			DownstreamKeyID: input.DownstreamKeyID, RequestID: input.ID, EventType: "reserve",
			ReservedDeltaNanoUSD: reservationNanoUSD, PriceCatalogVersion: input.PriceCatalogVersion,
			PriceSKU: input.PriceSKU, CreatedAt: input.StartedAt,
		}); err != nil {
			return RequestStartResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return RequestStartResult{}, err
	}
	return RequestStartResult{
		ReservationNanoUSD: reservationNanoUSD, BillingMultiplierBPS: billingMultiplierBPS,
	}, nil
}

func (s *Store) classifyStartAfterRollback(ctx context.Context, input RequestStart, fallback error) (RequestStartResult, error) {
	existing, err := s.GetRequestLog(ctx, input.ID)
	if err == nil {
		matches, matchErr := sameRequestStartWithCandidates(ctx, s.DB, existing, input)
		if matchErr != nil {
			return RequestStartResult{}, matchErr
		}
		if matches {
			return RequestStartResult{
				AlreadyStarted: true, ReservationNanoUSD: existing.ReservationNanoUSD,
				BillingMultiplierBPS: existing.BillingMultiplierBPS,
			}, nil
		}
		return RequestStartResult{}, ErrRequestConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RequestStartResult{}, err
	}
	return RequestStartResult{}, fallback
}

func reserveHourlyUsageTx(
	ctx context.Context,
	tx *sql.Tx,
	downstreamKeyID, windowStartedAt, updatedAt int64,
	quota sql.NullInt64,
	reservation int64,
) error {
	var used, reserved int64
	err := tx.QueryRowContext(ctx, `SELECT used_nano_usd,reserved_nano_usd
FROM downstream_key_hourly_usage WHERE downstream_key_id=? AND window_started_at=?`,
		downstreamKeyID, windowStartedAt).Scan(&used, &reserved)
	if errors.Is(err, sql.ErrNoRows) {
		if quota.Valid && reservation > quota.Int64 {
			return ErrQuotaExceeded
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO downstream_key_hourly_usage(
downstream_key_id,window_started_at,used_nano_usd,reserved_nano_usd,updated_at)
VALUES (?,?,0,?,?)`, downstreamKeyID, windowStartedAt, reservation, updatedAt)
		return err
	}
	if err != nil {
		return err
	}
	if used < 0 || reserved < 0 || reserved > math.MaxInt64-reservation {
		return ErrInvalidAccountingState
	}
	if quota.Valid {
		if used > quota.Int64 || reserved > quota.Int64-used || reservation > quota.Int64-used-reserved {
			return ErrQuotaExceeded
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE downstream_key_hourly_usage SET
reserved_nano_usd=reserved_nano_usd+?,updated_at=?
WHERE downstream_key_id=? AND window_started_at=? AND used_nano_usd=? AND reserved_nano_usd=?`,
		reservation, updatedAt, downstreamKeyID, windowStartedAt, used, reserved)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrInvalidAccountingState
	}
	return nil
}

// RecoverInterruptedRequests settles every request left running by a previous
// process. JieShan is a single-instance SQLite deployment, so no live request
// exists before Runtime.Open starts serving. Recovery is idempotent because
// SettleRequest owns the request-level transaction and unique settle ledger.
func (s *Store) RecoverInterruptedRequests(ctx context.Context, finishedAt int64) (int, error) {
	if s == nil || s.DB == nil {
		return 0, errors.New("store is unavailable")
	}
	if finishedAt <= 0 {
		return 0, errors.New("interrupted request recovery time is invalid")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT r.id,r.started_at,MAX(a.attempt_index)
FROM request_logs r
LEFT JOIN request_attempts a ON a.request_id=r.id
WHERE r.status='running' AND r.started_at<=?
GROUP BY r.id,r.started_at
ORDER BY r.started_at,r.id`, finishedAt)
	if err != nil {
		return 0, err
	}
	type interruptedRequest struct {
		id           string
		startedAt    int64
		finalAttempt *int
	}
	requests := make([]interruptedRequest, 0)
	for rows.Next() {
		var item interruptedRequest
		var finalAttempt sql.NullInt64
		if err := rows.Scan(&item.id, &item.startedAt, &finalAttempt); err != nil {
			rows.Close()
			return 0, err
		}
		item.finalAttempt = nullIntPointer(finalAttempt)
		requests = append(requests, item)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	recovered := 0
	for _, item := range requests {
		duration := finishedAt - item.startedAt
		if duration < 0 {
			duration = 0
		}
		_, err := s.SettleRequest(ctx, item.id, RequestSettlement{
			Status: "failed", MeteringStatus: "not_applicable", UnattemptedReason: "runtime_interrupted",
			FinalAttemptIndex: item.finalAttempt, DurationMS: duration,
			OfficialCostNanoUSD: 0, ErrorCode: "runtime_interrupted", FinishedAt: finishedAt,
		})
		if err != nil {
			return recovered, fmt.Errorf("recover interrupted request %q: %w", item.id, err)
		}
		recovered++
	}
	return recovered, nil
}

// SettleRequest serializes settlement for one request, releases its complete
// reservation, applies the charge allowed by the key, and writes one ledger
// delta. Replaying the exact same settlement is a no-op success.
func (s *Store) SettleRequest(ctx context.Context, requestID string, input RequestSettlement) (RequestSettlementResult, error) {
	requestID = strings.TrimSpace(requestID)
	input = normalizeRequestSettlement(input)
	if requestID == "" {
		return RequestSettlementResult{}, errors.New("request ID is required")
	}
	if err := validateRequestSettlement(input); err != nil {
		return RequestSettlementResult{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return RequestSettlementResult{}, err
	}
	defer tx.Rollback()

	request, err := scanRequestLog(tx.QueryRowContext(ctx, `UPDATE request_logs SET status=status
WHERE id=? AND status='running' RETURNING `+requestLogColumns, requestID))
	if errors.Is(err, sql.ErrNoRows) {
		request, err = getRequestLog(ctx, tx, requestID)
		if err != nil {
			return RequestSettlementResult{}, err
		}
		if !sameRequestSettlement(request, input) {
			return RequestSettlementResult{}, ErrSettlementConflict
		}
		return RequestSettlementResult{
			ChargedNanoUSD: request.ChargedNanoUSD,
			QuotaCapped:    request.QuotaCapped,
			AlreadySettled: true,
		}, nil
	}
	if err != nil {
		return RequestSettlementResult{}, err
	}
	if input.FinishedAt < request.StartedAt {
		return RequestSettlementResult{}, errors.New("request finish time precedes start time")
	}
	if !request.Stream && input.FirstTokenMS != nil {
		return RequestSettlementResult{}, errors.New("first-token latency is valid only for streaming requests")
	}
	if input.FinalAttemptIndex != nil {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM request_attempts WHERE request_id=? AND attempt_index=?`,
			requestID, *input.FinalAttemptIndex).Scan(&exists); err != nil {
			return RequestSettlementResult{}, err
		}
	}

	var quota, hourlyQuota sql.NullInt64
	var used, totalReserved int64
	if err := tx.QueryRowContext(ctx, `SELECT quota_nano_usd,hourly_quota_nano_usd,used_nano_usd,reserved_nano_usd
FROM downstream_keys WHERE id=?`, request.DownstreamKeyID).Scan(&quota, &hourlyQuota, &used, &totalReserved); err != nil {
		return RequestSettlementResult{}, err
	}
	if used < 0 || totalReserved < request.ReservationNanoUSD {
		return RequestSettlementResult{}, ErrInvalidAccountingState
	}
	scaledCharge, err := scaleNanoUSD(input.OfficialCostNanoUSD, request.BillingMultiplierBPS, false)
	if err != nil {
		return RequestSettlementResult{}, err
	}
	applied := scaledCharge
	quotaCapped := false
	if quota.Valid {
		if quota.Int64 < used {
			return RequestSettlementResult{}, ErrInvalidAccountingState
		}
		otherReservations := totalReserved - request.ReservationNanoUSD
		if otherReservations > quota.Int64-used {
			return RequestSettlementResult{}, ErrInvalidAccountingState
		}
		available := quota.Int64 - used - otherReservations
		if applied > available {
			applied = available
			quotaCapped = true
		}
	} else if used > math.MaxInt64-applied {
		return RequestSettlementResult{}, ErrInvalidAccountingState
	}
	hourWindow, err := hourlyWindowStart(request.StartedAt)
	if err != nil {
		return RequestSettlementResult{}, err
	}
	var hourlyUsed, hourlyReserved int64
	if err := tx.QueryRowContext(ctx, `SELECT used_nano_usd,reserved_nano_usd
FROM downstream_key_hourly_usage WHERE downstream_key_id=? AND window_started_at=?`,
		request.DownstreamKeyID, hourWindow).Scan(&hourlyUsed, &hourlyReserved); err != nil {
		return RequestSettlementResult{}, err
	}
	if hourlyUsed < 0 || hourlyReserved < request.ReservationNanoUSD {
		return RequestSettlementResult{}, ErrInvalidAccountingState
	}
	if hourlyQuota.Valid {
		if hourlyUsed > hourlyQuota.Int64 {
			return RequestSettlementResult{}, ErrInvalidAccountingState
		}
		otherReservations := hourlyReserved - request.ReservationNanoUSD
		if otherReservations > hourlyQuota.Int64-hourlyUsed {
			return RequestSettlementResult{}, ErrInvalidAccountingState
		}
		available := hourlyQuota.Int64 - hourlyUsed - otherReservations
		if applied > available {
			applied = available
			quotaCapped = true
		}
	} else if hourlyUsed > math.MaxInt64-applied {
		return RequestSettlementResult{}, ErrInvalidAccountingState
	}
	if applied < scaledCharge {
		quotaCapped = true
	}

	result, err := tx.ExecContext(ctx, `UPDATE request_logs SET
status=?,metering_status=?,metering_error_code=?,final_attempt_index=?,http_status=?,first_token_ms=?,duration_ms=?,input_tokens=?,output_tokens=?,
cache_read_tokens=?,cache_write_tokens=?,cache_write_5m_tokens=?,cache_write_1h_tokens=?,reasoning_tokens=?,official_cost_nano_usd=?,
charged_nano_usd=?,quota_capped=?,error_code=?,finished_at=?
WHERE id=? AND status='running'`, input.Status, input.MeteringStatus, nullableString(input.MeteringErrorCode),
		input.FinalAttemptIndex, input.HTTPStatus, input.FirstTokenMS, input.DurationMS,
		input.InputTokens, input.OutputTokens, input.CacheReadTokens, input.CacheWriteTokens,
		input.CacheWrite5MTokens, input.CacheWrite1HTokens, input.ReasoningTokens, input.OfficialCostNanoUSD, applied, boolInt(quotaCapped),
		nullableString(input.ErrorCode), input.FinishedAt, requestID)
	if err != nil {
		return RequestSettlementResult{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return RequestSettlementResult{}, err
	}
	if changed != 1 {
		return RequestSettlementResult{}, ErrRequestNotRunning
	}
	if _, err := tx.ExecContext(ctx, `UPDATE request_route_candidates SET
attempt_count=(SELECT COUNT(*) FROM request_attempts a
  WHERE a.request_id=request_route_candidates.request_id
    AND a.provider_model_target_id=request_route_candidates.provider_model_target_id),
first_attempt_index=(SELECT MIN(a.attempt_index) FROM request_attempts a
  WHERE a.request_id=request_route_candidates.request_id
    AND a.provider_model_target_id=request_route_candidates.provider_model_target_id),
last_attempt_index=(SELECT MAX(a.attempt_index) FROM request_attempts a
  WHERE a.request_id=request_route_candidates.request_id
    AND a.provider_model_target_id=request_route_candidates.provider_model_target_id),
disposition=CASE
  WHEN EXISTS (SELECT 1 FROM request_attempts a
    WHERE a.request_id=request_route_candidates.request_id
      AND a.provider_model_target_id=request_route_candidates.provider_model_target_id) THEN 'attempted'
  WHEN disposition='skipped' OR initial_eligibility='skipped' THEN 'skipped'
  ELSE 'not_attempted'
END,
disposition_reason=CASE
  WHEN EXISTS (SELECT 1 FROM request_attempts a
    WHERE a.request_id=request_route_candidates.request_id
      AND a.provider_model_target_id=request_route_candidates.provider_model_target_id) THEN COALESCE(
      (SELECT NULLIF(a.switch_reason,'') FROM request_attempts a
        WHERE a.request_id=request_route_candidates.request_id
          AND a.provider_model_target_id=request_route_candidates.provider_model_target_id
        ORDER BY a.attempt_index DESC,a.id DESC LIMIT 1),
      (SELECT NULLIF(a.error_code,'') FROM request_attempts a
        WHERE a.request_id=request_route_candidates.request_id
          AND a.provider_model_target_id=request_route_candidates.provider_model_target_id
        ORDER BY a.attempt_index DESC,a.id DESC LIMIT 1),
      (SELECT a.status FROM request_attempts a
        WHERE a.request_id=request_route_candidates.request_id
          AND a.provider_model_target_id=request_route_candidates.provider_model_target_id
        ORDER BY a.attempt_index DESC,a.id DESC LIMIT 1)
    )
  WHEN disposition='skipped' AND disposition_reason IS NOT NULL THEN disposition_reason
  WHEN initial_eligibility='skipped' THEN initial_reason
  ELSE ?
END
WHERE request_id=?`, input.UnattemptedReason, requestID); err != nil {
		return RequestSettlementResult{}, err
	}
	result, err = tx.ExecContext(ctx, `UPDATE downstream_keys SET
reserved_nano_usd=reserved_nano_usd-?,used_nano_usd=used_nano_usd+?,updated_at=?
WHERE id=? AND reserved_nano_usd=? AND used_nano_usd=?`, request.ReservationNanoUSD, applied,
		input.FinishedAt, request.DownstreamKeyID, totalReserved, used)
	if err != nil {
		return RequestSettlementResult{}, err
	}
	changed, err = result.RowsAffected()
	if err != nil {
		return RequestSettlementResult{}, err
	}
	if changed != 1 {
		return RequestSettlementResult{}, ErrInvalidAccountingState
	}
	result, err = tx.ExecContext(ctx, `UPDATE downstream_key_hourly_usage SET
reserved_nano_usd=reserved_nano_usd-?,used_nano_usd=used_nano_usd+?,updated_at=?
WHERE downstream_key_id=? AND window_started_at=? AND reserved_nano_usd=? AND used_nano_usd=?`,
		request.ReservationNanoUSD, applied, input.FinishedAt, request.DownstreamKeyID, hourWindow,
		hourlyReserved, hourlyUsed)
	if err != nil {
		return RequestSettlementResult{}, err
	}
	changed, err = result.RowsAffected()
	if err != nil {
		return RequestSettlementResult{}, err
	}
	if changed != 1 {
		return RequestSettlementResult{}, ErrInvalidAccountingState
	}
	if request.ReservationNanoUSD > 0 || applied > 0 {
		if err := insertQuotaLedgerTx(ctx, tx, QuotaLedgerEntry{
			DownstreamKeyID: request.DownstreamKeyID, RequestID: requestID, EventType: "settle",
			ReservedDeltaNanoUSD: -request.ReservationNanoUSD, UsedDeltaNanoUSD: applied,
			PriceCatalogVersion: request.PriceCatalogVersion, PriceSKU: request.PriceSKU,
			CreatedAt: input.FinishedAt,
		}); err != nil {
			return RequestSettlementResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return RequestSettlementResult{}, err
	}
	return RequestSettlementResult{ChargedNanoUSD: applied, QuotaCapped: quotaCapped}, nil
}

// RecordRequestAttempt appends one completed upstream attempt. Duplicate
// replays are accepted only when every persisted field is identical.
func (s *Store) RecordRequestAttempt(ctx context.Context, input RequestAttemptWrite) error {
	input = normalizeRequestAttempt(input)
	if err := validateRequestAttempt(input); err != nil {
		return err
	}
	result, err := s.DB.ExecContext(ctx, `INSERT INTO request_attempts(
request_id,attempt_index,published_model_target_id,published_model_target_revision,
provider_model_target_id,provider_model_target_revision,
site_id,endpoint_id,credential_id,site_name_snapshot,endpoint_name_snapshot,credential_name_snapshot,
source_model,response_model,wire_protocol,api_surface,status,http_status,failure_kind,error_code,switch_reason,
first_token_ms,duration_ms,started_at,finished_at)
SELECT ?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?
WHERE EXISTS (SELECT 1 FROM request_logs WHERE id=? AND status='running')`, input.RequestID, input.AttemptIndex,
		input.PublishedModelTargetID, input.PublishedModelTargetRevision,
		input.ProviderModelTargetID, input.ProviderModelTargetRevision, input.SiteID,
		input.EndpointID, input.CredentialID, input.SiteName, input.EndpointName, input.CredentialName,
		input.SourceModel, nullableString(input.ResponseModel), input.WireProtocol, input.APISurface, input.Status, input.HTTPStatus,
		nullableString(input.FailureKind), nullableString(input.ErrorCode), nullableString(input.SwitchReason),
		input.FirstTokenMS, input.DurationMS, input.StartedAt, input.FinishedAt, input.RequestID)
	if err == nil {
		changed, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return rowsErr
		}
		if changed == 1 {
			return nil
		}
	} else if !isUniqueConstraintError(err) {
		return err
	}
	existing, loadErr := s.getRequestAttempt(ctx, input.RequestID, input.AttemptIndex)
	if loadErr == nil {
		if sameRequestAttempt(existing, input) {
			return nil
		}
		return ErrAttemptConflict
	}
	if !errors.Is(loadErr, sql.ErrNoRows) {
		return loadErr
	}
	var status string
	if loadErr := s.DB.QueryRowContext(ctx, `SELECT status FROM request_logs WHERE id=?`, input.RequestID).Scan(&status); loadErr != nil {
		return loadErr
	}
	return ErrRequestNotRunning
}

func (s *Store) MarkRequestRouteCandidateSkipped(ctx context.Context, requestID string, providerModelTargetID int64, reason string) error {
	requestID = strings.TrimSpace(requestID)
	reason = strings.ToLower(strings.TrimSpace(reason))
	if requestID == "" || providerModelTargetID <= 0 {
		return errors.New("request route candidate identity is invalid")
	}
	if err := validateAccountingCode(reason); err != nil || reason == "" {
		return errors.New("request route candidate skip reason is invalid")
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE request_route_candidates SET
disposition='skipped',disposition_reason=?
WHERE request_id=? AND provider_model_target_id=? AND disposition='pending'
  AND EXISTS (SELECT 1 FROM request_logs l WHERE l.id=request_id AND l.status='running')`,
		reason, requestID, providerModelTargetID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 1 {
		return nil
	}
	var disposition, existingReason string
	loadErr := s.DB.QueryRowContext(ctx, `SELECT disposition,COALESCE(disposition_reason,'')
FROM request_route_candidates WHERE request_id=? AND provider_model_target_id=?`, requestID, providerModelTargetID).
		Scan(&disposition, &existingReason)
	if loadErr != nil {
		return loadErr
	}
	if disposition == "skipped" && existingReason == reason {
		return nil
	}
	return ErrRequestNotRunning
}

func (s *Store) GetRequestLog(ctx context.Context, id string) (RequestLog, error) {
	return getRequestLog(ctx, s.DB, strings.TrimSpace(id))
}

func (s *Store) ListRequestAttempts(ctx context.Context, requestID string) ([]RequestAttempt, error) {
	rows, err := s.DB.QueryContext(ctx, requestAttemptSelect+` WHERE request_id=? ORDER BY attempt_index,id`, strings.TrimSpace(requestID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]RequestAttempt, 0)
	for rows.Next() {
		item, err := scanRequestAttempt(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListQuotaLedger(ctx context.Context, requestID string) ([]QuotaLedgerEntry, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,downstream_key_id,request_id,event_type,reserved_delta_nano_usd,
used_delta_nano_usd,price_catalog_version,price_sku,created_at
FROM quota_ledger WHERE request_id=? ORDER BY id`, strings.TrimSpace(requestID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]QuotaLedgerEntry, 0)
	for rows.Next() {
		var item QuotaLedgerEntry
		if err := rows.Scan(&item.ID, &item.DownstreamKeyID, &item.RequestID, &item.EventType,
			&item.ReservedDeltaNanoUSD, &item.UsedDeltaNanoUSD, &item.PriceCatalogVersion,
			&item.PriceSKU, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func insertQuotaLedgerTx(ctx context.Context, tx *sql.Tx, item QuotaLedgerEntry) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO quota_ledger(
downstream_key_id,request_id,event_type,reserved_delta_nano_usd,used_delta_nano_usd,
price_catalog_version,price_sku,created_at) VALUES (?,?,?,?,?,?,?,?)`, item.DownstreamKeyID,
		item.RequestID, item.EventType, item.ReservedDeltaNanoUSD, item.UsedDeltaNanoUSD,
		item.PriceCatalogVersion, item.PriceSKU, item.CreatedAt)
	return err
}

const requestLogColumns = `id,downstream_key_id,downstream_key_name_snapshot,published_model_id,
published_model_revision,effective_routing_profile_id,effective_routing_profile_name_snapshot,
source_routing_profile_id,source_routing_profile_name_snapshot,
route_revision,public_model,api_surface,reasoning_effort,thinking_budget_tokens,is_stream,
price_catalog_version,price_sku,billing_multiplier_bps,reservation_nano_usd,status,metering_status,metering_error_code,
final_attempt_index,http_status,first_token_ms,
duration_ms,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,cache_write_5m_tokens,cache_write_1h_tokens,
reasoning_tokens,official_cost_nano_usd,charged_nano_usd,quota_capped,error_code,started_at,finished_at
`

const requestLogSelect = `SELECT ` + requestLogColumns + ` FROM request_logs`

func getRequestLog(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (RequestLog, error) {
	return scanRequestLog(queryer.QueryRowContext(ctx, requestLogSelect+` WHERE id=?`, id))
}

func scanRequestLog(row scanner) (RequestLog, error) {
	var item RequestLog
	var reasoningEffort, meteringErrorCode, errorCode sql.NullString
	var thinkingBudget, finalAttempt, httpStatus, firstToken, duration, inputTokens, outputTokens sql.NullInt64
	var cacheRead, cacheWrite, cacheWrite5m, cacheWrite1h, reasoningTokens, finishedAt sql.NullInt64
	var stream, quotaCapped int
	err := row.Scan(&item.ID, &item.DownstreamKeyID, &item.DownstreamKeyName, &item.PublishedModelID,
		&item.PublishedModelRevision, &item.EffectiveRoutingProfileID, &item.EffectiveRoutingProfileName,
		&item.SourceRoutingProfileID, &item.SourceRoutingProfileName,
		&item.RouteRevision, &item.PublicModel, &item.APISurface, &reasoningEffort, &thinkingBudget,
		&stream, &item.PriceCatalogVersion, &item.PriceSKU, &item.BillingMultiplierBPS, &item.ReservationNanoUSD, &item.Status,
		&item.MeteringStatus, &meteringErrorCode, &finalAttempt, &httpStatus, &firstToken,
		&duration, &inputTokens, &outputTokens, &cacheRead,
		&cacheWrite, &cacheWrite5m, &cacheWrite1h, &reasoningTokens, &item.OfficialCostNanoUSD, &item.ChargedNanoUSD,
		&quotaCapped, &errorCode, &item.StartedAt, &finishedAt)
	if err != nil {
		return RequestLog{}, err
	}
	item.ReasoningEffort = reasoningEffort.String
	item.MeteringErrorCode = meteringErrorCode.String
	item.ErrorCode = errorCode.String
	item.Stream = stream == 1
	item.QuotaCapped = quotaCapped == 1
	item.ThinkingBudgetTokens = nullInt64Pointer(thinkingBudget)
	item.FinalAttemptIndex = nullIntPointer(finalAttempt)
	item.HTTPStatus = nullIntPointer(httpStatus)
	item.FirstTokenMS = nullInt64Pointer(firstToken)
	item.DurationMS = nullInt64Pointer(duration)
	item.InputTokens = nullInt64Pointer(inputTokens)
	item.OutputTokens = nullInt64Pointer(outputTokens)
	item.CacheReadTokens = nullInt64Pointer(cacheRead)
	item.CacheWriteTokens = nullInt64Pointer(cacheWrite)
	item.CacheWrite5MTokens = nullInt64Pointer(cacheWrite5m)
	item.CacheWrite1HTokens = nullInt64Pointer(cacheWrite1h)
	item.ReasoningTokens = nullInt64Pointer(reasoningTokens)
	item.FinishedAt = nullInt64Pointer(finishedAt)
	return item, nil
}

const requestAttemptSelect = `SELECT id,request_id,attempt_index,published_model_target_id,published_model_target_revision,
provider_model_target_id,
provider_model_target_revision,site_id,endpoint_id,credential_id,site_name_snapshot,endpoint_name_snapshot,
credential_name_snapshot,source_model,response_model,wire_protocol,api_surface,status,http_status,failure_kind,error_code,
switch_reason,first_token_ms,duration_ms,started_at,finished_at FROM request_attempts`

func (s *Store) getRequestAttempt(ctx context.Context, requestID string, attemptIndex int) (RequestAttempt, error) {
	return scanRequestAttempt(s.DB.QueryRowContext(ctx, requestAttemptSelect+` WHERE request_id=? AND attempt_index=?`, requestID, attemptIndex))
}

func scanRequestAttempt(row scanner) (RequestAttempt, error) {
	var item RequestAttempt
	var httpStatus, firstToken sql.NullInt64
	var responseModel, failureKind, errorCode, switchReason sql.NullString
	err := row.Scan(&item.ID, &item.RequestID, &item.AttemptIndex, &item.PublishedModelTargetID,
		&item.PublishedModelTargetRevision, &item.ProviderModelTargetID,
		&item.ProviderModelTargetRevision, &item.SiteID, &item.EndpointID,
		&item.CredentialID, &item.SiteName, &item.EndpointName, &item.CredentialName, &item.SourceModel, &responseModel,
		&item.WireProtocol, &item.APISurface, &item.Status, &httpStatus, &failureKind, &errorCode,
		&switchReason, &firstToken, &item.DurationMS, &item.StartedAt, &item.FinishedAt)
	if err != nil {
		return RequestAttempt{}, err
	}
	item.HTTPStatus = nullIntPointer(httpStatus)
	item.FirstTokenMS = nullInt64Pointer(firstToken)
	item.ResponseModel = responseModel.String
	item.FailureKind = failureKind.String
	item.ErrorCode = errorCode.String
	item.SwitchReason = switchReason.String
	return item, nil
}

func normalizeRequestStart(input RequestStart) RequestStart {
	input.ID = strings.TrimSpace(input.ID)
	input.EffectiveRoutingProfileName = strings.TrimSpace(input.EffectiveRoutingProfileName)
	input.SourceRoutingProfileName = strings.TrimSpace(input.SourceRoutingProfileName)
	input.PublicModel = strings.TrimSpace(input.PublicModel)
	input.APISurface = strings.ToLower(strings.TrimSpace(input.APISurface))
	input.ReasoningEffort = strings.ToLower(strings.TrimSpace(input.ReasoningEffort))
	input.PriceCatalogVersion = strings.TrimSpace(input.PriceCatalogVersion)
	input.PriceSKU = strings.TrimSpace(input.PriceSKU)
	for index := range input.RouteCandidates {
		candidate := &input.RouteCandidates[index]
		candidate.SiteName = strings.TrimSpace(candidate.SiteName)
		candidate.EndpointName = strings.TrimSpace(candidate.EndpointName)
		candidate.SourceModel = strings.TrimSpace(candidate.SourceModel)
		candidate.WireProtocol = strings.ToLower(strings.TrimSpace(candidate.WireProtocol))
		candidate.APISurface = strings.ToLower(strings.TrimSpace(candidate.APISurface))
		candidate.InitialEligibility = strings.ToLower(strings.TrimSpace(candidate.InitialEligibility))
		candidate.InitialReason = strings.ToLower(strings.TrimSpace(candidate.InitialReason))
		for credentialIndex := range candidate.Credentials {
			credential := &candidate.Credentials[credentialIndex]
			credential.Name = strings.TrimSpace(credential.Name)
			credential.RuntimeState = strings.ToLower(strings.TrimSpace(credential.RuntimeState))
		}
	}
	return input
}

func validateRequestStart(input RequestStart) error {
	if input.ID == "" || len(input.ID) > 128 || input.DownstreamKeyID <= 0 || input.PublishedModelID <= 0 ||
		input.PublishedModelRevision <= 0 || input.EffectiveRoutingProfileID <= 0 || input.SourceRoutingProfileID <= 0 {
		return errors.New("request identity and route snapshot are required")
	}
	if input.EffectiveRoutingProfileName == "" || input.SourceRoutingProfileName == "" ||
		input.PublicModel == "" || input.APISurface == "" || input.PriceCatalogVersion == "" || input.PriceSKU == "" {
		return errors.New("request model, surface, catalog version, and price SKU are required")
	}
	if len(input.PublicModel) > 256 || len(input.APISurface) > 128 || len(input.PriceCatalogVersion) > 256 || len(input.PriceSKU) > 256 {
		return errors.New("request snapshot field exceeds its maximum length")
	}
	if input.RouteRevision <= 0 || len(input.ReasoningEffort) > 64 || input.ReservationNanoUSD < 0 || input.StartedAt < 0 {
		return errors.New("request reasoning, reservation, or start time is invalid")
	}
	if err := validateNonNegativeInt64Pointer("thinking budget", input.ThinkingBudgetTokens); err != nil {
		return err
	}
	if len(input.RouteCandidates) == 0 {
		return errors.New("request route candidate snapshot is required")
	}
	positions := make(map[int]struct{}, len(input.RouteCandidates))
	targets := make(map[int64]struct{}, len(input.RouteCandidates))
	for _, candidate := range input.RouteCandidates {
		if candidate.Position < 0 || candidate.PublishedModelTargetID <= 0 || candidate.PublishedModelTargetRevision <= 0 ||
			candidate.ProviderModelTargetID <= 0 || candidate.ProviderModelTargetRevision <= 0 ||
			candidate.SiteID <= 0 || candidate.EndpointID <= 0 {
			return errors.New("request route candidate identity is invalid")
		}
		if _, exists := positions[candidate.Position]; exists {
			return errors.New("request route candidate positions must be unique")
		}
		positions[candidate.Position] = struct{}{}
		if _, exists := targets[candidate.ProviderModelTargetID]; exists {
			return errors.New("request route candidate targets must be unique")
		}
		targets[candidate.ProviderModelTargetID] = struct{}{}
		if candidate.SiteName == "" || candidate.EndpointName == "" || candidate.SourceModel == "" ||
			candidate.WireProtocol == "" || candidate.APISurface == "" {
			return errors.New("request route candidate snapshots are required")
		}
		if candidate.InitialEligibility != "eligible" && candidate.InitialEligibility != "skipped" {
			return errors.New("request route candidate eligibility is invalid")
		}
		if err := validateAccountingCode(candidate.InitialReason); err != nil || candidate.InitialReason == "" {
			return errors.New("request route candidate reason is invalid")
		}
		credentialIDs := make(map[int64]struct{}, len(candidate.Credentials))
		credentialPositions := make(map[int]struct{}, len(candidate.Credentials))
		for _, credential := range candidate.Credentials {
			if credential.ID <= 0 || credential.Position < 0 || credential.Name == "" || credential.RuntimeState == "" {
				return errors.New("request route credential snapshot is invalid")
			}
			if _, exists := credentialIDs[credential.ID]; exists {
				return errors.New("request route credential IDs must be unique per target")
			}
			credentialIDs[credential.ID] = struct{}{}
			if _, exists := credentialPositions[credential.Position]; exists {
				return errors.New("request route credential positions must be unique per target")
			}
			credentialPositions[credential.Position] = struct{}{}
			if credential.CoolingUntil != nil && *credential.CoolingUntil <= 0 {
				return errors.New("request route credential cooling time is invalid")
			}
		}
	}
	return nil
}

func normalizeRequestSettlement(input RequestSettlement) RequestSettlement {
	input.Status = strings.ToLower(strings.TrimSpace(input.Status))
	input.MeteringStatus = strings.ToLower(strings.TrimSpace(input.MeteringStatus))
	input.MeteringErrorCode = strings.ToLower(strings.TrimSpace(input.MeteringErrorCode))
	input.UnattemptedReason = strings.ToLower(strings.TrimSpace(input.UnattemptedReason))
	input.ErrorCode = strings.ToLower(strings.TrimSpace(input.ErrorCode))
	if input.MeteringStatus == "" {
		if input.Status == "success" {
			input.MeteringStatus = "metered"
		} else {
			input.MeteringStatus = "not_applicable"
		}
	}
	if input.UnattemptedReason == "" {
		input.UnattemptedReason = "request_" + input.Status
	}
	return input
}

func validateRequestSettlement(input RequestSettlement) error {
	switch input.Status {
	case "success", "failed", "cancelled":
	default:
		return errors.New("request settlement status is invalid")
	}
	switch input.MeteringStatus {
	case "pending", "metered", "unavailable", "not_applicable":
	default:
		return errors.New("request metering status is invalid")
	}
	if input.Status != "running" && input.MeteringStatus == "pending" {
		return errors.New("settled request cannot retain pending metering")
	}
	if input.FinalAttemptIndex != nil && *input.FinalAttemptIndex < 0 {
		return errors.New("final attempt index cannot be negative")
	}
	if input.HTTPStatus != nil && (*input.HTTPStatus < 100 || *input.HTTPStatus > 599) {
		return errors.New("HTTP status is invalid")
	}
	if input.DurationMS < 0 || input.OfficialCostNanoUSD < 0 || input.FinishedAt < 0 {
		return errors.New("request duration, cost, or finish time is invalid")
	}
	if input.FirstTokenMS != nil && (*input.FirstTokenMS < 0 || *input.FirstTokenMS > input.DurationMS) {
		return errors.New("first-token latency is invalid")
	}
	for name, value := range map[string]*int64{
		"input tokens": input.InputTokens, "output tokens": input.OutputTokens,
		"cache read tokens": input.CacheReadTokens, "cache write tokens": input.CacheWriteTokens,
		"cache write 5m tokens": input.CacheWrite5MTokens,
		"cache write 1h tokens": input.CacheWrite1HTokens, "reasoning tokens": input.ReasoningTokens,
	} {
		if err := validateNonNegativeInt64Pointer(name, value); err != nil {
			return err
		}
	}
	for _, code := range []string{input.MeteringErrorCode, input.UnattemptedReason, input.ErrorCode} {
		if err := validateAccountingCode(code); err != nil {
			return err
		}
	}
	return nil
}

func normalizeRequestAttempt(input RequestAttemptWrite) RequestAttemptWrite {
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.SiteName = strings.TrimSpace(input.SiteName)
	input.EndpointName = strings.TrimSpace(input.EndpointName)
	input.CredentialName = strings.TrimSpace(input.CredentialName)
	input.SourceModel = strings.TrimSpace(input.SourceModel)
	input.ResponseModel = strings.TrimSpace(input.ResponseModel)
	input.WireProtocol = strings.ToLower(strings.TrimSpace(input.WireProtocol))
	input.APISurface = strings.ToLower(strings.TrimSpace(input.APISurface))
	input.Status = strings.ToLower(strings.TrimSpace(input.Status))
	input.FailureKind = strings.ToLower(strings.TrimSpace(input.FailureKind))
	input.ErrorCode = strings.ToLower(strings.TrimSpace(input.ErrorCode))
	input.SwitchReason = strings.ToLower(strings.TrimSpace(input.SwitchReason))
	return input
}

func validateRequestAttempt(input RequestAttemptWrite) error {
	if input.RequestID == "" || input.AttemptIndex < 0 || input.PublishedModelTargetID <= 0 ||
		input.PublishedModelTargetRevision <= 0 || input.ProviderModelTargetID <= 0 ||
		input.ProviderModelTargetRevision <= 0 || input.SiteID <= 0 ||
		input.EndpointID <= 0 || input.CredentialID <= 0 {
		return errors.New("request attempt identity is invalid")
	}
	if input.SiteName == "" || input.EndpointName == "" || input.CredentialName == "" || input.SourceModel == "" ||
		input.WireProtocol == "" || input.APISurface == "" {
		return errors.New("request attempt snapshots are required")
	}
	switch input.Status {
	case "success", "failed", "cancelled":
	default:
		return errors.New("request attempt status is invalid")
	}
	if input.HTTPStatus != nil && (*input.HTTPStatus < 100 || *input.HTTPStatus > 599) {
		return errors.New("request attempt HTTP status is invalid")
	}
	if input.DurationMS < 0 || input.StartedAt < 0 || input.FinishedAt < input.StartedAt {
		return errors.New("request attempt timing is invalid")
	}
	if input.FirstTokenMS != nil && (*input.FirstTokenMS < 0 || *input.FirstTokenMS > input.DurationMS) {
		return errors.New("request attempt first-token latency is invalid")
	}
	for _, code := range []string{input.FailureKind, input.ErrorCode, input.SwitchReason} {
		if err := validateAccountingCode(code); err != nil {
			return err
		}
	}
	return nil
}

func sameRequestStart(item RequestLog, input RequestStart) bool {
	return item.ID == input.ID && item.DownstreamKeyID == input.DownstreamKeyID &&
		item.PublishedModelID == input.PublishedModelID && item.PublishedModelRevision == input.PublishedModelRevision &&
		item.EffectiveRoutingProfileID == input.EffectiveRoutingProfileID &&
		item.EffectiveRoutingProfileName == input.EffectiveRoutingProfileName &&
		item.SourceRoutingProfileID == input.SourceRoutingProfileID &&
		item.SourceRoutingProfileName == input.SourceRoutingProfileName &&
		item.RouteRevision == input.RouteRevision &&
		item.PublicModel == input.PublicModel && item.APISurface == input.APISurface &&
		item.ReasoningEffort == input.ReasoningEffort && equalInt64Pointer(item.ThinkingBudgetTokens, input.ThinkingBudgetTokens) &&
		item.Stream == input.Stream && item.PriceCatalogVersion == input.PriceCatalogVersion &&
		item.PriceSKU == input.PriceSKU && requestReservationMatches(item, input.ReservationNanoUSD) && item.StartedAt == input.StartedAt
}

func requestReservationMatches(item RequestLog, officialReservationNanoUSD int64) bool {
	scaled, err := scaleNanoUSD(officialReservationNanoUSD, item.BillingMultiplierBPS, true)
	return err == nil && item.ReservationNanoUSD == scaled
}

func sameRequestStartWithCandidates(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, item RequestLog, input RequestStart) (bool, error) {
	if !sameRequestStart(item, input) {
		return false, nil
	}
	stored, err := listRequestRouteCandidates(ctx, queryer, item.ID)
	if err != nil {
		return false, err
	}
	if len(stored) != len(input.RouteCandidates) {
		return false, nil
	}
	for index := range stored {
		if !sameRequestRouteCandidate(stored[index], input.RouteCandidates[index]) {
			return false, nil
		}
	}
	return true, nil
}

func sameRequestRouteCandidate(item RequestRouteCandidate, input RequestRouteCandidateWrite) bool {
	if item.Position != input.Position || item.PublishedModelTargetID != input.PublishedModelTargetID ||
		item.PublishedModelTargetRevision != input.PublishedModelTargetRevision ||
		item.ProviderModelTargetID != input.ProviderModelTargetID ||
		item.ProviderModelTargetRevision != input.ProviderModelTargetRevision || item.SiteID != input.SiteID ||
		item.SiteName != input.SiteName || item.EndpointID != input.EndpointID || item.EndpointName != input.EndpointName ||
		item.SourceModel != input.SourceModel || item.WireProtocol != input.WireProtocol || item.APISurface != input.APISurface ||
		item.InitialEligibility != input.InitialEligibility || item.InitialReason != input.InitialReason ||
		len(item.Credentials) != len(input.Credentials) {
		return false
	}
	for index := range item.Credentials {
		left, right := item.Credentials[index], input.Credentials[index]
		if left.ID != right.ID || left.Name != right.Name || left.Position != right.Position ||
			left.RuntimeState != right.RuntimeState || !equalInt64Pointer(left.CoolingUntil, right.CoolingUntil) {
			return false
		}
	}
	return true
}

func initialCandidateDisposition(candidate RequestRouteCandidateWrite) string {
	if candidate.InitialEligibility == "skipped" {
		return "skipped"
	}
	return "pending"
}

func initialCandidateDispositionReason(candidate RequestRouteCandidateWrite) any {
	if candidate.InitialEligibility == "skipped" {
		return candidate.InitialReason
	}
	return nil
}

func sameRequestSettlement(item RequestLog, input RequestSettlement) bool {
	if item.Status == "running" || item.Status != input.Status || item.MeteringStatus != input.MeteringStatus ||
		item.MeteringErrorCode != input.MeteringErrorCode || !equalIntPointer(item.FinalAttemptIndex, input.FinalAttemptIndex) ||
		!equalIntPointer(item.HTTPStatus, input.HTTPStatus) || !equalInt64Pointer(item.FirstTokenMS, input.FirstTokenMS) ||
		item.DurationMS == nil || *item.DurationMS != input.DurationMS || !equalInt64Pointer(item.InputTokens, input.InputTokens) ||
		!equalInt64Pointer(item.OutputTokens, input.OutputTokens) || !equalInt64Pointer(item.CacheReadTokens, input.CacheReadTokens) ||
		!equalInt64Pointer(item.CacheWriteTokens, input.CacheWriteTokens) ||
		!equalInt64Pointer(item.CacheWrite5MTokens, input.CacheWrite5MTokens) ||
		!equalInt64Pointer(item.CacheWrite1HTokens, input.CacheWrite1HTokens) ||
		!equalInt64Pointer(item.ReasoningTokens, input.ReasoningTokens) || item.OfficialCostNanoUSD != input.OfficialCostNanoUSD ||
		item.ErrorCode != input.ErrorCode || item.FinishedAt == nil || *item.FinishedAt != input.FinishedAt {
		return false
	}
	return true
}

func sameRequestAttempt(item RequestAttempt, input RequestAttemptWrite) bool {
	return item.RequestID == input.RequestID && item.AttemptIndex == input.AttemptIndex &&
		item.PublishedModelTargetID == input.PublishedModelTargetID &&
		item.PublishedModelTargetRevision == input.PublishedModelTargetRevision &&
		item.ProviderModelTargetID == input.ProviderModelTargetID &&
		item.ProviderModelTargetRevision == input.ProviderModelTargetRevision && item.SiteID == input.SiteID &&
		item.EndpointID == input.EndpointID && item.CredentialID == input.CredentialID && item.SiteName == input.SiteName &&
		item.EndpointName == input.EndpointName && item.CredentialName == input.CredentialName &&
		item.SourceModel == input.SourceModel && item.ResponseModel == input.ResponseModel &&
		item.WireProtocol == input.WireProtocol && item.APISurface == input.APISurface &&
		item.Status == input.Status && equalIntPointer(item.HTTPStatus, input.HTTPStatus) && item.FailureKind == input.FailureKind &&
		item.ErrorCode == input.ErrorCode && item.SwitchReason == input.SwitchReason &&
		equalInt64Pointer(item.FirstTokenMS, input.FirstTokenMS) && item.DurationMS == input.DurationMS &&
		item.StartedAt == input.StartedAt && item.FinishedAt == input.FinishedAt
}

func validateAccountingCode(value string) error {
	if value == "" {
		return nil
	}
	if !accountingCodePattern.MatchString(value) {
		return errors.New("accounting status code is invalid")
	}
	return nil
}

func validateNonNegativeInt64Pointer(name string, value *int64) error {
	if value != nil && *value < 0 {
		return fmt.Errorf("%s cannot be negative", name)
	}
	return nil
}

func nullInt64Pointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	copy := value.Int64
	return &copy
}

func nullIntPointer(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	copy := int(value.Int64)
	return &copy
}

func equalInt64Pointer(left, right *int64) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func equalIntPointer(left, right *int) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func isUniqueConstraintError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint") || strings.Contains(message, "constraint failed: unique")
}
