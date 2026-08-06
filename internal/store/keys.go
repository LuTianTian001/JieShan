package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrDownstreamKeyHasReservations = errors.New("downstream key has active quota reservations")

type DownstreamKeyWrite struct {
	Name             string
	Enabled          bool
	QuotaMicroUSD    *int64
	RPMLimit         int
	AllowedModels    []string
	RoutingProfileID *int64
	ExpiresAt        *int64
}

func APIKeyDigest(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

func (s *Store) CreateDownstreamKey(ctx context.Context, input DownstreamKeyWrite, prefix, raw string) (int64, error) {
	if input.QuotaMicroUSD != nil && *input.QuotaMicroUSD < 0 {
		return 0, fmt.Errorf("quota cannot be negative")
	}
	if err := s.validateDownstreamRoutingProfile(ctx, input.RoutingProfileID); err != nil {
		return 0, err
	}
	allowed, _ := json.Marshal(input.AllowedModels)
	now := NowMS()
	result, err := s.DB.ExecContext(ctx, `INSERT INTO downstream_keys(name,key_prefix,key_hash,enabled,quota_micro_usd,rpm_limit,allowed_models_json,routing_profile_id,expires_at,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?)`, input.Name, prefix, APIKeyDigest(raw), boolInt(input.Enabled), input.QuotaMicroUSD, clamp(input.RPMLimit, 0, 100000), string(allowed), input.RoutingProfileID, input.ExpiresAt, now, now)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) ListDownstreamKeys(ctx context.Context) ([]DownstreamKey, error) {
	rows, err := s.DB.QueryContext(ctx, keySelect+` ORDER BY k.created_at DESC,k.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]DownstreamKey, 0)
	for rows.Next() {
		item, err := scanDownstreamKey(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetDownstreamKey(ctx context.Context, id int64) (DownstreamKey, error) {
	return scanDownstreamKey(s.DB.QueryRowContext(ctx, keySelect+` WHERE k.id=?`, id))
}

func (s *Store) UpdateDownstreamKey(ctx context.Context, id int64, input DownstreamKeyWrite) error {
	if input.QuotaMicroUSD != nil && *input.QuotaMicroUSD < 0 {
		return fmt.Errorf("quota cannot be negative")
	}
	if err := s.validateDownstreamRoutingProfile(ctx, input.RoutingProfileID); err != nil {
		return err
	}
	allowed, _ := json.Marshal(input.AllowedModels)
	result, err := s.DB.ExecContext(ctx, `UPDATE downstream_keys SET name=?,enabled=?,quota_micro_usd=?,rpm_limit=?,allowed_models_json=?,routing_profile_id=?,expires_at=?,updated_at=?
WHERE id=? AND (? IS NULL OR (? >= used_micro_usd AND ?-used_micro_usd >= reserved_micro_usd))`,
		input.Name, boolInt(input.Enabled), input.QuotaMicroUSD, clamp(input.RPMLimit, 0, 100000), string(allowed), input.RoutingProfileID, input.ExpiresAt, NowMS(), id,
		input.QuotaMicroUSD, input.QuotaMicroUSD, input.QuotaMicroUSD)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteDownstreamKey(ctx context.Context, id int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, "DELETE FROM downstream_keys WHERE id=? AND reserved_micro_usd=0", id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 1 {
		return tx.Commit()
	}

	var reservedMicroUSD int64
	if err := tx.QueryRowContext(ctx, "SELECT reserved_micro_usd FROM downstream_keys WHERE id=?", id).Scan(&reservedMicroUSD); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sql.ErrNoRows
		}
		return err
	}
	if reservedMicroUSD > 0 {
		return fmt.Errorf("%w: %d micro-USD still reserved", ErrDownstreamKeyHasReservations, reservedMicroUSD)
	}
	if reservedMicroUSD < 0 {
		return fmt.Errorf("%w: downstream key has a negative reservation", ErrInvalidQuotaState)
	}
	return sql.ErrNoRows
}

func (s *Store) ResetDownstreamKeyUsage(ctx context.Context, id int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE downstream_keys SET used_micro_usd=0,updated_at=? WHERE id=? AND reserved_micro_usd=0", NowMS(), id)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM quota_ledger WHERE downstream_key_id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AuthenticateDownstreamKey(ctx context.Context, raw, model string) (DownstreamKey, error) {
	item, err := scanDownstreamKey(s.DB.QueryRowContext(ctx, keySelect+` WHERE k.key_hash=?`, APIKeyDigest(raw)))
	if err != nil {
		return DownstreamKey{}, err
	}
	if !item.Enabled {
		return DownstreamKey{}, fmt.Errorf("API key is disabled")
	}
	now := time.Now().UnixMilli()
	if item.ExpiresAt != nil && *item.ExpiresAt <= now {
		return DownstreamKey{}, fmt.Errorf("API key has expired")
	}
	if item.QuotaMicroUSD != nil {
		quota := *item.QuotaMicroUSD
		if quota < 0 || item.UsedMicroUSD < 0 || item.ReservedMicroUSD < 0 || item.UsedMicroUSD >= quota ||
			item.UsedMicroUSD > quota || item.ReservedMicroUSD >= quota-item.UsedMicroUSD {
			return DownstreamKey{}, fmt.Errorf("API key quota exhausted")
		}
	}
	if model != "" && len(item.AllowedModels) > 0 && !contains(item.AllowedModels, model) {
		return DownstreamKey{}, fmt.Errorf("model is not allowed for this API key")
	}
	if item.RPMLimit > 0 {
		if err := s.consumeRPM(ctx, item.ID, item.RPMLimit, now); err != nil {
			return DownstreamKey{}, err
		}
	}
	_, _ = s.DB.ExecContext(ctx, "UPDATE downstream_keys SET last_used_at=?,updated_at=? WHERE id=?", now, now, item.ID)
	return item, nil
}

func KeyAllowsModel(key DownstreamKey, model string) bool {
	return len(key.AllowedModels) == 0 || contains(key.AllowedModels, model)
}

const keySelect = `SELECT k.id,k.name,k.key_prefix,k.enabled,k.quota_micro_usd,k.used_micro_usd,k.reserved_micro_usd,
k.rpm_limit,k.allowed_models_json,k.routing_profile_id,COALESCE(p.name,'Default route'),k.expires_at,k.last_used_at,k.created_at,k.updated_at
FROM downstream_keys k LEFT JOIN routing_profiles p ON p.id=k.routing_profile_id`

func scanDownstreamKey(row scanner) (DownstreamKey, error) {
	var item DownstreamKey
	var enabled int
	var quota, routingProfileID, expires, lastUsed sql.NullInt64
	var allowed []byte
	err := row.Scan(&item.ID, &item.Name, &item.KeyPrefix, &enabled, &quota, &item.UsedMicroUSD,
		&item.ReservedMicroUSD, &item.RPMLimit, &allowed, &routingProfileID, &item.RoutingProfileName,
		&expires, &lastUsed, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return DownstreamKey{}, err
	}
	item.Enabled = enabled == 1
	_ = json.Unmarshal(allowed, &item.AllowedModels)
	if item.AllowedModels == nil {
		item.AllowedModels = []string{}
	}
	if quota.Valid {
		value := quota.Int64
		item.QuotaMicroUSD = &value
	}
	if routingProfileID.Valid {
		value := routingProfileID.Int64
		item.RoutingProfileID = &value
	}
	if expires.Valid {
		value := expires.Int64
		item.ExpiresAt = &value
	}
	if lastUsed.Valid {
		value := lastUsed.Int64
		item.LastUsedAt = &value
	}
	return item, nil
}

func (s *Store) validateDownstreamRoutingProfile(ctx context.Context, profileID *int64) error {
	if profileID == nil {
		return nil
	}
	if *profileID <= 0 {
		return errors.New("routing profile ID must be positive")
	}
	var exists int
	if err := s.DB.QueryRowContext(ctx, `SELECT 1 FROM routing_profiles WHERE id=?`, *profileID).Scan(&exists); err != nil {
		return err
	}
	return nil
}

func (s *Store) consumeRPM(ctx context.Context, keyID int64, limit int, nowMS int64) error {
	window := nowMS - (nowMS % 60_000)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO downstream_key_rate_windows(downstream_key_id,window_start,request_count)
VALUES (?,?,1)
ON CONFLICT(downstream_key_id,window_start) DO UPDATE SET request_count=request_count+1
WHERE request_count < ?`, keyID, window, limit)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		return fmt.Errorf("API key RPM limit exceeded")
	}
	_, _ = tx.ExecContext(ctx, "DELETE FROM downstream_key_rate_windows WHERE window_start<?", window-120_000)
	return tx.Commit()
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
