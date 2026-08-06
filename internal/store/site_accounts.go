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

// SiteAccount stores management credentials used only for balance,
// subscription, and upstream usage synchronization.
type SiteAccount struct {
	ID               int64           `json:"id"`
	SiteID           int64           `json:"siteId"`
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

type SiteAccountSecret struct {
	SiteAccount
	AuthCipher []byte `json:"-"`
}

type SiteAccountWrite struct {
	SiteID       int64
	AdapterKind  string
	APIOrigin    string
	AuthCipher   []byte
	Enabled      bool
	Capabilities json.RawMessage
}

type SiteAccountUpdate struct {
	AdapterKind  string
	APIOrigin    string
	AuthCipher   []byte
	ReplaceAuth  bool
	Enabled      bool
	Capabilities json.RawMessage
}

type SiteAccountSnapshot struct {
	ID            int64           `json:"id"`
	SiteAccountID int64           `json:"siteAccountId"`
	Snapshot      json.RawMessage `json:"snapshot"`
	CapturedAt    int64           `json:"capturedAt"`
}

type SiteAccountUsageRecord struct {
	ID            int64           `json:"id"`
	SiteAccountID int64           `json:"siteAccountId"`
	DedupeKey     string          `json:"dedupeKey"`
	ExternalID    string          `json:"externalId,omitempty"`
	ModelName     string          `json:"modelName,omitempty"`
	Amount        string          `json:"amount,omitempty"`
	Unit          string          `json:"unit,omitempty"`
	Raw           json.RawMessage `json:"raw"`
	OccurredAt    *int64          `json:"occurredAt,omitempty"`
	SyncedAt      int64           `json:"syncedAt"`
}

type SiteAccountSyncSuccess struct {
	AttemptedAt       int64
	SucceededAt       int64
	SnapshotAt        int64
	Capabilities      json.RawMessage
	Snapshot          json.RawMessage
	Usage             []UpstreamAccountUsageWrite
	RotatedAuthCipher []byte
}

type SiteAccountSyncFailure struct {
	AttemptedAt       int64
	State             string
	ErrorCode         string
	ErrorMessage      string
	Capabilities      json.RawMessage
	RotatedAuthCipher []byte
}

func (s *Store) CreateSiteAccount(ctx context.Context, input SiteAccountWrite) (int64, error) {
	adapter, origin, err := normalizeSiteAccountIdentity(input.AdapterKind, input.APIOrigin)
	if err != nil {
		return 0, err
	}
	if input.SiteID <= 0 {
		return 0, fmt.Errorf("site account site is required")
	}
	if len(input.AuthCipher) == 0 {
		return 0, fmt.Errorf("site account auth cipher is required")
	}
	capabilities, err := normalizeAccountJSON(input.Capabilities, "{}")
	if err != nil {
		return 0, fmt.Errorf("normalize site account capabilities: %w", err)
	}
	now := NowMS()
	result, err := s.DB.ExecContext(ctx, `INSERT INTO site_accounts(
site_id,adapter_kind,api_origin,auth_cipher,enabled,capabilities_json,sync_state,created_at,updated_at)
VALUES (?,?,?,?,?,?,'pending',?,?)`, input.SiteID, adapter, origin, input.AuthCipher, boolInt(input.Enabled), capabilities, now, now)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) ListSiteAccounts(ctx context.Context) ([]SiteAccount, error) {
	rows, err := s.DB.QueryContext(ctx, siteAccountSelect+` ORDER BY a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]SiteAccount, 0)
	for rows.Next() {
		item, err := scanSiteAccount(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetSiteAccount(ctx context.Context, siteID int64) (SiteAccount, error) {
	return scanSiteAccount(s.DB.QueryRowContext(ctx, siteAccountSelect+` WHERE a.site_id=?`, siteID))
}

func (s *Store) GetSiteAccountSecret(ctx context.Context, siteID int64) (SiteAccountSecret, error) {
	var result SiteAccountSecret
	var enabled int
	var capabilities []byte
	var lastAttempt, lastSuccess sql.NullInt64
	var lastErrorCode, lastErrorMessage sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT id,site_id,adapter_kind,api_origin,auth_cipher,enabled,
capabilities_json,sync_state,last_attempt_at,last_success_at,last_error_code,last_error_message,created_at,updated_at
FROM site_accounts WHERE site_id=?`, siteID).Scan(
		&result.ID, &result.SiteID, &result.AdapterKind, &result.APIOrigin, &result.AuthCipher, &enabled,
		&capabilities, &result.SyncState, &lastAttempt, &lastSuccess, &lastErrorCode, &lastErrorMessage,
		&result.CreatedAt, &result.UpdatedAt,
	)
	if err != nil {
		return SiteAccountSecret{}, err
	}
	result.AuthConfigured = len(result.AuthCipher) > 0
	result.Enabled = enabled == 1
	result.Capabilities = json.RawMessage(capabilities)
	setSiteAccountNullableFields(&result.SiteAccount, lastAttempt, lastSuccess, lastErrorCode, lastErrorMessage)
	return result, nil
}

func (s *Store) UpdateSiteAccount(ctx context.Context, siteID int64, input SiteAccountUpdate) error {
	adapter, origin, err := normalizeSiteAccountIdentity(input.AdapterKind, input.APIOrigin)
	if err != nil {
		return err
	}
	if input.ReplaceAuth && len(input.AuthCipher) == 0 {
		return fmt.Errorf("replacement site account auth cipher is required")
	}
	replaceCapabilities := len(bytes.TrimSpace(input.Capabilities)) > 0
	capabilities := "{}"
	if replaceCapabilities {
		capabilities, err = normalizeAccountJSON(input.Capabilities, "{}")
		if err != nil {
			return fmt.Errorf("normalize site account capabilities: %w", err)
		}
	}
	now := NowMS()
	result, err := s.DB.ExecContext(ctx, `UPDATE site_accounts SET
adapter_kind=?,api_origin=?,enabled=?,
auth_cipher=CASE WHEN ?=1 THEN ? ELSE auth_cipher END,
capabilities_json=CASE WHEN ?=1 THEN ? ELSE capabilities_json END,
sync_state='pending',last_error_code=NULL,last_error_message=NULL,updated_at=?
WHERE site_id=?`, adapter, origin, boolInt(input.Enabled), boolInt(input.ReplaceAuth), input.AuthCipher,
		boolInt(replaceCapabilities), capabilities, now, siteID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteSiteAccount(ctx context.Context, siteID int64) error {
	result, err := s.DB.ExecContext(ctx, "DELETE FROM site_accounts WHERE site_id=?", siteID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) GetLatestSiteAccountSnapshot(ctx context.Context, siteID int64) (SiteAccountSnapshot, error) {
	var item SiteAccountSnapshot
	var snapshot []byte
	err := s.DB.QueryRowContext(ctx, `SELECT sn.id,sn.site_account_id,sn.snapshot_json,sn.captured_at
FROM site_account_snapshots sn
JOIN site_accounts a ON a.id=sn.site_account_id
WHERE a.site_id=? ORDER BY sn.captured_at DESC,sn.id DESC LIMIT 1`, siteID).
		Scan(&item.ID, &item.SiteAccountID, &snapshot, &item.CapturedAt)
	if err != nil {
		return SiteAccountSnapshot{}, err
	}
	item.Snapshot = json.RawMessage(snapshot)
	return item, nil
}

func (s *Store) UpdateSiteAccountSyncSuccess(ctx context.Context, siteID int64, input SiteAccountSyncSuccess) error {
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
		return fmt.Errorf("normalize site account snapshot: %w", err)
	}
	replaceCapabilities := len(bytes.TrimSpace(input.Capabilities)) > 0
	capabilities := "{}"
	if replaceCapabilities {
		capabilities, err = normalizeAccountJSON(input.Capabilities, "{}")
		if err != nil {
			return fmt.Errorf("normalize site account capabilities: %w", err)
		}
	}
	if input.RotatedAuthCipher != nil && len(input.RotatedAuthCipher) == 0 {
		return fmt.Errorf("rotated site account auth cipher cannot be empty")
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
	if err := tx.QueryRowContext(ctx, `UPDATE site_accounts SET updated_at=updated_at
WHERE site_id=? RETURNING id`, siteID).Scan(&accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO site_account_snapshots(site_account_id,snapshot_json,captured_at)
VALUES (?,?,?)`, accountID, snapshot, snapshotAt); err != nil {
		return err
	}
	for _, item := range usage {
		syncedAt := item.SyncedAt
		if syncedAt == 0 {
			syncedAt = succeededAt
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO site_account_usage_records(
site_account_id,dedupe_key,external_id,model_name,amount_text,unit,raw_json,occurred_at,synced_at)
VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT(site_account_id,dedupe_key) DO UPDATE SET
external_id=COALESCE(excluded.external_id,site_account_usage_records.external_id),
model_name=COALESCE(excluded.model_name,site_account_usage_records.model_name),
amount_text=COALESCE(excluded.amount_text,site_account_usage_records.amount_text),
unit=COALESCE(excluded.unit,site_account_usage_records.unit),raw_json=excluded.raw_json,
occurred_at=COALESCE(excluded.occurred_at,site_account_usage_records.occurred_at),
synced_at=MAX(excluded.synced_at,site_account_usage_records.synced_at)`,
			accountID, item.DedupeKey, nullableString(strings.TrimSpace(item.ExternalID)), nullableString(strings.TrimSpace(item.ModelName)),
			nullableString(strings.TrimSpace(item.Amount)), nullableString(strings.TrimSpace(item.Unit)), item.raw, accountNullableInt64(item.OccurredAt), syncedAt); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE site_accounts SET
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

func (s *Store) UpdateSiteAccountSyncFailure(ctx context.Context, siteID int64, input SiteAccountSyncFailure) error {
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
			return fmt.Errorf("normalize site account capabilities: %w", err)
		}
	}
	if input.RotatedAuthCipher != nil && len(input.RotatedAuthCipher) == 0 {
		return fmt.Errorf("rotated site account auth cipher cannot be empty")
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE site_accounts SET
auth_cipher=CASE WHEN ?=1 THEN ? ELSE auth_cipher END,
capabilities_json=CASE WHEN ?=1 THEN ? ELSE capabilities_json END,
sync_state=?,last_attempt_at=?,last_error_code=?,last_error_message=?,updated_at=?
WHERE site_id=?`, boolInt(input.RotatedAuthCipher != nil), input.RotatedAuthCipher,
		boolInt(replaceCapabilities), capabilities, state, attemptedAt,
		nullableString(strings.TrimSpace(input.ErrorCode)), nullableString(redact.String(strings.TrimSpace(input.ErrorMessage))), attemptedAt, siteID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ListSiteAccountUsage(ctx context.Context, siteID int64, query UpstreamAccountUsageQuery) ([]SiteAccountUsageRecord, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	statement := `SELECT ur.id,ur.site_account_id,ur.dedupe_key,ur.external_id,ur.model_name,
ur.amount_text,ur.unit,ur.raw_json,ur.occurred_at,ur.synced_at
FROM site_account_usage_records ur
JOIN site_accounts a ON a.id=ur.site_account_id
WHERE a.site_id=?`
	args := []any{siteID}
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
	items := make([]SiteAccountUsageRecord, 0)
	for rows.Next() {
		var item SiteAccountUsageRecord
		var externalID, modelName, amount, unit sql.NullString
		var raw []byte
		var occurredAt sql.NullInt64
		if err := rows.Scan(&item.ID, &item.SiteAccountID, &item.DedupeKey, &externalID, &modelName,
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

const siteAccountSelect = `SELECT a.id,a.site_id,a.adapter_kind,a.api_origin,
CASE WHEN length(a.auth_cipher)>0 THEN 1 ELSE 0 END,a.enabled,a.capabilities_json,a.sync_state,
a.last_attempt_at,a.last_success_at,a.last_error_code,a.last_error_message,a.created_at,a.updated_at
FROM site_accounts a`

func scanSiteAccount(row scanner) (SiteAccount, error) {
	var item SiteAccount
	var configured, enabled int
	var capabilities []byte
	var lastAttempt, lastSuccess sql.NullInt64
	var lastErrorCode, lastErrorMessage sql.NullString
	err := row.Scan(&item.ID, &item.SiteID, &item.AdapterKind, &item.APIOrigin, &configured, &enabled,
		&capabilities, &item.SyncState, &lastAttempt, &lastSuccess, &lastErrorCode, &lastErrorMessage,
		&item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return SiteAccount{}, err
	}
	item.AuthConfigured = configured == 1
	item.Enabled = enabled == 1
	item.Capabilities = json.RawMessage(capabilities)
	setSiteAccountNullableFields(&item, lastAttempt, lastSuccess, lastErrorCode, lastErrorMessage)
	return item, nil
}

func setSiteAccountNullableFields(item *SiteAccount, lastAttempt, lastSuccess sql.NullInt64, lastErrorCode, lastErrorMessage sql.NullString) {
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

func normalizeSiteAccountIdentity(adapterKind, apiOrigin string) (string, string, error) {
	adapterKind = strings.ToLower(strings.TrimSpace(adapterKind))
	apiOrigin = strings.TrimRight(strings.TrimSpace(apiOrigin), "/")
	if adapterKind == "" {
		return "", "", fmt.Errorf("site account adapter kind is required")
	}
	if apiOrigin == "" {
		return "", "", fmt.Errorf("site account API origin is required")
	}
	return adapterKind, apiOrigin, nil
}
