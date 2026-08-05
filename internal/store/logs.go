package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

	"github.com/LuTianTian001/JieShan/internal/redact"
)

var (
	ErrQuotaExceeded          = errors.New("downstream key quota exceeded")
	ErrRequestAlreadyFinished = errors.New("request is already finished or missing")
	ErrInvalidQuotaState      = errors.New("invalid downstream quota state")
)

type RequestStart struct {
	ID              string
	DownstreamKeyID int64
	RouteID         int64
	RouteRevision   int64
	RequestedModel  string
	ReasoningEffort string
	ThinkingBudget  *int64
	Stream          bool
	StartedAt       int64
}

type RequestFinish struct {
	ActualModel       string
	Status            string
	HTTPStatus        int
	FirstTokenMS      *int64
	DurationMS        int64
	InputTokens       *int64
	CacheReadTokens   *int64
	OutputTokens      *int64
	ReasoningTokens   *int64
	CostMicroUSD      int64
	PriceSnapshotJSON string
	ErrorMessage      string
	FinishedAt        int64
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
input_tokens=?,cache_read_tokens=?,output_tokens=?,reasoning_tokens=?,cost_micro_usd=?,price_snapshot_json=?,error_message=?,finished_at=?`

func requestFinishArgs(input RequestFinish, tail ...any) []any {
	values := []any{
		nullableString(input.ActualModel), input.Status, nullableStatus(input.HTTPStatus), input.FirstTokenMS, input.DurationMS,
		input.InputTokens, input.CacheReadTokens, input.OutputTokens, input.ReasoningTokens, input.CostMicroUSD,
		nullableString(input.PriceSnapshotJSON), nullableString(redact.String(input.ErrorMessage)), input.FinishedAt,
	}
	return append(values, tail...)
}

func insertRequestStart(ctx context.Context, tx *sql.Tx, input RequestStart) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO request_logs(id,downstream_key_id,route_id,route_revision,requested_model,reasoning_effort,thinking_budget,status,is_stream,started_at)
VALUES (?,?,?,?,?,?,?,'running',?,?)`, input.ID, input.DownstreamKeyID, input.RouteID, input.RouteRevision,
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
	_, err := s.DB.ExecContext(ctx, `INSERT INTO request_attempts(request_id,attempt_index,target_id,upstream_id,upstream_model,status,http_status,switch_reason,error_class,error_message,latency_ms,first_token_ms,created_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, item.RequestID, item.AttemptIndex, item.TargetID, item.UpstreamID,
		nullableString(item.UpstreamModel), item.Status, item.HTTPStatus, nullableString(item.SwitchReason),
		nullableString(item.ErrorClass), nullableString(redact.String(item.ErrorMessage)), item.LatencyMS, item.FirstTokenMS, item.CreatedAt)
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

func (s *Store) GetRequestLog(ctx context.Context, id string) (RequestLog, []RequestAttempt, error) {
	item, err := scanRequestLog(s.DB.QueryRowContext(ctx, requestSelect+` WHERE l.id=?`, id))
	if err != nil {
		return RequestLog{}, nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT a.id,a.request_id,a.attempt_index,a.target_id,a.upstream_id,COALESCE(u.name,''),a.upstream_model,a.status,a.http_status,
a.switch_reason,a.error_class,a.error_message,a.latency_ms,a.first_token_ms,a.created_at FROM request_attempts a LEFT JOIN upstreams u ON u.id=a.upstream_id WHERE a.request_id=? ORDER BY a.attempt_index`, id)
	if err != nil {
		return RequestLog{}, nil, err
	}
	defer rows.Close()
	attempts := make([]RequestAttempt, 0)
	for rows.Next() {
		var attempt RequestAttempt
		var targetID, upstreamID, httpStatus, latency, firstToken sql.NullInt64
		var model, switchReason, errorClass, errorMessage sql.NullString
		if err := rows.Scan(&attempt.ID, &attempt.RequestID, &attempt.AttemptIndex, &targetID, &upstreamID, &attempt.UpstreamName,
			&model, &attempt.Status, &httpStatus, &switchReason, &errorClass, &errorMessage,
			&latency, &firstToken, &attempt.CreatedAt); err != nil {
			return RequestLog{}, nil, err
		}
		attempt.TargetID = int64Ptr(targetID)
		attempt.UpstreamID = int64Ptr(upstreamID)
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

const requestSelect = `SELECT l.id,l.downstream_key_id,COALESCE(k.name,'Deleted key'),l.route_id,l.route_revision,l.requested_model,l.actual_model,l.reasoning_effort,l.thinking_budget,
l.status,l.http_status,l.is_stream,l.first_token_ms,l.duration_ms,l.input_tokens,l.cache_read_tokens,l.output_tokens,l.reasoning_tokens,
l.cost_micro_usd,(SELECT CASE WHEN COUNT(*)>1 THEN COUNT(*)-1 ELSE 0 END FROM request_attempts a WHERE a.request_id=l.id),l.error_message,l.started_at,l.finished_at FROM request_logs l LEFT JOIN downstream_keys k ON k.id=l.downstream_key_id`

func scanRequestLog(row scanner) (RequestLog, error) {
	var item RequestLog
	var keyID, routeID, revision, thinkingBudget, httpStatus, firstToken, duration, input, cacheRead, output, reasoning, finished sql.NullInt64
	var actualModel, effort, errorMessage sql.NullString
	var stream int
	err := row.Scan(&item.ID, &keyID, &item.KeyName, &routeID, &revision, &item.RequestedModel, &actualModel, &effort, &thinkingBudget,
		&item.Status, &httpStatus, &stream, &firstToken, &duration, &input, &cacheRead, &output, &reasoning,
		&item.CostMicroUSD, &item.SwitchCount, &errorMessage, &item.StartedAt, &finished)
	if err != nil {
		return RequestLog{}, err
	}
	item.DownstreamKeyID = int64Ptr(keyID)
	item.RouteID = int64Ptr(routeID)
	item.RouteRevision = int64Ptr(revision)
	item.ActualModel = actualModel.String
	item.ReasoningEffort = effort.String
	item.ThinkingBudget = int64Ptr(thinkingBudget)
	item.HTTPStatus = intPtr(httpStatus)
	item.Stream = stream == 1
	item.FirstTokenMS = int64Ptr(firstToken)
	item.DurationMS = int64Ptr(duration)
	item.InputTokens = int64Ptr(input)
	item.CacheReadTokens = int64Ptr(cacheRead)
	item.OutputTokens = int64Ptr(output)
	item.ReasoningTokens = int64Ptr(reasoning)
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
