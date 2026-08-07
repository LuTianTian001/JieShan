package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"strings"
)

var (
	ErrDefaultRoutingProfile       = errors.New("default routing profile cannot be changed this way")
	ErrRoutingProfileInUse         = errors.New("routing profile is assigned to downstream keys")
	ErrRoutingProfileRouteNotFound = errors.New("routing profile route override not found")
	ErrPublishedTargetInUse        = errors.New("published model target is used by a routing profile")
)

func DigestDownstreamKey(raw string) []byte {
	digest := sha256.Sum256([]byte(raw))
	return digest[:]
}

// ImportDigestOnlyDownstreamKey exists only for one-shot migration of legacy
// keys whose plaintext no longer exists.
func (s *Store) ImportDigestOnlyDownstreamKey(ctx context.Context, input DownstreamKeyWrite) (int64, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.KeyPrefix = strings.TrimSpace(input.KeyPrefix)
	if err := validateDownstreamKeyWrite(input); err != nil {
		return 0, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	profileID, err := storedRoutingProfileIDTx(ctx, tx, input.RoutingProfileID)
	if err != nil {
		return 0, err
	}
	now := NowMS()
	billingMultiplierBPS := DefaultBillingMultiplierBPS
	if input.BillingMultiplierBPS != nil {
		billingMultiplierBPS = *input.BillingMultiplierBPS
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO downstream_keys(
name,key_prefix,key_digest,encrypted_secret,reveal_version,routing_profile_id,enabled,quota_nano_usd,
hourly_quota_nano_usd,billing_multiplier_bps,expires_at,revision,created_at,updated_at)
VALUES (?,?,?,NULL,0,?,?,?,?,?,?,1,?,?)`, input.Name, input.KeyPrefix, input.KeyDigest, profileID,
		boolInt(input.Enabled), input.QuotaNanoUSD, input.HourlyQuotaNanoUSD, billingMultiplierBPS,
		input.ExpiresAt, now, now)
	if err != nil {
		return 0, normalizeDownstreamKeyConflict(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) GetDownstreamKey(ctx context.Context, id int64) (DownstreamKey, error) {
	return scanDownstreamKeyMetadata(s.DB.QueryRowContext(ctx, downstreamKeyMetadataSelect+` WHERE k.id=?`, id))
}

func (s *Store) GetDefaultRoutingProfile(ctx context.Context) (RoutingProfile, error) {
	return scanRoutingProfile(s.DB.QueryRowContext(ctx, routingProfileSelect+` WHERE p.is_default=1`))
}

func (s *Store) CreateRoutingProfile(ctx context.Context, name string) (RoutingProfile, error) {
	name, err := normalizeRoutingProfileName(name)
	if err != nil {
		return RoutingProfile{}, err
	}
	now := NowMS()
	result, err := s.DB.ExecContext(ctx, `INSERT INTO routing_profiles(name,is_default,revision,created_at,updated_at)
VALUES (?,0,1,?,?)`, name, now, now)
	if err != nil {
		return RoutingProfile{}, normalizeInventoryConflict(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return RoutingProfile{}, err
	}
	return s.GetRoutingProfile(ctx, id)
}

func (s *Store) GetRoutingProfile(ctx context.Context, id int64) (RoutingProfile, error) {
	if id <= 0 {
		return RoutingProfile{}, sql.ErrNoRows
	}
	return scanRoutingProfile(s.DB.QueryRowContext(ctx, routingProfileSelect+` WHERE p.id=?`, id))
}

func (s *Store) ListRoutingProfiles(ctx context.Context) ([]RoutingProfile, error) {
	rows, err := s.DB.QueryContext(ctx, routingProfileSelect+` ORDER BY p.is_default DESC,p.name COLLATE NOCASE,p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]RoutingProfile, 0)
	for rows.Next() {
		item, err := scanRoutingProfile(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) UpdateRoutingProfile(ctx context.Context, id, expectedRevision int64, name string) (RoutingProfile, error) {
	name, err := normalizeRoutingProfileName(name)
	if err != nil {
		return RoutingProfile{}, err
	}
	if id <= 0 || expectedRevision <= 0 {
		return RoutingProfile{}, errors.New("routing profile ID and expected revision are required")
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE routing_profiles SET name=?,revision=revision+1,updated_at=?
WHERE id=? AND is_default=0 AND revision=?`, name, NowMS(), id, expectedRevision)
	if err != nil {
		return RoutingProfile{}, normalizeInventoryConflict(err)
	}
	if err := requireDirectRevisionChange(ctx, s.DB, result, `SELECT is_default FROM routing_profiles WHERE id=?`, id); err != nil {
		return RoutingProfile{}, err
	}
	return s.GetRoutingProfile(ctx, id)
}

func (s *Store) DeleteRoutingProfile(ctx context.Context, id, expectedRevision int64) error {
	if id <= 0 || expectedRevision <= 0 {
		return errors.New("routing profile ID and expected revision are required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var isDefault int
	var revision int64
	var downstreamKeyCount int
	err = tx.QueryRowContext(ctx, `SELECT p.is_default,p.revision,
(SELECT COUNT(*) FROM downstream_keys k WHERE k.routing_profile_id=p.id)
FROM routing_profiles p WHERE p.id=?`, id).Scan(&isDefault, &revision, &downstreamKeyCount)
	if err != nil {
		return err
	}
	if isDefault == 1 {
		return ErrDefaultRoutingProfile
	}
	if revision != expectedRevision {
		return ErrRevisionConflict
	}
	if downstreamKeyCount > 0 {
		return ErrRoutingProfileInUse
	}

	result, err := tx.ExecContext(ctx, `DELETE FROM routing_profiles WHERE id=? AND is_default=0 AND revision=?`, id, expectedRevision)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrRevisionConflict
	}
	return tx.Commit()
}

func storedRoutingProfileIDTx(ctx context.Context, tx *sql.Tx, requested *int64) (any, error) {
	if requested == nil {
		var defaultID int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM routing_profiles WHERE is_default=1`).Scan(&defaultID); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if *requested <= 0 {
		return nil, errors.New("routing profile ID must be positive")
	}
	var isDefault int
	if err := tx.QueryRowContext(ctx, `SELECT is_default FROM routing_profiles WHERE id=?`, *requested).Scan(&isDefault); err != nil {
		return nil, err
	}
	if isDefault == 1 {
		return nil, nil
	}
	return *requested, nil
}

func normalizeRoutingProfileName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("routing profile name is required")
	}
	if len(name) > 120 {
		return "", errors.New("routing profile name must not exceed 120 bytes")
	}
	if strings.EqualFold(name, "Default") {
		return "", errors.New("Default is reserved for the default routing profile")
	}
	return name, nil
}

const routingProfileSelect = `SELECT p.id,p.name,p.is_default,p.revision,
(SELECT COUNT(*) FROM published_models),
CASE WHEN p.is_default=1 THEN (SELECT COUNT(*) FROM published_models)
     ELSE (SELECT COUNT(*) FROM routing_profile_model_routes r WHERE r.routing_profile_id=p.id) END,
CASE WHEN p.is_default=1 THEN 0
     ELSE (SELECT COUNT(*) FROM published_models)-(SELECT COUNT(*) FROM routing_profile_model_routes r WHERE r.routing_profile_id=p.id) END,
CASE WHEN p.is_default=1 THEN (SELECT COUNT(*) FROM downstream_keys k WHERE k.routing_profile_id IS NULL OR k.routing_profile_id=p.id)
     ELSE (SELECT COUNT(*) FROM downstream_keys k WHERE k.routing_profile_id=p.id) END,
p.created_at,p.updated_at FROM routing_profiles p`

func scanRoutingProfile(row scanner) (RoutingProfile, error) {
	var item RoutingProfile
	var isDefault int
	err := row.Scan(&item.ID, &item.Name, &isDefault, &item.Revision, &item.ModelCount,
		&item.LocalModelCount, &item.InheritedModelCount, &item.DownstreamKeyCount, &item.CreatedAt, &item.UpdatedAt)
	item.Default = isDefault == 1
	return item, err
}

func requireDirectRevisionChange(ctx context.Context, db *sql.DB, result sql.Result, existsQuery string, id int64) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 1 {
		return nil
	}
	var isDefault int
	err = db.QueryRowContext(ctx, existsQuery, id).Scan(&isDefault)
	if errors.Is(err, sql.ErrNoRows) {
		return sql.ErrNoRows
	}
	if err != nil {
		return err
	}
	if isDefault == 1 {
		return ErrDefaultRoutingProfile
	}
	return ErrRevisionConflict
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}
