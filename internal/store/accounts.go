package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LuTianTian001/JieShan/internal/redact"
)

type UpstreamAccount struct {
	ID               int64           `json:"id"`
	UpstreamID       int64           `json:"upstreamId"`
	AdapterKind      string          `json:"adapterKind"`
	APIOrigin        string          `json:"apiOrigin"`
	AuthConfigured   bool            `json:"authConfigured"`
	Enabled          bool            `json:"enabled"`
	Capabilities     json.RawMessage `json:"capabilities"`
	SyncState        string          `json:"syncState"`
	LastAttemptAt    *int64          `json:"lastAttemptAt,omitempty"`
	LastSuccessAt    *int64          `json:"lastSuccessAt,omitempty"`
	LastErrorCode    string          `json:"lastErrorCode,omitempty"`
	LastErrorMessage string          `json:"lastErrorMessage,omitempty"`
	CreatedAt        int64           `json:"createdAt"`
	UpdatedAt        int64           `json:"updatedAt"`
}

type UpstreamAccountSecret struct {
	UpstreamAccount
	AuthCipher []byte `json:"-"`
}

type UpstreamAccountWrite struct {
	UpstreamID   int64
	AdapterKind  string
	APIOrigin    string
	AuthCipher   []byte
	Enabled      bool
	Capabilities json.RawMessage
}

type UpstreamAccountUpdate struct {
	AdapterKind  string
	APIOrigin    string
	AuthCipher   []byte
	ReplaceAuth  bool
	Enabled      bool
	Capabilities json.RawMessage
}

type UpstreamAccountSnapshot struct {
	ID                int64           `json:"id"`
	UpstreamAccountID int64           `json:"upstreamAccountId"`
	Snapshot          json.RawMessage `json:"snapshot"`
	CapturedAt        int64           `json:"capturedAt"`
}

type UpstreamAccountUsageRecord struct {
	ID                int64           `json:"id"`
	UpstreamAccountID int64           `json:"upstreamAccountId"`
	DedupeKey         string          `json:"dedupeKey"`
	ExternalID        string          `json:"externalId,omitempty"`
	ModelName         string          `json:"modelName,omitempty"`
	Amount            string          `json:"amount,omitempty"`
	Unit              string          `json:"unit,omitempty"`
	Raw               json.RawMessage `json:"raw"`
	OccurredAt        *int64          `json:"occurredAt,omitempty"`
	SyncedAt          int64           `json:"syncedAt"`
}

type UpstreamAccountUsageWrite struct {
	DedupeKey  string
	ExternalID string
	ModelName  string
	Amount     string
	Unit       string
	Raw        json.RawMessage
	OccurredAt *int64
	SyncedAt   int64
}

type UpstreamAccountSyncSuccess struct {
	AttemptedAt       int64
	SucceededAt       int64
	SnapshotAt        int64
	Capabilities      json.RawMessage
	Snapshot          json.RawMessage
	Usage             []UpstreamAccountUsageWrite
	RotatedAuthCipher []byte
}

type UpstreamAccountSyncFailure struct {
	AttemptedAt       int64
	State             string
	ErrorCode         string
	ErrorMessage      string
	Capabilities      json.RawMessage
	RotatedAuthCipher []byte
}

type UpstreamAccountUsageQuery struct {
	BeforeID int64
	SinceAt  int64
	Limit    int
}

func (s *Store) CreateUpstreamAccount(ctx context.Context, input UpstreamAccountWrite) (int64, error) {
	adapter, origin, err := normalizeUpstreamAccountIdentity(input.AdapterKind, input.APIOrigin)
	if err != nil {
		return 0, err
	}
	if len(input.AuthCipher) == 0 {
		return 0, fmt.Errorf("upstream account auth cipher is required")
	}
	capabilities, err := normalizeAccountJSON(input.Capabilities, "{}")
	if err != nil {
		return 0, fmt.Errorf("normalize upstream account capabilities: %w", err)
	}
	now := NowMS()
	result, err := s.DB.ExecContext(ctx, `INSERT INTO upstream_accounts(
upstream_id,adapter_kind,api_origin,auth_cipher,enabled,capabilities_json,sync_state,created_at,updated_at)
VALUES (?,?,?,?,?,?,'pending',?,?)`, input.UpstreamID, adapter, origin, input.AuthCipher, boolInt(input.Enabled), capabilities, now, now)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) ListUpstreamAccounts(ctx context.Context) ([]UpstreamAccount, error) {
	rows, err := s.DB.QueryContext(ctx, upstreamAccountSelect+` ORDER BY a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]UpstreamAccount, 0)
	for rows.Next() {
		item, err := scanUpstreamAccount(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetUpstreamAccount(ctx context.Context, upstreamID int64) (UpstreamAccount, error) {
	return scanUpstreamAccount(s.DB.QueryRowContext(ctx, upstreamAccountSelect+` WHERE a.upstream_id=?`, upstreamID))
}

func (s *Store) GetUpstreamAccountSecret(ctx context.Context, upstreamID int64) (UpstreamAccountSecret, error) {
	var result UpstreamAccountSecret
	var enabled int
	var capabilities []byte
	var lastAttempt, lastSuccess sql.NullInt64
	var lastErrorCode, lastErrorMessage sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT id,upstream_id,adapter_kind,api_origin,auth_cipher,enabled,
capabilities_json,sync_state,last_attempt_at,last_success_at,last_error_code,last_error_message,created_at,updated_at
FROM upstream_accounts WHERE upstream_id=?`, upstreamID).Scan(
		&result.ID, &result.UpstreamID, &result.AdapterKind, &result.APIOrigin, &result.AuthCipher, &enabled,
		&capabilities, &result.SyncState, &lastAttempt, &lastSuccess, &lastErrorCode, &lastErrorMessage,
		&result.CreatedAt, &result.UpdatedAt,
	)
	if err != nil {
		return UpstreamAccountSecret{}, err
	}
	result.AuthConfigured = len(result.AuthCipher) > 0
	result.Enabled = enabled == 1
	result.Capabilities = json.RawMessage(capabilities)
	setAccountNullableFields(&result.UpstreamAccount, lastAttempt, lastSuccess, lastErrorCode, lastErrorMessage)
	return result, nil
}

func (s *Store) UpdateUpstreamAccount(ctx context.Context, upstreamID int64, input UpstreamAccountUpdate) error {
	adapter, origin, err := normalizeUpstreamAccountIdentity(input.AdapterKind, input.APIOrigin)
	if err != nil {
		return err
	}
	if input.ReplaceAuth && len(input.AuthCipher) == 0 {
		return fmt.Errorf("replacement upstream account auth cipher is required")
	}
	replaceCapabilities := len(bytes.TrimSpace(input.Capabilities)) > 0
	capabilities := "{}"
	if replaceCapabilities {
		capabilities, err = normalizeAccountJSON(input.Capabilities, "{}")
		if err != nil {
			return fmt.Errorf("normalize upstream account capabilities: %w", err)
		}
	}
	now := NowMS()
	result, err := s.DB.ExecContext(ctx, `UPDATE upstream_accounts SET
adapter_kind=?,api_origin=?,enabled=?,
auth_cipher=CASE WHEN ?=1 THEN ? ELSE auth_cipher END,
capabilities_json=CASE WHEN ?=1 THEN ? ELSE capabilities_json END,
sync_state='pending',last_error_code=NULL,last_error_message=NULL,updated_at=?
WHERE upstream_id=?`, adapter, origin, boolInt(input.Enabled), boolInt(input.ReplaceAuth), input.AuthCipher,
		boolInt(replaceCapabilities), capabilities, now, upstreamID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteUpstreamAccount(ctx context.Context, upstreamID int64) error {
	result, err := s.DB.ExecContext(ctx, "DELETE FROM upstream_accounts WHERE upstream_id=?", upstreamID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) GetLatestUpstreamAccountSnapshot(ctx context.Context, upstreamID int64) (UpstreamAccountSnapshot, error) {
	var item UpstreamAccountSnapshot
	var snapshot []byte
	err := s.DB.QueryRowContext(ctx, `SELECT sn.id,sn.upstream_account_id,sn.snapshot_json,sn.captured_at
FROM upstream_account_snapshots sn
JOIN upstream_accounts a ON a.id=sn.upstream_account_id
WHERE a.upstream_id=? ORDER BY sn.captured_at DESC,sn.id DESC LIMIT 1`, upstreamID).
		Scan(&item.ID, &item.UpstreamAccountID, &snapshot, &item.CapturedAt)
	if err != nil {
		return UpstreamAccountSnapshot{}, err
	}
	item.Snapshot = json.RawMessage(snapshot)
	return item, nil
}

func (s *Store) UpdateSyncSuccess(ctx context.Context, upstreamID int64, input UpstreamAccountSyncSuccess) error {
	succeededAt := input.SucceededAt
	if succeededAt == 0 {
		succeededAt = NowMS()
	}
	attemptedAt := input.AttemptedAt
	if attemptedAt == 0 {
		attemptedAt = succeededAt
	}
	snapshotAt := input.SnapshotAt
	if snapshotAt == 0 {
		snapshotAt = succeededAt
	}
	snapshot, err := normalizeAccountJSON(input.Snapshot, "{}")
	if err != nil {
		return fmt.Errorf("normalize upstream account snapshot: %w", err)
	}
	replaceCapabilities := len(bytes.TrimSpace(input.Capabilities)) > 0
	capabilities := "{}"
	if replaceCapabilities {
		capabilities, err = normalizeAccountJSON(input.Capabilities, "{}")
		if err != nil {
			return fmt.Errorf("normalize upstream account capabilities: %w", err)
		}
	}
	if input.RotatedAuthCipher != nil && len(input.RotatedAuthCipher) == 0 {
		return fmt.Errorf("rotated upstream account auth cipher cannot be empty")
	}
	type preparedUsage struct {
		UpstreamAccountUsageWrite
		raw string
	}
	usage := make([]preparedUsage, 0, len(input.Usage))
	for index, item := range input.Usage {
		item.DedupeKey = strings.TrimSpace(item.DedupeKey)
		if item.DedupeKey == "" {
			return fmt.Errorf("usage record %d dedupe key is required", index)
		}
		raw, err := normalizeAccountJSON(item.Raw, "{}")
		if err != nil {
			return fmt.Errorf("normalize usage record %d: %w", index, err)
		}
		usage = append(usage, preparedUsage{UpstreamAccountUsageWrite: item, raw: raw})
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var accountID int64
	// Start with a write so SQLite cannot promote a stale read snapshot later.
	if err := tx.QueryRowContext(ctx, `UPDATE upstream_accounts SET updated_at=updated_at
WHERE upstream_id=? RETURNING id`, upstreamID).Scan(&accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO upstream_account_snapshots(upstream_account_id,snapshot_json,captured_at)
VALUES (?,?,?)`, accountID, snapshot, snapshotAt); err != nil {
		return err
	}
	for _, item := range usage {
		syncedAt := item.SyncedAt
		if syncedAt == 0 {
			syncedAt = succeededAt
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO upstream_account_usage_records(
upstream_account_id,dedupe_key,external_id,model_name,amount_text,unit,raw_json,occurred_at,synced_at)
VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT(upstream_account_id,dedupe_key) DO NOTHING`,
			accountID, item.DedupeKey, nullableString(strings.TrimSpace(item.ExternalID)), nullableString(strings.TrimSpace(item.ModelName)),
			nullableString(strings.TrimSpace(item.Amount)), nullableString(strings.TrimSpace(item.Unit)), item.raw, accountNullableInt64(item.OccurredAt), syncedAt); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE upstream_accounts SET
auth_cipher=CASE WHEN ?=1 THEN ? ELSE auth_cipher END,
capabilities_json=CASE WHEN ?=1 THEN ? ELSE capabilities_json END,
sync_state='healthy',last_attempt_at=?,last_success_at=?,last_error_code=NULL,last_error_message=NULL,updated_at=?
WHERE id=?`, boolInt(input.RotatedAuthCipher != nil), input.RotatedAuthCipher,
		boolInt(replaceCapabilities), capabilities, attemptedAt, succeededAt, succeededAt, accountID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (s *Store) UpdateSyncFailure(ctx context.Context, upstreamID int64, input UpstreamAccountSyncFailure) error {
	attemptedAt := input.AttemptedAt
	if attemptedAt == 0 {
		attemptedAt = NowMS()
	}
	state := strings.TrimSpace(input.State)
	if state == "" {
		state = "error"
	}
	replaceCapabilities := len(bytes.TrimSpace(input.Capabilities)) > 0
	capabilities := "{}"
	var err error
	if replaceCapabilities {
		capabilities, err = normalizeAccountJSON(input.Capabilities, "{}")
		if err != nil {
			return fmt.Errorf("normalize upstream account capabilities: %w", err)
		}
	}
	if input.RotatedAuthCipher != nil && len(input.RotatedAuthCipher) == 0 {
		return fmt.Errorf("rotated upstream account auth cipher cannot be empty")
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE upstream_accounts SET
auth_cipher=CASE WHEN ?=1 THEN ? ELSE auth_cipher END,
capabilities_json=CASE WHEN ?=1 THEN ? ELSE capabilities_json END,
sync_state=?,last_attempt_at=?,last_error_code=?,last_error_message=?,updated_at=?
WHERE upstream_id=?`, boolInt(input.RotatedAuthCipher != nil), input.RotatedAuthCipher,
		boolInt(replaceCapabilities), capabilities, state, attemptedAt,
		nullableString(strings.TrimSpace(input.ErrorCode)), nullableString(redact.String(strings.TrimSpace(input.ErrorMessage))), attemptedAt, upstreamID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ListUsage(ctx context.Context, upstreamID int64, query UpstreamAccountUsageQuery) ([]UpstreamAccountUsageRecord, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	statement := `SELECT ur.id,ur.upstream_account_id,ur.dedupe_key,ur.external_id,ur.model_name,
ur.amount_text,ur.unit,ur.raw_json,ur.occurred_at,ur.synced_at
FROM upstream_account_usage_records ur
JOIN upstream_accounts a ON a.id=ur.upstream_account_id
WHERE a.upstream_id=?`
	args := []any{upstreamID}
	if query.BeforeID > 0 {
		statement += " AND ur.id<?"
		args = append(args, query.BeforeID)
	}
	if query.SinceAt > 0 {
		statement += " AND COALESCE(ur.occurred_at,ur.synced_at)>=?"
		args = append(args, query.SinceAt)
	}
	statement += " ORDER BY COALESCE(ur.occurred_at,ur.synced_at) DESC,ur.id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]UpstreamAccountUsageRecord, 0)
	for rows.Next() {
		var item UpstreamAccountUsageRecord
		var externalID, modelName, amount, unit sql.NullString
		var raw []byte
		var occurredAt sql.NullInt64
		if err := rows.Scan(&item.ID, &item.UpstreamAccountID, &item.DedupeKey, &externalID, &modelName,
			&amount, &unit, &raw, &occurredAt, &item.SyncedAt); err != nil {
			return nil, err
		}
		item.ExternalID = externalID.String
		item.ModelName = modelName.String
		item.Amount = amount.String
		item.Unit = unit.String
		item.Raw = json.RawMessage(raw)
		if occurredAt.Valid {
			value := occurredAt.Int64
			item.OccurredAt = &value
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) DeleteOldAccountData(ctx context.Context, cutoffMS int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM upstream_account_usage_records
WHERE COALESCE(occurred_at,synced_at)<?`, cutoffMS); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM upstream_account_snapshots AS expired
WHERE expired.captured_at<?
  AND EXISTS (
    SELECT 1 FROM upstream_account_snapshots AS newer
    WHERE newer.upstream_account_id=expired.upstream_account_id
      AND (newer.captured_at>expired.captured_at
        OR (newer.captured_at=expired.captured_at AND newer.id>expired.id))
  )`, cutoffMS); err != nil {
		return err
	}
	return tx.Commit()
}

const upstreamAccountSelect = `SELECT a.id,a.upstream_id,a.adapter_kind,a.api_origin,
CASE WHEN length(a.auth_cipher)>0 THEN 1 ELSE 0 END,a.enabled,a.capabilities_json,a.sync_state,
a.last_attempt_at,a.last_success_at,a.last_error_code,a.last_error_message,a.created_at,a.updated_at
FROM upstream_accounts a`

func scanUpstreamAccount(row scanner) (UpstreamAccount, error) {
	var item UpstreamAccount
	var configured, enabled int
	var capabilities []byte
	var lastAttempt, lastSuccess sql.NullInt64
	var lastErrorCode, lastErrorMessage sql.NullString
	err := row.Scan(&item.ID, &item.UpstreamID, &item.AdapterKind, &item.APIOrigin, &configured, &enabled,
		&capabilities, &item.SyncState, &lastAttempt, &lastSuccess, &lastErrorCode, &lastErrorMessage,
		&item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return UpstreamAccount{}, err
	}
	item.AuthConfigured = configured == 1
	item.Enabled = enabled == 1
	item.Capabilities = json.RawMessage(capabilities)
	setAccountNullableFields(&item, lastAttempt, lastSuccess, lastErrorCode, lastErrorMessage)
	return item, nil
}

func setAccountNullableFields(item *UpstreamAccount, lastAttempt, lastSuccess sql.NullInt64, lastErrorCode, lastErrorMessage sql.NullString) {
	if lastAttempt.Valid {
		value := lastAttempt.Int64
		item.LastAttemptAt = &value
	}
	if lastSuccess.Valid {
		value := lastSuccess.Int64
		item.LastSuccessAt = &value
	}
	item.LastErrorCode = lastErrorCode.String
	item.LastErrorMessage = lastErrorMessage.String
}

func normalizeUpstreamAccountIdentity(adapterKind, apiOrigin string) (string, string, error) {
	adapterKind = strings.ToLower(strings.TrimSpace(adapterKind))
	apiOrigin = strings.TrimRight(strings.TrimSpace(apiOrigin), "/")
	if adapterKind == "" {
		return "", "", fmt.Errorf("upstream account adapter kind is required")
	}
	if apiOrigin == "" {
		return "", "", fmt.Errorf("upstream account API origin is required")
	}
	return adapterKind, apiOrigin, nil
}

func normalizeAccountJSON(value json.RawMessage, fallback string) (string, error) {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 {
		return fallback, nil
	}
	if !json.Valid(trimmed) {
		return "", fmt.Errorf("invalid JSON")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err != nil {
		return "", err
	}
	return compact.String(), nil
}

func accountNullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
