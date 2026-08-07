package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var ErrSiteAccountUnavailable = errors.New("site account connection is unavailable")

type SiteAccountConnection struct {
	ID                   int64
	SiteID               int64
	SiteName             string
	AdapterKind          string
	Origin               string
	SecretConfigured     bool
	CipherVersion        int64
	Enabled              bool
	LastSessionRefreshAt *int64
	LastBalanceRefreshAt *int64
	LastUsageRefreshAt   *int64
	LastErrorOperation   string
	LastErrorCode        string
	LastErrorAt          *int64
	Revision             int64
	CreatedAt            int64
	UpdatedAt            int64
}

type SealedSiteAccountConnectionInput struct {
	AdapterKind   string
	Origin        string
	CipherVersion int64
	Enabled       bool
}

type SiteAccountConnectionUpdate struct {
	ExpectedRevision int64
	AdapterKind      string
	Origin           string
	Enabled          bool
}

type SiteAccountSecret struct {
	Connection SiteAccountConnection
	Ciphertext []byte
}

type SiteBalanceSnapshot struct {
	ID                      int64
	SiteAccountConnectionID int64
	SiteID                  int64
	AdapterKind             string
	AccountRemoteID         string
	AccountName             string
	AvailableValue          string
	AvailableUnit           string
	UsedValue               *string
	UsedUnit                *string
	CapturedAt              int64
	CreatedAt               int64
}

type SiteBalanceSnapshotWrite struct {
	AccountRemoteID string
	AccountName     string
	AvailableValue  string
	AvailableUnit   string
	UsedValue       *string
	UsedUnit        *string
	CapturedAt      int64
}

type SiteUsageRecord struct {
	ID                      int64
	SiteAccountConnectionID int64
	SiteID                  int64
	AdapterKind             string
	DedupKey                string
	RemoteID                string
	RequestID               string
	UpstreamRequestID       string
	OccurredAt              int64
	Model                   string
	UpstreamModel           string
	Status                  string
	HTTPStatus              *int
	InputTokens             *int64
	OutputTokens            *int64
	CacheReadTokens         *int64
	CacheWriteTokens        *int64
	ReasoningTokens         *int64
	TotalTokens             *int64
	ChargeValue             *string
	ChargeUnit              *string
	DurationMS              *int64
	APIKeyName              string
	SourceFetchedAt         int64
	CreatedAt               int64
}

type SiteUsageRecordWrite struct {
	DedupKey          string
	RemoteID          string
	RequestID         string
	UpstreamRequestID string
	OccurredAt        int64
	Model             string
	UpstreamModel     string
	Status            string
	HTTPStatus        *int
	InputTokens       *int64
	OutputTokens      *int64
	CacheReadTokens   *int64
	CacheWriteTokens  *int64
	ReasoningTokens   *int64
	TotalTokens       *int64
	ChargeValue       *string
	ChargeUnit        *string
	DurationMS        *int64
	APIKeyName        string
	SourceFetchedAt   int64
}

type SiteUsageSaveResult struct {
	Inserted     int
	Deduplicated int
}

type SiteUsageListFilter struct {
	Limit            int
	BeforeOccurredAt *int64
	BeforeID         *int64
	From             *int64
	To               *int64
	Model            string
	Status           string
	APIKey           string
	RequestID        string
	Search           string
}

type SiteUsagePage struct {
	Records              []SiteUsageRecord
	HasMore              bool
	NextBeforeOccurredAt *int64
	NextBeforeID         *int64
}

// CreateSealedSiteAccountConnection allocates the record before sealing so
// authenticated encryption can bind account secrets to both connection and
// site IDs. The placeholder is rolled back when sealing fails.
func (s *Store) CreateSealedSiteAccountConnection(
	ctx context.Context,
	siteID int64,
	input SealedSiteAccountConnectionInput,
	seal func(connectionID, siteID int64) ([]byte, error),
) (SiteAccountConnection, error) {
	input.AdapterKind = strings.ToLower(strings.TrimSpace(input.AdapterKind))
	input.Origin = strings.TrimRight(strings.TrimSpace(input.Origin), "/")
	if siteID <= 0 || input.AdapterKind == "" || input.Origin == "" || input.CipherVersion <= 0 || seal == nil {
		return SiteAccountConnection{}, errors.New("site, adapter, origin, cipher version, and sealer are required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return SiteAccountConnection{}, err
	}
	defer tx.Rollback()
	now := NowMS()
	result, err := tx.ExecContext(ctx, `INSERT INTO site_account_connections(
site_id,adapter_kind,origin,secrets_cipher,cipher_version,enabled,revision,created_at,updated_at)
VALUES (?,?,?,X'00',?,?,1,?,?)`, siteID, input.AdapterKind, input.Origin, input.CipherVersion, boolInt(input.Enabled), now, now)
	if err != nil {
		return SiteAccountConnection{}, normalizeInventoryConflict(err)
	}
	connectionID, err := result.LastInsertId()
	if err != nil {
		return SiteAccountConnection{}, err
	}
	ciphertext, err := seal(connectionID, siteID)
	if err != nil {
		return SiteAccountConnection{}, err
	}
	if len(ciphertext) == 0 {
		return SiteAccountConnection{}, errors.New("site account sealer returned an empty ciphertext")
	}
	result, err = tx.ExecContext(ctx, `UPDATE site_account_connections SET secrets_cipher=? WHERE id=? AND site_id=?`, ciphertext, connectionID, siteID)
	if err != nil {
		return SiteAccountConnection{}, err
	}
	if changed, changedErr := result.RowsAffected(); changedErr != nil || changed != 1 {
		if changedErr != nil {
			return SiteAccountConnection{}, changedErr
		}
		return SiteAccountConnection{}, errors.New("site account ciphertext was not stored")
	}
	item, err := scanSiteAccountConnection(tx.QueryRowContext(ctx, siteAccountSelect+` WHERE a.id=? AND a.site_id=?`, connectionID, siteID))
	if err != nil {
		return SiteAccountConnection{}, err
	}
	if err := tx.Commit(); err != nil {
		return SiteAccountConnection{}, err
	}
	return item, nil
}

func (s *Store) ListSiteAccountConnections(ctx context.Context) ([]SiteAccountConnection, error) {
	rows, err := s.DB.QueryContext(ctx, siteAccountSelect+` ORDER BY s.name COLLATE NOCASE,a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]SiteAccountConnection, 0)
	for rows.Next() {
		item, err := scanSiteAccountConnection(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetSiteAccountConnection(ctx context.Context, siteID int64) (SiteAccountConnection, error) {
	if siteID <= 0 {
		return SiteAccountConnection{}, sql.ErrNoRows
	}
	return scanSiteAccountConnection(s.DB.QueryRowContext(ctx, siteAccountSelect+` WHERE a.site_id=?`, siteID))
}

func (s *Store) LoadSiteAccountSecret(ctx context.Context, siteID int64) (SiteAccountSecret, error) {
	if siteID <= 0 {
		return SiteAccountSecret{}, sql.ErrNoRows
	}
	var secret SiteAccountSecret
	var enabled int
	var lastSession, lastBalance, lastUsage, lastErrorAt sql.NullInt64
	var lastErrorOperation, lastErrorCode sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT a.id,a.site_id,s.name,a.adapter_kind,a.origin,a.secrets_cipher,a.cipher_version,a.enabled,
a.last_session_refresh_at,a.last_balance_refresh_at,a.last_usage_refresh_at,a.last_error_operation,a.last_error_code,
a.last_error_at,a.revision,a.created_at,a.updated_at
FROM site_account_connections a JOIN sites s ON s.id=a.site_id
WHERE a.site_id=? AND a.enabled=1 AND s.enabled=1`, siteID).Scan(
		&secret.Connection.ID, &secret.Connection.SiteID, &secret.Connection.SiteName, &secret.Connection.AdapterKind,
		&secret.Connection.Origin, &secret.Ciphertext, &secret.Connection.CipherVersion, &enabled,
		&lastSession, &lastBalance, &lastUsage, &lastErrorOperation, &lastErrorCode, &lastErrorAt,
		&secret.Connection.Revision, &secret.Connection.CreatedAt, &secret.Connection.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return SiteAccountSecret{}, ErrSiteAccountUnavailable
	}
	if err != nil {
		return SiteAccountSecret{}, err
	}
	secret.Connection.Enabled = enabled == 1
	assignSiteAccountRuntime(&secret.Connection, lastSession, lastBalance, lastUsage, lastErrorOperation, lastErrorCode, lastErrorAt)
	secret.Connection.SecretConfigured = len(secret.Ciphertext) > 0
	secret.Ciphertext = append([]byte(nil), secret.Ciphertext...)
	return secret, nil
}

func (s *Store) UpdateSiteAccountConnection(ctx context.Context, siteID int64, input SiteAccountConnectionUpdate) (SiteAccountConnection, error) {
	input.AdapterKind = strings.ToLower(strings.TrimSpace(input.AdapterKind))
	input.Origin = strings.TrimRight(strings.TrimSpace(input.Origin), "/")
	if siteID <= 0 {
		return SiteAccountConnection{}, sql.ErrNoRows
	}
	if input.ExpectedRevision <= 0 || input.AdapterKind == "" || input.Origin == "" {
		return SiteAccountConnection{}, errors.New("expected revision, adapter, and origin are required")
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE site_account_connections SET
adapter_kind=?,origin=?,enabled=?,revision=revision+1,updated_at=? WHERE site_id=? AND revision=?`,
		input.AdapterKind, input.Origin, boolInt(input.Enabled), NowMS(), siteID, input.ExpectedRevision)
	if err != nil {
		return SiteAccountConnection{}, normalizeInventoryConflict(err)
	}
	if err := requireSiteAccountRevisionChange(ctx, s.DB, result, siteID); err != nil {
		return SiteAccountConnection{}, err
	}
	return s.GetSiteAccountConnection(ctx, siteID)
}

func (s *Store) ReplaceSealedSiteAccountSecret(
	ctx context.Context,
	siteID, expectedRevision, cipherVersion int64,
	ciphertext []byte,
) (SiteAccountConnection, error) {
	if siteID <= 0 {
		return SiteAccountConnection{}, sql.ErrNoRows
	}
	if expectedRevision <= 0 || cipherVersion <= 0 || len(ciphertext) == 0 {
		return SiteAccountConnection{}, errors.New("expected revision, cipher version, and ciphertext are required")
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE site_account_connections SET
secrets_cipher=?,cipher_version=?,revision=revision+1,last_error_operation=NULL,last_error_code=NULL,last_error_at=NULL,
updated_at=? WHERE site_id=? AND revision=?`, ciphertext, cipherVersion, NowMS(), siteID, expectedRevision)
	if err != nil {
		return SiteAccountConnection{}, err
	}
	if err := requireSiteAccountRevisionChange(ctx, s.DB, result, siteID); err != nil {
		return SiteAccountConnection{}, err
	}
	return s.GetSiteAccountConnection(ctx, siteID)
}

func (s *Store) DeleteSiteAccountConnection(ctx context.Context, siteID, expectedRevision int64) error {
	if siteID <= 0 {
		return sql.ErrNoRows
	}
	if expectedRevision <= 0 {
		return errors.New("expected revision is required")
	}
	result, err := s.DB.ExecContext(ctx, `DELETE FROM site_account_connections WHERE site_id=? AND revision=?`, siteID, expectedRevision)
	if err != nil {
		return err
	}
	return requireSiteAccountRevisionChange(ctx, s.DB, result, siteID)
}

func (s *Store) PersistSiteAccountSession(
	ctx context.Context,
	siteID, expectedRevision, cipherVersion int64,
	ciphertext []byte,
	refreshedAt int64,
) error {
	if siteID <= 0 || expectedRevision <= 0 || cipherVersion <= 0 || len(ciphertext) == 0 || refreshedAt <= 0 {
		return errors.New("valid site account session update is required")
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE site_account_connections SET
secrets_cipher=?,cipher_version=?,last_session_refresh_at=?,last_error_operation=NULL,last_error_code=NULL,last_error_at=NULL,
revision=revision+1,updated_at=? WHERE site_id=? AND revision=?`,
		ciphertext, cipherVersion, refreshedAt, NowMS(), siteID, expectedRevision)
	if err != nil {
		return err
	}
	return requireSiteAccountRevisionChange(ctx, s.DB, result, siteID)
}

func (s *Store) RecordSiteAccountFailure(ctx context.Context, siteID int64, operation, code string, occurredAt int64) error {
	operation = strings.TrimSpace(operation)
	code = strings.TrimSpace(code)
	if siteID <= 0 || operation == "" || code == "" || occurredAt <= 0 {
		return errors.New("site account failure requires site, operation, code, and time")
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE site_account_connections SET
last_error_operation=?,last_error_code=?,last_error_at=?,updated_at=? WHERE site_id=?`,
		operation, code, occurredAt, NowMS(), siteID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) SaveSiteBalanceSnapshot(
	ctx context.Context,
	siteID int64,
	adapterKind string,
	input SiteBalanceSnapshotWrite,
) (SiteBalanceSnapshot, error) {
	adapterKind = strings.ToLower(strings.TrimSpace(adapterKind))
	input.AvailableValue = strings.TrimSpace(input.AvailableValue)
	input.AvailableUnit = strings.TrimSpace(input.AvailableUnit)
	if siteID <= 0 || adapterKind == "" || input.AvailableValue == "" || input.AvailableUnit == "" || input.CapturedAt <= 0 {
		return SiteBalanceSnapshot{}, errors.New("valid site balance snapshot is required")
	}
	if (input.UsedValue == nil) != (input.UsedUnit == nil) {
		return SiteBalanceSnapshot{}, errors.New("used balance value and unit must be provided together")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return SiteBalanceSnapshot{}, err
	}
	defer tx.Rollback()
	var connectionID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM site_account_connections WHERE site_id=? AND adapter_kind=?`, siteID, adapterKind).Scan(&connectionID); err != nil {
		return SiteBalanceSnapshot{}, err
	}
	now := NowMS()
	result, err := tx.ExecContext(ctx, `INSERT INTO site_balance_snapshots(
site_account_connection_id,site_id,adapter_kind,account_remote_id,account_name,available_value,available_unit,
used_value,used_unit,captured_at,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, connectionID, siteID, adapterKind,
		nullableString(strings.TrimSpace(input.AccountRemoteID)), nullableString(strings.TrimSpace(input.AccountName)),
		input.AvailableValue, input.AvailableUnit, nullableStringPointer(input.UsedValue), nullableStringPointer(input.UsedUnit),
		input.CapturedAt, now)
	if err != nil {
		return SiteBalanceSnapshot{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return SiteBalanceSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE site_account_connections SET
last_balance_refresh_at=?,last_error_operation=NULL,last_error_code=NULL,last_error_at=NULL,updated_at=? WHERE id=? AND site_id=?`,
		input.CapturedAt, now, connectionID, siteID); err != nil {
		return SiteBalanceSnapshot{}, err
	}
	item, err := scanSiteBalanceSnapshot(tx.QueryRowContext(ctx, siteBalanceSelect+` WHERE id=?`, id))
	if err != nil {
		return SiteBalanceSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return SiteBalanceSnapshot{}, err
	}
	return item, nil
}

func (s *Store) GetLatestSiteBalance(ctx context.Context, siteID int64) (SiteBalanceSnapshot, error) {
	if siteID <= 0 {
		return SiteBalanceSnapshot{}, sql.ErrNoRows
	}
	return scanSiteBalanceSnapshot(s.DB.QueryRowContext(ctx, siteBalanceSelect+`
 WHERE site_id=? ORDER BY captured_at DESC,id DESC LIMIT 1`, siteID))
}

func (s *Store) SaveSiteUsageRecords(
	ctx context.Context,
	siteID int64,
	adapterKind string,
	records []SiteUsageRecordWrite,
	fetchedAt int64,
) (SiteUsageSaveResult, error) {
	adapterKind = strings.ToLower(strings.TrimSpace(adapterKind))
	if siteID <= 0 || adapterKind == "" || fetchedAt <= 0 {
		return SiteUsageSaveResult{}, errors.New("site, adapter, and fetch time are required")
	}
	if len(records) > 500 {
		return SiteUsageSaveResult{}, errors.New("site usage page exceeds 500 records")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return SiteUsageSaveResult{}, err
	}
	defer tx.Rollback()
	var connectionID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM site_account_connections WHERE site_id=? AND adapter_kind=?`, siteID, adapterKind).Scan(&connectionID); err != nil {
		return SiteUsageSaveResult{}, err
	}
	now := NowMS()
	result := SiteUsageSaveResult{}
	for index, record := range records {
		if err := validateSiteUsageWrite(record, fetchedAt); err != nil {
			return SiteUsageSaveResult{}, fmt.Errorf("site usage record %d: %w", index, err)
		}
		insert, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO site_usage_records(
site_account_connection_id,site_id,adapter_kind,dedup_key,remote_id,request_id,upstream_request_id,occurred_at,
model,upstream_model,status,http_status,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,
reasoning_tokens,total_tokens,charge_value,charge_unit,duration_ms,api_key_name,source_fetched_at,created_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, connectionID, siteID, adapterKind, strings.TrimSpace(record.DedupKey),
			nullableString(strings.TrimSpace(record.RemoteID)), nullableString(strings.TrimSpace(record.RequestID)),
			nullableString(strings.TrimSpace(record.UpstreamRequestID)), record.OccurredAt,
			nullableString(strings.TrimSpace(record.Model)), nullableString(strings.TrimSpace(record.UpstreamModel)),
			nullableString(strings.TrimSpace(record.Status)), record.HTTPStatus, record.InputTokens, record.OutputTokens,
			record.CacheReadTokens, record.CacheWriteTokens, record.ReasoningTokens, record.TotalTokens,
			nullableStringPointer(record.ChargeValue), nullableStringPointer(record.ChargeUnit), record.DurationMS,
			nullableString(strings.TrimSpace(record.APIKeyName)), fetchedAt, now)
		if err != nil {
			return SiteUsageSaveResult{}, err
		}
		changed, err := insert.RowsAffected()
		if err != nil {
			return SiteUsageSaveResult{}, err
		}
		if changed == 1 {
			result.Inserted++
		} else {
			result.Deduplicated++
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE site_account_connections SET
last_usage_refresh_at=?,last_error_operation=NULL,last_error_code=NULL,last_error_at=NULL,updated_at=? WHERE id=? AND site_id=?`,
		fetchedAt, now, connectionID, siteID); err != nil {
		return SiteUsageSaveResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return SiteUsageSaveResult{}, err
	}
	return result, nil
}

func (s *Store) ListSiteUsageRecords(ctx context.Context, siteID int64, filter SiteUsageListFilter) (SiteUsagePage, error) {
	if siteID <= 0 {
		return SiteUsagePage{}, sql.ErrNoRows
	}
	if filter.Limit < 1 || filter.Limit > 200 {
		return SiteUsagePage{}, errors.New("site usage list limit must be between 1 and 200")
	}
	if (filter.BeforeOccurredAt == nil) != (filter.BeforeID == nil) {
		return SiteUsagePage{}, errors.New("site usage cursor is incomplete")
	}
	query := strings.Builder{}
	query.WriteString(siteUsageSelect)
	query.WriteString(` WHERE site_id=?`)
	args := []any{siteID}
	if filter.BeforeOccurredAt != nil {
		query.WriteString(` AND (occurred_at<? OR (occurred_at=? AND id<?))`)
		args = append(args, *filter.BeforeOccurredAt, *filter.BeforeOccurredAt, *filter.BeforeID)
	}
	if filter.From != nil {
		query.WriteString(` AND occurred_at>=?`)
		args = append(args, *filter.From)
	}
	if filter.To != nil {
		query.WriteString(` AND occurred_at<=?`)
		args = append(args, *filter.To)
	}
	appendExactFilter := func(column, value string) {
		if value = strings.TrimSpace(value); value != "" {
			query.WriteString(` AND ` + column + `=? COLLATE NOCASE`)
			args = append(args, value)
		}
	}
	appendExactFilter("model", filter.Model)
	switch strings.ToLower(strings.TrimSpace(filter.Status)) {
	case "success":
		query.WriteString(` AND status COLLATE NOCASE IN (?,?)`)
		args = append(args, "success", "succeeded")
	case "failed":
		query.WriteString(` AND status COLLATE NOCASE IN (?,?)`)
		args = append(args, "failed", "error")
	default:
		appendExactFilter("status", filter.Status)
	}
	appendExactFilter("api_key_name", filter.APIKey)
	appendExactFilter("request_id", filter.RequestID)
	if search := strings.TrimSpace(filter.Search); search != "" {
		pattern := "%" + escapeLike(search) + "%"
		query.WriteString(` AND (model LIKE ? ESCAPE '\' OR upstream_model LIKE ? ESCAPE '\' OR
request_id LIKE ? ESCAPE '\' OR upstream_request_id LIKE ? ESCAPE '\' OR
api_key_name LIKE ? ESCAPE '\' OR status LIKE ? ESCAPE '\')`)
		for range 6 {
			args = append(args, pattern)
		}
	}
	query.WriteString(` ORDER BY occurred_at DESC,id DESC LIMIT ?`)
	args = append(args, filter.Limit+1)
	rows, err := s.DB.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return SiteUsagePage{}, err
	}
	defer rows.Close()
	items := make([]SiteUsageRecord, 0, filter.Limit+1)
	for rows.Next() {
		item, err := scanSiteUsageRecord(rows)
		if err != nil {
			return SiteUsagePage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return SiteUsagePage{}, err
	}
	page := SiteUsagePage{Records: items}
	if len(items) > filter.Limit {
		page.HasMore = true
		page.Records = items[:filter.Limit]
		last := page.Records[len(page.Records)-1]
		occurredAt, id := last.OccurredAt, last.ID
		page.NextBeforeOccurredAt = &occurredAt
		page.NextBeforeID = &id
	}
	if len(page.Records) == 0 {
		if _, err := s.GetSiteAccountConnection(ctx, siteID); err != nil {
			return SiteUsagePage{}, err
		}
	}
	return page, nil
}

const siteAccountSelect = `SELECT a.id,a.site_id,s.name,a.adapter_kind,a.origin,a.secrets_cipher,a.cipher_version,a.enabled,
a.last_session_refresh_at,a.last_balance_refresh_at,a.last_usage_refresh_at,a.last_error_operation,a.last_error_code,
a.last_error_at,a.revision,a.created_at,a.updated_at
FROM site_account_connections a JOIN sites s ON s.id=a.site_id`

const siteBalanceSelect = `SELECT id,site_account_connection_id,site_id,adapter_kind,account_remote_id,account_name,
available_value,available_unit,used_value,used_unit,captured_at,created_at FROM site_balance_snapshots`

const siteUsageSelect = `SELECT id,site_account_connection_id,site_id,adapter_kind,dedup_key,remote_id,request_id,
upstream_request_id,occurred_at,model,upstream_model,status,http_status,input_tokens,output_tokens,
cache_read_tokens,cache_write_tokens,reasoning_tokens,total_tokens,charge_value,charge_unit,duration_ms,
api_key_name,source_fetched_at,created_at FROM site_usage_records`

func scanSiteAccountConnection(row scanner) (SiteAccountConnection, error) {
	var item SiteAccountConnection
	var ciphertext []byte
	var enabled int
	var lastSession, lastBalance, lastUsage, lastErrorAt sql.NullInt64
	var lastErrorOperation, lastErrorCode sql.NullString
	err := row.Scan(&item.ID, &item.SiteID, &item.SiteName, &item.AdapterKind, &item.Origin, &ciphertext,
		&item.CipherVersion, &enabled, &lastSession, &lastBalance, &lastUsage, &lastErrorOperation,
		&lastErrorCode, &lastErrorAt, &item.Revision, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return SiteAccountConnection{}, err
	}
	item.SecretConfigured = len(ciphertext) > 0
	item.Enabled = enabled == 1
	assignSiteAccountRuntime(&item, lastSession, lastBalance, lastUsage, lastErrorOperation, lastErrorCode, lastErrorAt)
	return item, nil
}

func assignSiteAccountRuntime(
	item *SiteAccountConnection,
	lastSession, lastBalance, lastUsage sql.NullInt64,
	lastErrorOperation, lastErrorCode sql.NullString,
	lastErrorAt sql.NullInt64,
) {
	if lastSession.Valid {
		value := lastSession.Int64
		item.LastSessionRefreshAt = &value
	}
	if lastBalance.Valid {
		value := lastBalance.Int64
		item.LastBalanceRefreshAt = &value
	}
	if lastUsage.Valid {
		value := lastUsage.Int64
		item.LastUsageRefreshAt = &value
	}
	item.LastErrorOperation = lastErrorOperation.String
	item.LastErrorCode = lastErrorCode.String
	if lastErrorAt.Valid {
		value := lastErrorAt.Int64
		item.LastErrorAt = &value
	}
}

func scanSiteBalanceSnapshot(row scanner) (SiteBalanceSnapshot, error) {
	var item SiteBalanceSnapshot
	var remoteID, accountName, usedValue, usedUnit sql.NullString
	err := row.Scan(&item.ID, &item.SiteAccountConnectionID, &item.SiteID, &item.AdapterKind,
		&remoteID, &accountName, &item.AvailableValue, &item.AvailableUnit, &usedValue, &usedUnit,
		&item.CapturedAt, &item.CreatedAt)
	if err != nil {
		return SiteBalanceSnapshot{}, err
	}
	item.AccountRemoteID = remoteID.String
	item.AccountName = accountName.String
	if usedValue.Valid {
		value := usedValue.String
		item.UsedValue = &value
	}
	if usedUnit.Valid {
		value := usedUnit.String
		item.UsedUnit = &value
	}
	return item, nil
}

func scanSiteUsageRecord(row scanner) (SiteUsageRecord, error) {
	var item SiteUsageRecord
	var remoteID, requestID, upstreamRequestID, model, upstreamModel, status, apiKeyName sql.NullString
	var httpStatus sql.NullInt64
	var input, output, cacheRead, cacheWrite, reasoning, total, duration sql.NullInt64
	var chargeValue, chargeUnit sql.NullString
	err := row.Scan(&item.ID, &item.SiteAccountConnectionID, &item.SiteID, &item.AdapterKind, &item.DedupKey,
		&remoteID, &requestID, &upstreamRequestID, &item.OccurredAt, &model, &upstreamModel, &status,
		&httpStatus, &input, &output, &cacheRead, &cacheWrite, &reasoning, &total, &chargeValue, &chargeUnit,
		&duration, &apiKeyName, &item.SourceFetchedAt, &item.CreatedAt)
	if err != nil {
		return SiteUsageRecord{}, err
	}
	item.RemoteID, item.RequestID, item.UpstreamRequestID = remoteID.String, requestID.String, upstreamRequestID.String
	item.Model, item.UpstreamModel, item.Status, item.APIKeyName = model.String, upstreamModel.String, status.String, apiKeyName.String
	item.HTTPStatus = nullableInt(httpStatus)
	item.InputTokens = nullableInt64(input)
	item.OutputTokens = nullableInt64(output)
	item.CacheReadTokens = nullableInt64(cacheRead)
	item.CacheWriteTokens = nullableInt64(cacheWrite)
	item.ReasoningTokens = nullableInt64(reasoning)
	item.TotalTokens = nullableInt64(total)
	item.DurationMS = nullableInt64(duration)
	if chargeValue.Valid {
		value := chargeValue.String
		item.ChargeValue = &value
	}
	if chargeUnit.Valid {
		value := chargeUnit.String
		item.ChargeUnit = &value
	}
	return item, nil
}

func validateSiteUsageWrite(record SiteUsageRecordWrite, fetchedAt int64) error {
	if strings.TrimSpace(record.DedupKey) == "" || record.OccurredAt <= 0 || fetchedAt <= 0 {
		return errors.New("dedup key, occurrence time, and fetch time are required")
	}
	if (record.ChargeValue == nil) != (record.ChargeUnit == nil) {
		return errors.New("charge value and unit must be provided together")
	}
	for name, value := range map[string]*int64{
		"input tokens": record.InputTokens, "output tokens": record.OutputTokens,
		"cache read tokens": record.CacheReadTokens, "cache write tokens": record.CacheWriteTokens,
		"reasoning tokens": record.ReasoningTokens, "total tokens": record.TotalTokens,
		"duration": record.DurationMS,
	} {
		if value != nil && *value < 0 {
			return fmt.Errorf("%s cannot be negative", name)
		}
	}
	return nil
}

func nullableStringPointer(value *string) any {
	if value == nil {
		return nil
	}
	return nullableString(strings.TrimSpace(*value))
}

func nullableInt(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	converted := int(value.Int64)
	return &converted
}

func nullableInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	converted := value.Int64
	return &converted
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}

func requireSiteAccountRevisionChange(ctx context.Context, db *sql.DB, result sql.Result, siteID int64) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 1 {
		return nil
	}
	var exists int
	err = db.QueryRowContext(ctx, `SELECT 1 FROM site_account_connections WHERE site_id=?`, siteID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return sql.ErrNoRows
	}
	if err != nil {
		return err
	}
	return ErrRevisionConflict
}
