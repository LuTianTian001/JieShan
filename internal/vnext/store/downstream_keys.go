package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrConflict            = errors.New("store conflict")
	ErrQuotaBelowCommitted = errors.New("quota is below committed usage")
)

// DownstreamKeyRevealSecret is deliberately separate from DownstreamKey so
// normal reads and lists cannot accidentally serialize encrypted key material.
type DownstreamKeyRevealSecret struct {
	EncryptedSecret []byte
	RevealVersion   int64
}

// DownstreamKeyAuthCandidate is restricted to authentication code. Its digest
// is never part of DownstreamKey and is explicitly excluded from JSON so the
// type cannot be reused as a collection response by accident.
type DownstreamKeyAuthCandidate struct {
	Key       DownstreamKey     `json:"-"`
	KeyDigest [sha256.Size]byte `json:"-"`
}

// CreateRevealableDownstreamKey inserts the public key record first, then asks
// the caller to encrypt the raw key against the allocated record ID. Both
// operations commit together, so encryption failures cannot leave digest-only
// keys behind.
func (s *Store) CreateRevealableDownstreamKey(
	ctx context.Context,
	input DownstreamKeyWrite,
	revealVersion int64,
	seal func(recordID int64) ([]byte, error),
) (DownstreamKey, error) {
	if seal == nil {
		return DownstreamKey{}, errors.New("downstream key sealer is required")
	}
	if revealVersion <= 0 {
		return DownstreamKey{}, errors.New("positive reveal version is required")
	}
	if err := validateDownstreamKeyWrite(input); err != nil {
		return DownstreamKey{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return DownstreamKey{}, err
	}
	defer tx.Rollback()
	profileID, err := storedRoutingProfileIDTx(ctx, tx, input.RoutingProfileID)
	if err != nil {
		return DownstreamKey{}, err
	}

	now := NowMS()
	billingMultiplierBPS := DefaultBillingMultiplierBPS
	if input.BillingMultiplierBPS != nil {
		billingMultiplierBPS = *input.BillingMultiplierBPS
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO downstream_keys(
name,key_prefix,key_digest,encrypted_secret,reveal_version,routing_profile_id,enabled,quota_nano_usd,
hourly_quota_nano_usd,billing_multiplier_bps,expires_at,revision,created_at,updated_at)
VALUES (?,?,?,NULL,0,?,?,?,?,?,?,1,?,?)`, strings.TrimSpace(input.Name), strings.TrimSpace(input.KeyPrefix), input.KeyDigest,
		profileID, boolInt(input.Enabled), input.QuotaNanoUSD, input.HourlyQuotaNanoUSD, billingMultiplierBPS,
		input.ExpiresAt, now, now)
	if err != nil {
		return DownstreamKey{}, normalizeDownstreamKeyConflict(err)
	}
	recordID, err := result.LastInsertId()
	if err != nil {
		return DownstreamKey{}, err
	}
	ciphertext, err := seal(recordID)
	if err != nil {
		return DownstreamKey{}, err
	}
	if len(ciphertext) == 0 {
		return DownstreamKey{}, errors.New("record-bound sealer returned an empty secret")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE downstream_keys
SET encrypted_secret=?,reveal_version=?,updated_at=? WHERE id=?`, ciphertext, revealVersion, now, recordID); err != nil {
		return DownstreamKey{}, err
	}
	item, err := scanDownstreamKeyMetadata(tx.QueryRowContext(ctx, downstreamKeyMetadataSelect+` WHERE k.id=?`, recordID))
	if err != nil {
		return DownstreamKey{}, err
	}
	if err := tx.Commit(); err != nil {
		return DownstreamKey{}, err
	}
	return item, nil
}

// RotateDownstreamKeySecret replaces the digest and encrypted reveal copy in
// one statement. Once the transaction commits, the previous digest no longer
// authenticates.
func (s *Store) RotateDownstreamKeySecret(
	ctx context.Context,
	id, expectedRevision int64,
	prefix string,
	digest, encryptedSecret []byte,
	revealVersion int64,
) (DownstreamKey, error) {
	prefix = strings.TrimSpace(prefix)
	if id <= 0 {
		return DownstreamKey{}, errors.New("downstream key ID must be positive")
	}
	if expectedRevision <= 0 {
		return DownstreamKey{}, errors.New("expected downstream key revision is required")
	}
	if prefix == "" {
		return DownstreamKey{}, errors.New("downstream key prefix is required")
	}
	if len(digest) != sha256.Size {
		return DownstreamKey{}, fmt.Errorf("downstream key digest must be %d bytes", sha256.Size)
	}
	if len(encryptedSecret) == 0 || revealVersion <= 0 {
		return DownstreamKey{}, errors.New("encrypted secret and positive reveal version are required")
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return DownstreamKey{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE downstream_keys SET
key_prefix=?,key_digest=?,encrypted_secret=?,reveal_version=?,revision=revision+1,updated_at=?
WHERE id=? AND revision=?`, prefix, digest, encryptedSecret, revealVersion, NowMS(), id, expectedRevision)
	if err != nil {
		return DownstreamKey{}, normalizeDownstreamKeyConflict(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return DownstreamKey{}, err
	}
	if changed != 1 {
		var currentRevision int64
		if err := tx.QueryRowContext(ctx, `SELECT revision FROM downstream_keys WHERE id=?`, id).Scan(&currentRevision); err != nil {
			return DownstreamKey{}, err
		}
		return DownstreamKey{}, ErrRevisionConflict
	}
	item, err := scanDownstreamKeyMetadata(tx.QueryRowContext(ctx, downstreamKeyMetadataSelect+` WHERE k.id=?`, id))
	if err != nil {
		return DownstreamKey{}, err
	}
	if err := tx.Commit(); err != nil {
		return DownstreamKey{}, err
	}
	return item, nil
}

// UpdateDownstreamKey changes only operator-owned key metadata. Digest,
// encrypted secret, usage counters, routes, and models are not part of this
// statement. ExpectedRevision is mandatory and enforced again by the UPDATE.
func (s *Store) UpdateDownstreamKey(ctx context.Context, id int64, input DownstreamKeyUpdate) (DownstreamKey, error) {
	input.Name = strings.TrimSpace(input.Name)
	if id <= 0 {
		return DownstreamKey{}, sql.ErrNoRows
	}
	if input.ExpectedRevision <= 0 {
		return DownstreamKey{}, errors.New("expected downstream key revision is required")
	}
	if input.Name == "" {
		return DownstreamKey{}, errors.New("downstream key name is required")
	}
	if input.QuotaNanoUSD != nil && *input.QuotaNanoUSD < 0 {
		return DownstreamKey{}, errors.New("quota cannot be negative")
	}
	if input.HourlyQuotaNanoUSD != nil && *input.HourlyQuotaNanoUSD < 0 {
		return DownstreamKey{}, errors.New("hourly quota cannot be negative")
	}
	if err := validateBillingMultiplierBPS(input.BillingMultiplierBPS); err != nil {
		return DownstreamKey{}, err
	}
	if input.ExpiresAt != nil && *input.ExpiresAt <= 0 {
		return DownstreamKey{}, errors.New("expiry must be a positive Unix millisecond timestamp")
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return DownstreamKey{}, err
	}
	defer tx.Rollback()
	current, err := scanDownstreamKeyMetadata(tx.QueryRowContext(ctx, downstreamKeyMetadataSelect+` WHERE k.id=?`, id))
	if err != nil {
		return DownstreamKey{}, err
	}
	if current.Revision != input.ExpectedRevision {
		return DownstreamKey{}, ErrRevisionConflict
	}
	if input.QuotaNanoUSD != nil {
		quota := *input.QuotaNanoUSD
		if current.UsedNanoUSD > quota || current.ReservedNanoUSD > quota-current.UsedNanoUSD {
			return DownstreamKey{}, ErrQuotaBelowCommitted
		}
	}
	if input.HourlyQuotaNanoUSD != nil {
		quota := *input.HourlyQuotaNanoUSD
		if current.UsedThisHourNanoUSD > quota || current.ReservedThisHourNanoUSD > quota-current.UsedThisHourNanoUSD {
			return DownstreamKey{}, ErrQuotaBelowCommitted
		}
	}
	profileID, err := storedRoutingProfileIDTx(ctx, tx, input.RoutingProfileID)
	if err != nil {
		return DownstreamKey{}, err
	}
	now := NowMS()
	result, err := tx.ExecContext(ctx, `UPDATE downstream_keys SET
name=?,routing_profile_id=?,enabled=?,quota_nano_usd=?,hourly_quota_nano_usd=?,billing_multiplier_bps=?,
expires_at=?,revision=revision+1,updated_at=?
WHERE id=? AND revision=?`, input.Name, profileID, boolInt(input.Enabled), input.QuotaNanoUSD,
		input.HourlyQuotaNanoUSD, input.BillingMultiplierBPS, input.ExpiresAt, now, id, input.ExpectedRevision)
	if err != nil {
		return DownstreamKey{}, normalizeDownstreamKeyConflict(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return DownstreamKey{}, err
	}
	if changed != 1 {
		return DownstreamKey{}, ErrRevisionConflict
	}
	updated, err := scanDownstreamKeyMetadata(tx.QueryRowContext(ctx, downstreamKeyMetadataSelect+` WHERE k.id=?`, id))
	if err != nil {
		return DownstreamKey{}, err
	}
	if err := tx.Commit(); err != nil {
		return DownstreamKey{}, err
	}
	return updated, nil
}

// GetDownstreamKeyRevealSecret is the only VNext store read that returns the
// encrypted reveal copy. It must never be used by collection/list endpoints.
func (s *Store) GetDownstreamKeyRevealSecret(ctx context.Context, id int64) (DownstreamKeyRevealSecret, error) {
	var item DownstreamKeyRevealSecret
	err := s.DB.QueryRowContext(ctx, `SELECT encrypted_secret,reveal_version FROM downstream_keys WHERE id=?`, id).
		Scan(&item.EncryptedSecret, &item.RevealVersion)
	return item, err
}

// ListDownstreamKeyAuthCandidates narrows authentication work by the public
// display prefix. The caller must still compare every returned digest in
// constant time; SQL is not the cryptographic equality check.
func (s *Store) ListDownstreamKeyAuthCandidates(ctx context.Context, prefix string) ([]DownstreamKeyAuthCandidate, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+downstreamKeyMetadataColumns+`,k.key_digest
`+downstreamKeyMetadataFrom+` WHERE k.key_prefix=? ORDER BY k.id`, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]DownstreamKeyAuthCandidate, 0)
	for rows.Next() {
		item, err := scanDownstreamKeyAuthCandidate(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ListDownstreamKeys intentionally selects no digest and no encrypted secret.
func (s *Store) ListDownstreamKeys(ctx context.Context) ([]DownstreamKey, error) {
	rows, err := s.DB.QueryContext(ctx, downstreamKeyMetadataSelect+` ORDER BY k.name COLLATE NOCASE,k.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]DownstreamKey, 0)
	for rows.Next() {
		item, err := scanDownstreamKeyMetadata(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

const downstreamKeyMetadataColumns = `k.id,k.name,k.key_prefix,
CASE WHEN k.encrypted_secret IS NOT NULL AND k.reveal_version > 0 THEN 1 ELSE 0 END,
k.reveal_version,COALESCE(selected.id,defaults.id),COALESCE(selected.name,defaults.name),
CASE WHEN k.routing_profile_id IS NULL OR selected.is_default=1 THEN 1 ELSE 0 END,
k.enabled,k.quota_nano_usd,k.used_nano_usd,k.reserved_nano_usd,k.hourly_quota_nano_usd,
COALESCE((SELECT h.used_nano_usd FROM downstream_key_hourly_usage h
  WHERE h.downstream_key_id=k.id
    AND h.window_started_at=(CAST(strftime('%s','now') AS INTEGER)/3600)*3600000),0),
COALESCE((SELECT h.reserved_nano_usd FROM downstream_key_hourly_usage h
  WHERE h.downstream_key_id=k.id
    AND h.window_started_at=(CAST(strftime('%s','now') AS INTEGER)/3600)*3600000),0),
(CAST(strftime('%s','now') AS INTEGER)/3600)*3600000,k.billing_multiplier_bps,
k.expires_at,k.last_used_at,k.revision,k.created_at,k.updated_at`

const downstreamKeyMetadataFrom = `FROM downstream_keys k
LEFT JOIN routing_profiles selected ON selected.id=k.routing_profile_id
JOIN routing_profiles defaults ON defaults.is_default=1`

const downstreamKeyMetadataSelect = `SELECT ` + downstreamKeyMetadataColumns + ` ` + downstreamKeyMetadataFrom

func scanDownstreamKeyMetadata(row scanner) (DownstreamKey, error) {
	var item DownstreamKey
	var state downstreamKeyScanState
	if err := row.Scan(downstreamKeyScanDestinations(&item, &state)...); err != nil {
		return DownstreamKey{}, err
	}
	applyDownstreamKeyScanState(&item, state)
	return item, nil
}

func scanDownstreamKeyAuthCandidate(row scanner) (DownstreamKeyAuthCandidate, error) {
	var item DownstreamKeyAuthCandidate
	var state downstreamKeyScanState
	var digest []byte
	destinations := append(downstreamKeyScanDestinations(&item.Key, &state), &digest)
	if err := row.Scan(destinations...); err != nil {
		return DownstreamKeyAuthCandidate{}, err
	}
	if len(digest) != sha256.Size {
		return DownstreamKeyAuthCandidate{}, errors.New("stored downstream key digest has invalid length")
	}
	copy(item.KeyDigest[:], digest)
	applyDownstreamKeyScanState(&item.Key, state)
	return item, nil
}

type downstreamKeyScanState struct {
	revealable  int
	usesDefault int
	enabled     int
	quota       sql.NullInt64
	hourlyQuota sql.NullInt64
	expiresAt   sql.NullInt64
	lastUsedAt  sql.NullInt64
}

func downstreamKeyScanDestinations(item *DownstreamKey, state *downstreamKeyScanState) []any {
	return []any{
		&item.ID, &item.Name, &item.KeyPrefix, &state.revealable, &item.RevealVersion,
		&item.RoutingProfileID, &item.RoutingProfileName, &state.usesDefault, &state.enabled,
		&state.quota, &item.UsedNanoUSD, &item.ReservedNanoUSD, &state.hourlyQuota,
		&item.UsedThisHourNanoUSD, &item.ReservedThisHourNanoUSD, &item.HourlyWindowStartedAt,
		&item.BillingMultiplierBPS, &state.expiresAt,
		&state.lastUsedAt, &item.Revision, &item.CreatedAt, &item.UpdatedAt,
	}
}

func applyDownstreamKeyScanState(item *DownstreamKey, state downstreamKeyScanState) {
	item.Revealable = state.revealable == 1
	item.UsesDefaultRoutingProfile = state.usesDefault == 1
	item.Enabled = state.enabled == 1
	if state.quota.Valid {
		value := state.quota.Int64
		item.QuotaNanoUSD = &value
	}
	if state.hourlyQuota.Valid {
		value := state.hourlyQuota.Int64
		item.HourlyQuotaNanoUSD = &value
	}
	if state.expiresAt.Valid {
		value := state.expiresAt.Int64
		item.ExpiresAt = &value
	}
	if state.lastUsedAt.Valid {
		value := state.lastUsedAt.Int64
		item.LastUsedAt = &value
	}
}

func validateDownstreamKeyWrite(input DownstreamKeyWrite) error {
	if strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.KeyPrefix) == "" {
		return errors.New("downstream key name and prefix are required")
	}
	if len(input.KeyDigest) != sha256.Size {
		return fmt.Errorf("downstream key digest must be %d bytes", sha256.Size)
	}
	if input.QuotaNanoUSD != nil && *input.QuotaNanoUSD < 0 {
		return errors.New("quota cannot be negative")
	}
	if input.HourlyQuotaNanoUSD != nil && *input.HourlyQuotaNanoUSD < 0 {
		return errors.New("hourly quota cannot be negative")
	}
	billingMultiplierBPS := DefaultBillingMultiplierBPS
	if input.BillingMultiplierBPS != nil {
		billingMultiplierBPS = *input.BillingMultiplierBPS
	}
	if err := validateBillingMultiplierBPS(billingMultiplierBPS); err != nil {
		return err
	}
	return nil
}

func normalizeDownstreamKeyConflict(err error) error {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "unique constraint") || strings.Contains(message, "constraint failed: unique") {
		return ErrConflict
	}
	return err
}
