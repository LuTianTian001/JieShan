package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/LuTianTian001/JieShan/internal/redact"
)

var (
	ErrQuotaExceeded          = errors.New("downstream key quota exceeded")
	ErrRequestAlreadyFinished = errors.New("request is already finished or missing")
	ErrInvalidQuotaState      = errors.New("invalid downstream quota state")
)

type RequestStart struct {
	ID                     string
	RoutingGeneration      string
	Surface                string
	DownstreamKeyID        int64
	RouteID                int64
	RouteRevision          int64
	PublishedModelID       int64
	PublishedModelRevision int64
	RoutingProfileID       int64
	RoutingProfileName     string
	RequestedModel         string
	ReasoningEffort        string
	ThinkingBudget         *int64
	Stream                 bool
	StartedAt              int64
}

type RequestFinish struct {
	ActualModel        string
	Status             string
	HTTPStatus         int
	FirstTokenMS       *int64
	DurationMS         int64
	InputTokens        *int64
	CacheReadTokens    *int64
	CacheWriteTokens   *int64
	CacheWrite1HTokens *int64
	OutputTokens       *int64
	ReasoningTokens    *int64
	CostMicroUSD       int64
	PriceSnapshotJSON  string
	ErrorMessage       string
	FinishedAt         int64
}

func (s *Store) StartRequest(ctx context.Context, input RequestStart) error {
	return s.StartRequestWithReservation(ctx, input, 0, false)
}

// StartRequestWithReservation creates the request log and reserves quota in one
// transaction. A finite key is admitted only when the full amount is available
// at the instant the write lock is held.
func (s *Store) StartRequestWithReservation(ctx context.Context, input RequestStart, reservedMicroUSD int64, finiteQuota bool) error {
	if reservedMicroUSD < 0 || (finiteQuota && reservedMicroUSD == 0) {
		return fmt.Errorf("%w: invalid reservation amount", ErrInvalidQuotaState)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if finiteQuota {
		result, err := tx.ExecContext(ctx, `UPDATE downstream_keys
SET reserved_micro_usd=reserved_micro_usd+?,last_used_at=?,updated_at=?
WHERE id=? AND enabled=1 AND quota_micro_usd IS NOT NULL
  AND quota_micro_usd>=0 AND used_micro_usd>=0 AND reserved_micro_usd>=0
  AND used_micro_usd<=quota_micro_usd
  AND reserved_micro_usd<=quota_micro_usd-used_micro_usd
  AND ?<=quota_micro_usd-used_micro_usd-reserved_micro_usd
  AND (expires_at IS NULL OR expires_at>?)`,
			reservedMicroUSD, input.StartedAt, input.StartedAt, input.DownstreamKeyID, reservedMicroUSD, input.StartedAt)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return ErrQuotaExceeded
		}
	}
	if err := insertRequestStart(ctx, tx, input); err != nil {
		return err
	}
	if finiteQuota {
		if err := insertQuotaLedger(ctx, tx, input.DownstreamKeyID, input.ID, "reserve", reservedMicroUSD, input.StartedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) FinishRequest(ctx context.Context, id string, input RequestFinish) error {
	result, err := s.DB.ExecContext(ctx, requestFinishSQL+` WHERE id=? AND status='running'`, requestFinishArgs(input, id)...)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrRequestAlreadyFinished
	}
	return nil
}

// FinishRequestAndSettle atomically finalizes the request, removes its complete
// reservation, charges actual usage, and writes the quota ledger.
func (s *Store) FinishRequestAndSettle(ctx context.Context, id string, keyID, reservedMicroUSD, chargedMicroUSD int64, input RequestFinish) error {
	if keyID <= 0 || reservedMicroUSD < 0 || chargedMicroUSD < 0 {
		return fmt.Errorf("%w: negative or missing settlement value", ErrInvalidQuotaState)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var quota sql.NullInt64
	var usedMicroUSD, totalReservedMicroUSD int64
	if err := tx.QueryRowContext(ctx, `SELECT quota_micro_usd,used_micro_usd,reserved_micro_usd
FROM downstream_keys WHERE id=?`, keyID).Scan(&quota, &usedMicroUSD, &totalReservedMicroUSD); err != nil {
		return err
	}
	if usedMicroUSD < 0 || totalReservedMicroUSD < reservedMicroUSD {
		return ErrInvalidQuotaState
	}
	appliedMicroUSD := chargedMicroUSD
	if quota.Valid {
		otherReservations := totalReservedMicroUSD - reservedMicroUSD
		available := quota.Int64 - usedMicroUSD - otherReservations
		if quota.Int64 < 0 || available < 0 {
			return ErrInvalidQuotaState
		}
		if appliedMicroUSD > available {
			appliedMicroUSD = available
			input.ErrorMessage = appendAccountingMessage(input.ErrorMessage, "official cost exceeded the remaining downstream quota; the key was exhausted")
		}
	}

	result, err := tx.ExecContext(ctx, requestFinishSQL+` WHERE id=? AND downstream_key_id=? AND status='running'`, requestFinishArgs(input, id, keyID)...)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrRequestAlreadyFinished
	}

	result, err = tx.ExecContext(ctx, `UPDATE downstream_keys
SET reserved_micro_usd=reserved_micro_usd-?,used_micro_usd=used_micro_usd+?,updated_at=?
WHERE id=? AND reserved_micro_usd=? AND used_micro_usd=? AND used_micro_usd<=?`,
		reservedMicroUSD, appliedMicroUSD, input.FinishedAt, keyID, totalReservedMicroUSD, usedMicroUSD, int64(math.MaxInt64)-appliedMicroUSD)
	if err != nil {
		return err
	}
	changed, err = result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrInvalidQuotaState
	}

	if reservedMicroUSD == 0 {
		if appliedMicroUSD > 0 {
			if err := insertQuotaLedger(ctx, tx, keyID, id, "settle", appliedMicroUSD, input.FinishedAt); err != nil {
				return err
			}
		}
	} else {
		settled := appliedMicroUSD
		if settled > reservedMicroUSD {
			settled = reservedMicroUSD
		}
		if settled > 0 {
			if err := insertQuotaLedger(ctx, tx, keyID, id, "settle", settled, input.FinishedAt); err != nil {
				return err
			}
		}
		if appliedMicroUSD < reservedMicroUSD {
			if err := insertQuotaLedger(ctx, tx, keyID, id, "release", reservedMicroUSD-appliedMicroUSD, input.FinishedAt); err != nil {
				return err
			}
		}
		if appliedMicroUSD > reservedMicroUSD {
			if err := insertQuotaLedger(ctx, tx, keyID, id, "additional", appliedMicroUSD-reservedMicroUSD, input.FinishedAt); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func appendAccountingMessage(existing, addition string) string {
	if existing == "" {
		return addition
	}
	return existing + "; " + addition
}

const requestFinishSQL = `UPDATE request_logs SET actual_model=?,status=?,http_status=?,first_token_ms=?,duration_ms=?,
input_tokens=?,cache_read_tokens=?,cache_write_tokens=?,cache_write_1h_tokens=?,output_tokens=?,reasoning_tokens=?,cost_micro_usd=?,price_snapshot_json=?,error_message=?,finished_at=?`

func requestFinishArgs(input RequestFinish, tail ...any) []any {
	values := []any{
		nullableString(input.ActualModel), input.Status, nullableStatus(input.HTTPStatus), input.FirstTokenMS, input.DurationMS,
		input.InputTokens, input.CacheReadTokens, input.CacheWriteTokens, input.CacheWrite1HTokens,
		input.OutputTokens, input.ReasoningTokens, input.CostMicroUSD,
		nullableString(input.PriceSnapshotJSON), nullableString(redact.String(input.ErrorMessage)), input.FinishedAt,
	}
	return append(values, tail...)
}

func insertRequestStart(ctx context.Context, tx *sql.Tx, input RequestStart) error {
	generation := strings.ToLower(strings.TrimSpace(input.RoutingGeneration))
	if generation == "" {
		generation = "legacy"
	}
	if generation != "legacy" && generation != "v3" {
		return errors.New("request routing generation must be legacy or v3")
	}
	surface := strings.ToLower(strings.TrimSpace(input.Surface))
	if surface == "" {
		surface = "chat_completions"
	}
	if surface != "chat_completions" && surface != "responses" {
		return errors.New("request API surface must be chat_completions or responses")
	}
	profileName := strings.TrimSpace(input.RoutingProfileName)
	if profileName == "" {
		profileName = DefaultRoutingProfileName
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO request_logs(
id,routing_generation,api_surface,downstream_key_id,route_id,route_revision,published_model_id,published_model_revision,routing_profile_id,routing_profile_name,
requested_model,reasoning_effort,thinking_budget,status,is_stream,started_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,'running',?,?)`, input.ID, generation, surface, input.DownstreamKeyID,
		nullableInt64(input.RouteID), nullableInt64(input.RouteRevision),
		nullableInt64(input.PublishedModelID), nullableInt64(input.PublishedModelRevision),
		nullableInt64(input.RoutingProfileID), profileName,
		input.RequestedModel, nullableString(input.ReasoningEffort), input.ThinkingBudget, boolInt(input.Stream), input.StartedAt)
	return err
}

func insertQuotaLedger(ctx context.Context, tx *sql.Tx, keyID int64, requestID, entryType string, amount, createdAt int64) error {
	if amount <= 0 {
		return fmt.Errorf("%w: ledger amount must be positive", ErrInvalidQuotaState)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO quota_ledger(downstream_key_id,request_id,entry_type,amount_micro_usd,created_at)
VALUES (?,?,?,?,?)`, keyID, requestID, entryType, amount, createdAt)
	return err
}

func (s *Store) AddRequestAttempt(ctx context.Context, item RequestAttempt) error {
	generation := strings.ToLower(strings.TrimSpace(item.RoutingGeneration))
	if generation == "" {
		generation = "legacy"
	}
	if generation != "legacy" && generation != "v3" {
		return errors.New("request attempt routing generation must be legacy or v3")
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO request_attempts(
request_id,attempt_index,routing_generation,target_id,upstream_id,route_site_target_id,site_id,endpoint_id,
inference_credential_id,site_model_id,upstream_name,site_name,endpoint_name,credential_name,upstream_model,status,http_status,switch_reason,error_class,
error_message,latency_ms,first_token_ms,created_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, item.RequestID, item.AttemptIndex, generation, item.TargetID,
		item.UpstreamID, item.RouteSiteTargetID, item.SiteID, item.EndpointID, item.InferenceCredentialID,
		item.SiteModelID, nullableString(item.UpstreamName), nullableString(item.SiteName), nullableString(item.EndpointName), nullableString(item.CredentialName),
		nullableString(item.UpstreamModel), item.Status, item.HTTPStatus,
		nullableString(item.SwitchReason), nullableString(item.ErrorClass), nullableString(redact.String(item.ErrorMessage)),
		item.LatencyMS, item.FirstTokenMS, item.CreatedAt)
	return err
}

func (s *Store) ListRequestLogs(ctx context.Context, limit, offset int) ([]RequestLog, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.DB.QueryContext(ctx, requestSelect+` ORDER BY l.started_at DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]RequestLog, 0)
	for rows.Next() {
		item, err := scanRequestLog(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListRequestLogsPage(ctx context.Context, filter RequestLogFilter, limit int) (RequestLogPage, error) {
	page := RequestLogPage{Items: make([]RequestLog, 0)}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return page, errors.New("request log limit must be between 1 and 200")
	}
	where, args, err := requestLogWhere(filter)
	if err != nil {
		return page, err
	}
	args = append(args, limit+1)
	rows, err := s.DB.QueryContext(ctx, requestSelect+where+` ORDER BY l.started_at DESC,l.id DESC LIMIT ?`, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		item, scanErr := scanRequestLog(rows)
		if scanErr != nil {
			return page, scanErr
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Items) <= limit {
		return page, nil
	}
	page.Items = page.Items[:limit]
	last := page.Items[len(page.Items)-1]
	page.HasMore = true
	page.NextCursor = &RequestLogCursor{BeforeTime: last.StartedAt, BeforeID: last.ID}
	return page, nil
}

func (s *Store) SummarizeRequestLogs(ctx context.Context, filter RequestLogFilter) (RequestLogSummary, error) {
	where, args, err := requestLogWhere(filter)
	if err != nil {
		return RequestLogSummary{}, err
	}
	query := `WITH filtered AS (
SELECT l.status,l.cost_micro_usd,l.first_token_ms,
CASE WHEN l.routing_generation='v3' THEN
  CASE WHEN (SELECT COUNT(DISTINCT a.site_id) FROM request_attempts a WHERE a.request_id=l.id AND a.site_id IS NOT NULL)>1 THEN 1 ELSE 0 END
ELSE CASE WHEN (SELECT COUNT(*) FROM request_attempts a WHERE a.request_id=l.id)>1 THEN 1 ELSE 0 END END AS switched
FROM request_logs l LEFT JOIN downstream_keys k ON k.id=l.downstream_key_id` + where + `
), metrics AS (
SELECT COUNT(*) AS total_count,
COALESCE(SUM(CASE WHEN status='success' THEN 1 ELSE 0 END),0) AS success_count,
COALESCE(SUM(cost_micro_usd),0) AS total_cost,
COALESCE(SUM(switched),0) AS switched_count
FROM filtered
), ranked AS (
SELECT first_token_ms,
ROW_NUMBER() OVER (ORDER BY first_token_ms) AS row_number,
COUNT(*) OVER () AS total_ttft
FROM filtered WHERE first_token_ms IS NOT NULL
), percentiles AS (
SELECT
MAX(CASE WHEN row_number=((total_ttft*50+99)/100) THEN first_token_ms END) AS p50_ttft,
MAX(CASE WHEN row_number=((total_ttft*95+99)/100) THEN first_token_ms END) AS p95_ttft
FROM ranked
)
SELECT metrics.total_count,metrics.success_count,metrics.total_cost,metrics.switched_count,
percentiles.p50_ttft,percentiles.p95_ttft
FROM metrics CROSS JOIN percentiles`
	var summary RequestLogSummary
	var successCount, switchedCount int64
	var p50, p95 sql.NullInt64
	if err := s.DB.QueryRowContext(ctx, query, args...).Scan(
		&summary.Count, &successCount, &summary.CostMicroUSD, &switchedCount, &p50, &p95,
	); err != nil {
		return RequestLogSummary{}, err
	}
	if summary.Count > 0 {
		summary.SuccessRate = float64(successCount) / float64(summary.Count) * 100
		summary.SwitchRate = float64(switchedCount) / float64(summary.Count) * 100
	}
	summary.P50TTFTMS = int64Ptr(p50)
	summary.P95TTFTMS = int64Ptr(p95)
	return summary, nil
}

func requestLogWhere(filter RequestLogFilter) (string, []any, error) {
	beforeID := strings.TrimSpace(filter.BeforeID)
	if (filter.BeforeTime == nil) != (beforeID == "") {
		return "", nil, errors.New("request log cursor requires both beforeTime and beforeId")
	}
	if filter.BeforeTime != nil && *filter.BeforeTime < 0 {
		return "", nil, errors.New("request log beforeTime cannot be negative")
	}
	if filter.UpstreamID != nil && *filter.UpstreamID <= 0 {
		return "", nil, errors.New("request log upstream ID must be positive")
	}
	if filter.SiteID != nil && *filter.SiteID <= 0 {
		return "", nil, errors.New("request log site ID must be positive")
	}
	if filter.DownstreamKeyID != nil && *filter.DownstreamKeyID <= 0 {
		return "", nil, errors.New("request log downstream key ID must be positive")
	}

	clauses := make([]string, 0, 7)
	args := make([]any, 0, 10)
	if status := strings.TrimSpace(filter.Status); status != "" {
		clauses = append(clauses, "l.status=?")
		args = append(args, strings.ToLower(status))
	}
	if model := strings.TrimSpace(filter.Model); model != "" {
		clauses = append(clauses, "(l.requested_model=? COLLATE NOCASE OR l.actual_model=? COLLATE NOCASE)")
		args = append(args, model, model)
	}
	if filter.SiteID != nil {
		clauses = append(clauses, "EXISTS (SELECT 1 FROM request_attempts af WHERE af.request_id=l.id AND af.site_id=?)")
		args = append(args, *filter.SiteID)
	}
	if filter.UpstreamID != nil {
		clauses = append(clauses, "EXISTS (SELECT 1 FROM request_attempts af WHERE af.request_id=l.id AND af.upstream_id=?)")
		args = append(args, *filter.UpstreamID)
	}
	if filter.DownstreamKeyID != nil {
		clauses = append(clauses, "l.downstream_key_id=?")
		args = append(args, *filter.DownstreamKeyID)
	}
	if filter.Stream != nil {
		clauses = append(clauses, "l.is_stream=?")
		args = append(args, boolInt(*filter.Stream))
	}
	if filter.Switched != nil {
		operator := "=0"
		if *filter.Switched {
			operator = ">0"
		}
		clauses = append(clauses, `(CASE WHEN l.routing_generation='v3' THEN
MAX((SELECT COUNT(DISTINCT asw.site_id) FROM request_attempts asw WHERE asw.request_id=l.id AND asw.site_id IS NOT NULL)-1,0)
ELSE MAX((SELECT COUNT(*) FROM request_attempts asw WHERE asw.request_id=l.id)-1,0) END)`+operator)
	}
	if filter.BeforeTime != nil {
		clauses = append(clauses, "(l.started_at<? OR (l.started_at=? AND l.id<?))")
		args = append(args, *filter.BeforeTime, *filter.BeforeTime, beforeID)
	}
	if len(clauses) == 0 {
		return "", args, nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args, nil
}

func (s *Store) GetRequestLog(ctx context.Context, id string) (RequestLog, []RequestAttempt, error) {
	item, err := scanRequestLog(s.DB.QueryRowContext(ctx, requestSelect+` WHERE l.id=?`, id))
	if err != nil {
		return RequestLog{}, nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT a.id,a.request_id,a.attempt_index,a.routing_generation,a.target_id,a.upstream_id,COALESCE(NULLIF(a.upstream_name,''),u.name,''),
	a.route_site_target_id,a.site_id,COALESCE(NULLIF(a.site_name,''),s.name,''),a.endpoint_id,COALESCE(NULLIF(a.endpoint_name,''),e.name,''),
	a.inference_credential_id,COALESCE(NULLIF(a.credential_name,''),c.name,''),a.site_model_id,a.upstream_model,a.status,a.http_status,
a.switch_reason,a.error_class,a.error_message,a.latency_ms,a.first_token_ms,a.created_at
FROM request_attempts a
LEFT JOIN upstreams u ON u.id=a.upstream_id
LEFT JOIN sites s ON s.id=a.site_id
LEFT JOIN inference_endpoints e ON e.id=a.endpoint_id
LEFT JOIN inference_credentials c ON c.id=a.inference_credential_id
WHERE a.request_id=? ORDER BY a.attempt_index,a.id`, id)
	if err != nil {
		return RequestLog{}, nil, err
	}
	defer rows.Close()
	attempts := make([]RequestAttempt, 0)
	for rows.Next() {
		var attempt RequestAttempt
		var targetID, upstreamID, routeSiteTargetID, siteID, endpointID, credentialID, siteModelID sql.NullInt64
		var httpStatus, latency, firstToken sql.NullInt64
		var model, switchReason, errorClass, errorMessage sql.NullString
		if err := rows.Scan(&attempt.ID, &attempt.RequestID, &attempt.AttemptIndex, &attempt.RoutingGeneration,
			&targetID, &upstreamID, &attempt.UpstreamName, &routeSiteTargetID, &siteID, &attempt.SiteName,
			&endpointID, &attempt.EndpointName, &credentialID, &attempt.CredentialName, &siteModelID,
			&model, &attempt.Status, &httpStatus, &switchReason, &errorClass, &errorMessage,
			&latency, &firstToken, &attempt.CreatedAt); err != nil {
			return RequestLog{}, nil, err
		}
		attempt.TargetID = int64Ptr(targetID)
		attempt.UpstreamID = int64Ptr(upstreamID)
		attempt.RouteSiteTargetID = int64Ptr(routeSiteTargetID)
		attempt.SiteID = int64Ptr(siteID)
		attempt.EndpointID = int64Ptr(endpointID)
		attempt.InferenceCredentialID = int64Ptr(credentialID)
		attempt.SiteModelID = int64Ptr(siteModelID)
		attempt.UpstreamModel = model.String
		attempt.HTTPStatus = intPtr(httpStatus)
		attempt.SwitchReason = switchReason.String
		attempt.ErrorClass = errorClass.String
		attempt.ErrorMessage = errorMessage.String
		attempt.LatencyMS = int64Ptr(latency)
		attempt.FirstTokenMS = int64Ptr(firstToken)
		attempts = append(attempts, attempt)
	}
	return item, attempts, rows.Err()
}

func (s *Store) DeleteOldLogs(ctx context.Context, cutoffMS int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM request_logs WHERE started_at<? AND status<>'running'", cutoffMS); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM quota_ledger
WHERE request_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM request_logs WHERE request_logs.id=quota_ledger.request_id)`); err != nil {
		return err
	}
	return tx.Commit()
}

const requestSelect = `SELECT l.id,l.routing_generation,l.api_surface,l.downstream_key_id,COALESCE(k.name,'Deleted key'),l.route_id,l.route_revision,l.published_model_id,l.published_model_revision,l.routing_profile_id,l.routing_profile_name,
selected.upstream_id,COALESCE(NULLIF(selected.upstream_name,''),selected_upstream.name,''),selected.site_id,COALESCE(NULLIF(selected.site_name,''),selected_site.name,''),
selected.endpoint_id,COALESCE(NULLIF(selected.endpoint_name,''),selected_endpoint.name,''),selected.inference_credential_id,COALESCE(NULLIF(selected.credential_name,''),selected_credential.name,''),
l.requested_model,l.actual_model,l.reasoning_effort,l.thinking_budget,
l.status,l.http_status,l.is_stream,l.first_token_ms,l.duration_ms,l.input_tokens,l.cache_read_tokens,l.cache_write_tokens,l.cache_write_1h_tokens,l.output_tokens,l.reasoning_tokens,
l.cost_micro_usd,l.price_snapshot_json,
CASE WHEN l.routing_generation='v3' THEN MAX((SELECT COUNT(DISTINCT a.site_id) FROM request_attempts a WHERE a.request_id=l.id AND a.site_id IS NOT NULL)-1,0)
ELSE MAX((SELECT COUNT(*) FROM request_attempts a WHERE a.request_id=l.id)-1,0) END,
l.error_message,l.started_at,l.finished_at
FROM request_logs l
LEFT JOIN downstream_keys k ON k.id=l.downstream_key_id
LEFT JOIN request_attempts selected ON selected.id=(
  SELECT candidate.id FROM request_attempts candidate WHERE candidate.request_id=l.id
  ORDER BY CASE WHEN candidate.status='success' THEN 0 ELSE 1 END,candidate.attempt_index DESC,candidate.id DESC LIMIT 1
)
LEFT JOIN upstreams selected_upstream ON selected_upstream.id=selected.upstream_id
LEFT JOIN sites selected_site ON selected_site.id=selected.site_id
LEFT JOIN inference_endpoints selected_endpoint ON selected_endpoint.id=selected.endpoint_id
LEFT JOIN inference_credentials selected_credential ON selected_credential.id=selected.inference_credential_id`

func scanRequestLog(row scanner) (RequestLog, error) {
	var item RequestLog
	var keyID, routeID, revision, publishedModelID, publishedRevision, routingProfileID sql.NullInt64
	var actualUpstreamID, actualSiteID, actualEndpointID, actualCredentialID sql.NullInt64
	var thinkingBudget, httpStatus, firstToken, duration, input, cacheRead, cacheWrite, cacheWrite1H, output, reasoning, finished sql.NullInt64
	var actualModel, effort, priceSnapshot, errorMessage sql.NullString
	var stream int
	err := row.Scan(&item.ID, &item.RoutingGeneration, &item.Surface, &keyID, &item.KeyName, &routeID, &revision, &publishedModelID, &publishedRevision, &routingProfileID, &item.RoutingProfileName,
		&actualUpstreamID, &item.ActualUpstreamName, &actualSiteID, &item.ActualSiteName,
		&actualEndpointID, &item.ActualEndpointName, &actualCredentialID, &item.ActualCredentialName,
		&item.RequestedModel, &actualModel, &effort, &thinkingBudget,
		&item.Status, &httpStatus, &stream, &firstToken, &duration, &input, &cacheRead, &cacheWrite, &cacheWrite1H, &output, &reasoning,
		&item.CostMicroUSD, &priceSnapshot, &item.SwitchCount, &errorMessage, &item.StartedAt, &finished)
	if err != nil {
		return RequestLog{}, err
	}
	item.DownstreamKeyID = int64Ptr(keyID)
	item.RouteID = int64Ptr(routeID)
	item.RouteRevision = int64Ptr(revision)
	item.PublishedModelID = int64Ptr(publishedModelID)
	item.PublishedModelRevision = int64Ptr(publishedRevision)
	item.RoutingProfileID = int64Ptr(routingProfileID)
	item.ActualUpstreamID = int64Ptr(actualUpstreamID)
	item.ActualSiteID = int64Ptr(actualSiteID)
	item.ActualEndpointID = int64Ptr(actualEndpointID)
	item.ActualCredentialID = int64Ptr(actualCredentialID)
	item.ActualModel = actualModel.String
	item.ReasoningEffort = effort.String
	item.ThinkingBudget = int64Ptr(thinkingBudget)
	item.HTTPStatus = intPtr(httpStatus)
	item.Stream = stream == 1
	item.FirstTokenMS = int64Ptr(firstToken)
	item.DurationMS = int64Ptr(duration)
	item.InputTokens = int64Ptr(input)
	item.CacheReadTokens = int64Ptr(cacheRead)
	item.CacheWriteTokens = int64Ptr(cacheWrite)
	item.CacheWrite1HTokens = int64Ptr(cacheWrite1H)
	item.OutputTokens = int64Ptr(output)
	item.ReasoningTokens = int64Ptr(reasoning)
	item.PriceSnapshot = priceSnapshot.String
	item.ErrorMessage = errorMessage.String
	item.FinishedAt = int64Ptr(finished)
	return item, nil
}

func nullableStatus(value int) any {
	if value <= 0 {
		return nil
	}
	return value
}

func int64Ptr(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	copy := value.Int64
	return &copy
}

func intPtr(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	copy := int(value.Int64)
	return &copy
}
