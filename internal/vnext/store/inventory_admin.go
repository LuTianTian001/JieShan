package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// SiteUpdate is a complete mutable site snapshot protected by Revision.
type SiteUpdate struct {
	ExpectedRevision int64
	Name             string
	DashboardURL     string
	Enabled          bool
}

// SiteEndpointUpdate intentionally excludes encrypted endpoint headers. Those
// secrets have a separate lifecycle and are preserved by metadata updates.
type SiteEndpointUpdate struct {
	ExpectedRevision int64
	Name             string
	BaseURL          string
	WireProtocol     string
	Surface          string
	AdapterKind      string
	AuthScheme       string
	HeaderTemplate   []byte
	Enabled          bool
}

type SiteCredentialUpdate struct {
	ExpectedRevision int64
	Name             string
	Enabled          bool
}

type ProviderModelTargetUpdate struct {
	ExpectedRevision int64
	SourceModel      string
	DisplayName      string
	Enabled          bool
}

// ProviderModelTargetInventory is the model-centric route picker projection.
// It deliberately contains no credential secret or encrypted configuration.
type ProviderModelTargetInventory struct {
	ProviderModelTarget
	SiteName               string
	SiteEnabled            bool
	EndpointName           string
	EndpointEnabled        bool
	BaseURL                string
	WireProtocol           string
	Surface                string
	AdapterKind            string
	AuthScheme             string
	BoundCredentialCount   int
	UsableCredentialCount  int
	UnknownCredentialCount int
}

func (s *Store) ListSites(ctx context.Context) ([]Site, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,name,dashboard_url,enabled,revision,created_at,updated_at
FROM sites ORDER BY name COLLATE NOCASE,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Site, 0)
	for rows.Next() {
		item, err := scanSite(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) UpdateSite(ctx context.Context, id int64, input SiteUpdate) (Site, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.DashboardURL = strings.TrimRight(strings.TrimSpace(input.DashboardURL), "/")
	if id <= 0 {
		return Site{}, sql.ErrNoRows
	}
	if input.ExpectedRevision <= 0 {
		return Site{}, errors.New("expected site revision is required")
	}
	if input.Name == "" {
		return Site{}, errors.New("site name is required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Site{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE sites SET
name=?,dashboard_url=?,enabled=?,revision=revision+1,updated_at=?
WHERE id=? AND revision=?`, input.Name, nullableString(input.DashboardURL), boolInt(input.Enabled), NowMS(), id, input.ExpectedRevision)
	if err != nil {
		return Site{}, normalizeInventoryConflict(err)
	}
	if err := requireRevisionChange(ctx, tx, result, `SELECT 1 FROM sites WHERE id=?`, id); err != nil {
		return Site{}, err
	}
	item, err := scanSite(tx.QueryRowContext(ctx, `SELECT id,name,dashboard_url,enabled,revision,created_at,updated_at FROM sites WHERE id=?`, id))
	if err != nil {
		return Site{}, err
	}
	if err := tx.Commit(); err != nil {
		return Site{}, err
	}
	return item, nil
}

func (s *Store) ListSiteEndpoints(ctx context.Context, siteID int64) ([]SiteEndpoint, error) {
	if siteID <= 0 {
		return nil, sql.ErrNoRows
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,site_id,name,base_url,wire_protocol,surface,adapter_kind,auth_scheme,
header_template_json,secret_headers_cipher,cipher_version,position,enabled,revision,created_at,updated_at
FROM site_endpoints WHERE site_id=? ORDER BY position,id`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]SiteEndpoint, 0)
	for rows.Next() {
		item, err := scanSiteEndpoint(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		if err := requireSite(ctx, s.DB, siteID); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (s *Store) UpdateSiteEndpoint(ctx context.Context, siteID, endpointID int64, input SiteEndpointUpdate) (SiteEndpoint, error) {
	if siteID <= 0 || endpointID <= 0 {
		return SiteEndpoint{}, sql.ErrNoRows
	}
	if input.ExpectedRevision <= 0 {
		return SiteEndpoint{}, errors.New("expected endpoint revision is required")
	}
	normalized, err := normalizeSiteEndpointWrite(SiteEndpointWrite{
		Name: input.Name, BaseURL: input.BaseURL, WireProtocol: input.WireProtocol, Surface: input.Surface,
		AdapterKind: input.AdapterKind, AuthScheme: input.AuthScheme, HeaderTemplate: append([]byte(nil), input.HeaderTemplate...),
		Enabled: input.Enabled,
	})
	if err != nil {
		return SiteEndpoint{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return SiteEndpoint{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE site_endpoints SET
name=?,base_url=?,wire_protocol=?,surface=?,adapter_kind=?,auth_scheme=?,header_template_json=?,enabled=?,
revision=revision+1,updated_at=? WHERE id=? AND site_id=? AND revision=?`,
		normalized.Name, normalized.BaseURL, normalized.WireProtocol, normalized.Surface, normalized.AdapterKind,
		normalized.AuthScheme, string(normalized.HeaderTemplate), boolInt(normalized.Enabled), NowMS(), endpointID, siteID, input.ExpectedRevision)
	if err != nil {
		return SiteEndpoint{}, normalizeInventoryConflict(err)
	}
	if err := requireRevisionChange(ctx, tx, result, `SELECT 1 FROM site_endpoints WHERE id=? AND site_id=?`, endpointID, siteID); err != nil {
		return SiteEndpoint{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE provider_model_targets SET
revision=revision+1,updated_at=? WHERE endpoint_id=? AND site_id=?`, NowMS(), endpointID, siteID); err != nil {
		return SiteEndpoint{}, err
	}
	item, err := scanSiteEndpoint(tx.QueryRowContext(ctx, `SELECT id,site_id,name,base_url,wire_protocol,surface,adapter_kind,auth_scheme,
header_template_json,secret_headers_cipher,cipher_version,position,enabled,revision,created_at,updated_at
FROM site_endpoints WHERE id=? AND site_id=?`, endpointID, siteID))
	if err != nil {
		return SiteEndpoint{}, err
	}
	if err := tx.Commit(); err != nil {
		return SiteEndpoint{}, err
	}
	return item, nil
}

func (s *Store) ListSiteCredentials(ctx context.Context, siteID int64) ([]SiteCredential, error) {
	if siteID <= 0 {
		return nil, sql.ErrNoRows
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,site_id,name,secret_cipher,cipher_version,enabled,revision,created_at,updated_at
FROM site_credentials WHERE site_id=? ORDER BY name COLLATE NOCASE,id`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]SiteCredential, 0)
	for rows.Next() {
		item, err := scanSiteCredential(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		if err := requireSite(ctx, s.DB, siteID); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (s *Store) UpdateSiteCredential(ctx context.Context, siteID, credentialID int64, input SiteCredentialUpdate) (SiteCredential, error) {
	input.Name = strings.TrimSpace(input.Name)
	if siteID <= 0 || credentialID <= 0 {
		return SiteCredential{}, sql.ErrNoRows
	}
	if input.ExpectedRevision <= 0 {
		return SiteCredential{}, errors.New("expected credential revision is required")
	}
	if input.Name == "" {
		return SiteCredential{}, errors.New("credential name is required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return SiteCredential{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE site_credentials SET
name=?,enabled=?,revision=revision+1,updated_at=? WHERE id=? AND site_id=? AND revision=?`,
		input.Name, boolInt(input.Enabled), NowMS(), credentialID, siteID, input.ExpectedRevision)
	if err != nil {
		return SiteCredential{}, normalizeInventoryConflict(err)
	}
	if err := requireRevisionChange(ctx, tx, result, `SELECT 1 FROM site_credentials WHERE id=? AND site_id=?`, credentialID, siteID); err != nil {
		return SiteCredential{}, err
	}
	item, err := scanSiteCredential(tx.QueryRowContext(ctx, `SELECT id,site_id,name,secret_cipher,cipher_version,enabled,revision,created_at,updated_at
FROM site_credentials WHERE id=? AND site_id=?`, credentialID, siteID))
	if err != nil {
		return SiteCredential{}, err
	}
	if err := tx.Commit(); err != nil {
		return SiteCredential{}, err
	}
	return item, nil
}

// ReplaceSealedSiteCredentialSecret changes only the encrypted API key. The
// plaintext and encryption operation stay outside Store, while the CAS update
// prevents a concurrent metadata edit from being silently overwritten.
func (s *Store) ReplaceSealedSiteCredentialSecret(
	ctx context.Context,
	siteID, credentialID, expectedRevision, cipherVersion int64,
	secretCipher []byte,
) (SiteCredential, error) {
	if siteID <= 0 || credentialID <= 0 {
		return SiteCredential{}, sql.ErrNoRows
	}
	if expectedRevision <= 0 || cipherVersion <= 0 || len(secretCipher) == 0 {
		return SiteCredential{}, errors.New("expected revision, cipher version, and encrypted credential are required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return SiteCredential{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE site_credentials SET
secret_cipher=?,cipher_version=?,revision=revision+1,updated_at=?
WHERE id=? AND site_id=? AND revision=?`, secretCipher, cipherVersion, NowMS(), credentialID, siteID, expectedRevision)
	if err != nil {
		return SiteCredential{}, err
	}
	if err := requireRevisionChange(ctx, tx, result, `SELECT 1 FROM site_credentials WHERE id=? AND site_id=?`, credentialID, siteID); err != nil {
		return SiteCredential{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE credential_runtime_state SET
state='active',cooling_until=NULL,last_http_status=NULL,last_error_code=NULL,revision=revision+1,updated_at=?
WHERE credential_id=?`, NowMS(), credentialID); err != nil {
		return SiteCredential{}, err
	}
	item, err := scanSiteCredential(tx.QueryRowContext(ctx, `SELECT id,site_id,name,secret_cipher,cipher_version,enabled,revision,created_at,updated_at
FROM site_credentials WHERE id=? AND site_id=?`, credentialID, siteID))
	if err != nil {
		return SiteCredential{}, err
	}
	if err := tx.Commit(); err != nil {
		return SiteCredential{}, err
	}
	return item, nil
}

func (s *Store) ListProviderModelTargets(ctx context.Context, siteID, endpointID int64) ([]ProviderModelTarget, error) {
	if siteID <= 0 || endpointID <= 0 {
		return nil, sql.ErrNoRows
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,site_id,endpoint_id,source_model,display_name,enabled,revision,last_seen_at,created_at,updated_at
FROM provider_model_targets WHERE site_id=? AND endpoint_id=? ORDER BY source_model COLLATE NOCASE,id`, siteID, endpointID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ProviderModelTarget, 0)
	for rows.Next() {
		item, err := scanProviderModelTarget(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		if err := requireEndpoint(ctx, s.DB, siteID, endpointID); err != nil {
			return nil, err
		}
	}
	return items, nil
}

// ApplyCredentialModelDiscovery records only a successfully decoded discovery
// result. Complete snapshots can prove that an existing target is unsupported;
// partial snapshots may only confirm targets that were actually returned.
func (s *Store) ApplyCredentialModelDiscovery(
	ctx context.Context,
	siteID, endpointID, credentialID int64,
	models []string,
	complete bool,
	checkedAt int64,
) error {
	if siteID <= 0 || endpointID <= 0 || credentialID <= 0 {
		return sql.ErrNoRows
	}
	if checkedAt <= 0 {
		return errors.New("positive discovery timestamp is required")
	}
	normalized := make([]string, 0)
	if len(models) > 0 {
		var err error
		normalized, err = normalizeModelNames(models)
		if err != nil {
			return err
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1
FROM credential_endpoint_bindings b
JOIN site_credentials c ON c.id=b.credential_id AND c.site_id=b.site_id
WHERE b.site_id=? AND b.endpoint_id=? AND b.credential_id=? AND b.enabled=1 AND c.enabled=1`,
		siteID, endpointID, credentialID).Scan(&exists); err != nil {
		return err
	}
	type targetIdentity struct {
		id    int64
		model string
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,source_model FROM provider_model_targets
WHERE site_id=? AND endpoint_id=? ORDER BY id`, siteID, endpointID)
	if err != nil {
		return err
	}
	targets := make([]targetIdentity, 0)
	for rows.Next() {
		var target targetIdentity
		if err := rows.Scan(&target.id, &target.model); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(normalized))
	for _, model := range normalized {
		seen[model] = struct{}{}
	}
	now := NowMS()
	for _, target := range targets {
		availability := ""
		if _, found := seen[target.model]; found {
			availability = "supported"
		} else if complete {
			availability = "unsupported"
		} else {
			continue
		}
		if err := upsertCredentialTargetAccess(ctx, tx, CredentialTargetAccessWrite{
			SiteID: siteID, EndpointID: endpointID, CredentialID: credentialID,
			ProviderModelTargetID: target.id, Availability: availability, LastCheckedAt: &checkedAt,
		}, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ImportProviderModelTargets idempotently materializes an explicit selection
// from one credential's discovery result. Existing enabled choices remain
// operator-controlled, while model availability is recorded atomically.
func (s *Store) ImportProviderModelTargets(
	ctx context.Context,
	siteID, endpointID, credentialID int64,
	models []string,
	seenAt int64,
) ([]ProviderModelTarget, error) {
	if siteID <= 0 || endpointID <= 0 || credentialID <= 0 {
		return nil, sql.ErrNoRows
	}
	if seenAt <= 0 {
		return nil, errors.New("positive discovery timestamp is required")
	}
	normalized, err := normalizeModelNames(models)
	if err != nil {
		return nil, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := requireEndpoint(ctx, tx, siteID, endpointID); err != nil {
		return nil, err
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1
FROM credential_endpoint_bindings b
JOIN site_credentials c ON c.id=b.credential_id AND c.site_id=b.site_id
WHERE b.site_id=? AND b.endpoint_id=? AND b.credential_id=? AND b.enabled=1 AND c.enabled=1`,
		siteID, endpointID, credentialID).Scan(&exists); err != nil {
		return nil, err
	}
	now := NowMS()
	items := make([]ProviderModelTarget, 0, len(normalized))
	for _, model := range normalized {
		_, err := tx.ExecContext(ctx, `INSERT INTO provider_model_targets(
site_id,endpoint_id,source_model,display_name,enabled,revision,last_seen_at,created_at,updated_at)
VALUES (?,?,?,NULL,1,1,?,?,?)
ON CONFLICT(endpoint_id,source_model) DO UPDATE SET last_seen_at=excluded.last_seen_at,updated_at=excluded.updated_at`,
			siteID, endpointID, model, seenAt, now, now)
		if err != nil {
			return nil, normalizeInventoryConflict(err)
		}
		item, err := scanProviderModelTarget(tx.QueryRowContext(ctx, `SELECT id,site_id,endpoint_id,source_model,display_name,enabled,revision,last_seen_at,created_at,updated_at
FROM provider_model_targets WHERE endpoint_id=? AND source_model=?`, endpointID, model))
		if err != nil {
			return nil, err
		}
		if err := upsertCredentialTargetAccess(ctx, tx, CredentialTargetAccessWrite{
			SiteID: siteID, EndpointID: endpointID, CredentialID: credentialID,
			ProviderModelTargetID: item.ID, Availability: "supported", LastCheckedAt: &seenAt,
		}, now); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) UpdateProviderModelTarget(
	ctx context.Context,
	siteID, endpointID, targetID int64,
	input ProviderModelTargetUpdate,
) (ProviderModelTarget, error) {
	input.SourceModel = strings.TrimSpace(input.SourceModel)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if siteID <= 0 || endpointID <= 0 || targetID <= 0 {
		return ProviderModelTarget{}, sql.ErrNoRows
	}
	if input.ExpectedRevision <= 0 {
		return ProviderModelTarget{}, errors.New("expected provider target revision is required")
	}
	if input.SourceModel == "" {
		return ProviderModelTarget{}, errors.New("source model is required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ProviderModelTarget{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE provider_model_targets SET
source_model=?,display_name=?,enabled=?,revision=revision+1,updated_at=?
WHERE id=? AND site_id=? AND endpoint_id=? AND revision=?`, input.SourceModel, nullableString(input.DisplayName),
		boolInt(input.Enabled), NowMS(), targetID, siteID, endpointID, input.ExpectedRevision)
	if err != nil {
		return ProviderModelTarget{}, normalizeInventoryConflict(err)
	}
	if err := requireRevisionChange(ctx, tx, result, `SELECT 1 FROM provider_model_targets WHERE id=? AND site_id=? AND endpoint_id=?`, targetID, siteID, endpointID); err != nil {
		return ProviderModelTarget{}, err
	}
	item, err := scanProviderModelTarget(tx.QueryRowContext(ctx, `SELECT id,site_id,endpoint_id,source_model,display_name,enabled,revision,last_seen_at,created_at,updated_at
FROM provider_model_targets WHERE id=? AND site_id=? AND endpoint_id=?`, targetID, siteID, endpointID))
	if err != nil {
		return ProviderModelTarget{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProviderModelTarget{}, err
	}
	return item, nil
}

func (s *Store) ListProviderModelTargetInventory(ctx context.Context) ([]ProviderModelTargetInventory, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT
p.id,p.site_id,p.endpoint_id,p.source_model,p.display_name,p.enabled,p.revision,p.last_seen_at,p.created_at,p.updated_at,
s.name,s.enabled,e.name,e.enabled,e.base_url,e.wire_protocol,e.surface,e.adapter_kind,e.auth_scheme,
(SELECT COUNT(*) FROM credential_endpoint_bindings b JOIN site_credentials c ON c.id=b.credential_id AND c.site_id=b.site_id
 WHERE b.endpoint_id=e.id AND b.site_id=e.site_id AND b.enabled=1 AND c.enabled=1),
(SELECT COUNT(*) FROM credential_endpoint_bindings b JOIN site_credentials c ON c.id=b.credential_id AND c.site_id=b.site_id
 LEFT JOIN credential_target_access a ON a.credential_id=b.credential_id AND a.provider_model_target_id=p.id
 WHERE b.endpoint_id=e.id AND b.site_id=e.site_id AND b.enabled=1 AND c.enabled=1
 AND (a.availability IS NULL OR a.availability IN ('unknown','supported'))),
(SELECT COUNT(*) FROM credential_endpoint_bindings b JOIN site_credentials c ON c.id=b.credential_id AND c.site_id=b.site_id
 LEFT JOIN credential_target_access a ON a.credential_id=b.credential_id AND a.provider_model_target_id=p.id
 WHERE b.endpoint_id=e.id AND b.site_id=e.site_id AND b.enabled=1 AND c.enabled=1
 AND (a.availability IS NULL OR a.availability='unknown'))
FROM provider_model_targets p
JOIN sites s ON s.id=p.site_id
JOIN site_endpoints e ON e.id=p.endpoint_id AND e.site_id=p.site_id
ORDER BY p.source_model COLLATE NOCASE,s.name COLLATE NOCASE,e.position,p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ProviderModelTargetInventory, 0)
	for rows.Next() {
		var item ProviderModelTargetInventory
		var display sql.NullString
		var lastSeen sql.NullInt64
		var targetEnabled, siteEnabled, endpointEnabled int
		if err := rows.Scan(
			&item.ID, &item.SiteID, &item.EndpointID, &item.SourceModel, &display, &targetEnabled, &item.Revision,
			&lastSeen, &item.CreatedAt, &item.UpdatedAt, &item.SiteName, &siteEnabled, &item.EndpointName,
			&endpointEnabled, &item.BaseURL, &item.WireProtocol, &item.Surface, &item.AdapterKind, &item.AuthScheme,
			&item.BoundCredentialCount, &item.UsableCredentialCount, &item.UnknownCredentialCount,
		); err != nil {
			return nil, err
		}
		item.DisplayName = display.String
		item.Enabled = targetEnabled == 1
		item.SiteEnabled = siteEnabled == 1
		item.EndpointEnabled = endpointEnabled == 1
		if lastSeen.Valid {
			value := lastSeen.Int64
			item.LastSeenAt = &value
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanSite(row scanner) (Site, error) {
	var item Site
	var dashboard sql.NullString
	var enabled int
	if err := row.Scan(&item.ID, &item.Name, &dashboard, &enabled, &item.Revision, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return Site{}, err
	}
	item.DashboardURL = dashboard.String
	item.Enabled = enabled == 1
	return item, nil
}

func scanSiteEndpoint(row scanner) (SiteEndpoint, error) {
	var item SiteEndpoint
	var enabled int
	var headerTemplate, secretHeaders []byte
	if err := row.Scan(&item.ID, &item.SiteID, &item.Name, &item.BaseURL, &item.WireProtocol, &item.Surface,
		&item.AdapterKind, &item.AuthScheme, &headerTemplate, &secretHeaders, &item.SecretHeadersCipherVersion,
		&item.Position, &enabled, &item.Revision, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return SiteEndpoint{}, err
	}
	item.HeaderTemplate = append([]byte(nil), headerTemplate...)
	item.SecretHeadersConfigured = len(secretHeaders) > 0
	item.Enabled = enabled == 1
	return item, nil
}

func scanSiteCredential(row scanner) (SiteCredential, error) {
	var item SiteCredential
	var secret []byte
	var enabled int
	if err := row.Scan(&item.ID, &item.SiteID, &item.Name, &secret, &item.CipherVersion, &enabled,
		&item.Revision, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return SiteCredential{}, err
	}
	item.SecretConfigured = len(secret) > 0
	item.Enabled = enabled == 1
	return item, nil
}

func scanProviderModelTarget(row scanner) (ProviderModelTarget, error) {
	var item ProviderModelTarget
	var display sql.NullString
	var lastSeen sql.NullInt64
	var enabled int
	if err := row.Scan(&item.ID, &item.SiteID, &item.EndpointID, &item.SourceModel, &display, &enabled,
		&item.Revision, &lastSeen, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return ProviderModelTarget{}, err
	}
	item.DisplayName = display.String
	item.Enabled = enabled == 1
	if lastSeen.Valid {
		value := lastSeen.Int64
		item.LastSeenAt = &value
	}
	return item, nil
}

func normalizeModelNames(models []string) ([]string, error) {
	if len(models) == 0 {
		return nil, errors.New("at least one discovered model is required")
	}
	if len(models) > 5000 {
		return nil, errors.New("discovered model list exceeds the safety limit")
	}
	seen := make(map[string]struct{}, len(models))
	result := make([]string, 0, len(models))
	for _, raw := range models {
		model := strings.TrimSpace(raw)
		if model == "" || len(model) > 512 {
			return nil, errors.New("model names must be non-empty and at most 512 bytes")
		}
		if _, duplicate := seen[model]; duplicate {
			continue
		}
		seen[model] = struct{}{}
		result = append(result, model)
	}
	if len(result) == 0 {
		return nil, errors.New("at least one discovered model is required")
	}
	return result, nil
}

func requireSite(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, siteID int64) error {
	var exists int
	return queryer.QueryRowContext(ctx, `SELECT 1 FROM sites WHERE id=?`, siteID).Scan(&exists)
}

func requireEndpoint(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, siteID, endpointID int64) error {
	var exists int
	return queryer.QueryRowContext(ctx, `SELECT 1 FROM site_endpoints WHERE id=? AND site_id=?`, endpointID, siteID).Scan(&exists)
}

func normalizeInventoryConflict(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "unique constraint") || strings.Contains(message, "constraint failed: unique") {
		return ErrConflict
	}
	return fmt.Errorf("inventory persistence failed: %w", err)
}
