package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
)

var ErrRevisionConflict = errors.New("revision conflict")

type inventoryExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s *Store) CreateSite(ctx context.Context, input SiteWrite) (int64, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.DashboardURL = strings.TrimRight(strings.TrimSpace(input.DashboardURL), "/")
	if input.MaxInFlight == 0 {
		input.MaxInFlight = DefaultSiteMaxInFlight
	}
	if input.Name == "" {
		return 0, errors.New("site name is required")
	}
	if input.MaxInFlight <= 0 {
		return 0, errors.New("site maximum in-flight must be positive")
	}
	now := NowMS()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO sites(name,dashboard_url,enabled,max_in_flight,revision,created_at,updated_at)
VALUES (?,?,?,?,1,?,?)`, input.Name, nullableString(input.DashboardURL), boolInt(input.Enabled), input.MaxInFlight, now, now)
	if err != nil {
		return 0, normalizeInventoryConflict(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := s.EnqueueConfigRevisionTx(ctx, tx, "site_created", time.UnixMilli(now)); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) GetSite(ctx context.Context, id int64) (Site, error) {
	var item Site
	var dashboard sql.NullString
	var enabled int
	err := s.DB.QueryRowContext(ctx, `SELECT id,name,dashboard_url,enabled,max_in_flight,revision,created_at,updated_at FROM sites WHERE id=?`, id).
		Scan(&item.ID, &item.Name, &dashboard, &enabled, &item.MaxInFlight, &item.Revision, &item.CreatedAt, &item.UpdatedAt)
	item.DashboardURL = dashboard.String
	item.Enabled = enabled == 1
	return item, err
}

func (s *Store) CreateSiteEndpoint(ctx context.Context, siteID int64, input SiteEndpointWrite) (int64, error) {
	normalized, err := normalizeSiteEndpointWrite(input)
	if err != nil {
		return 0, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var position int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position)+1,0) FROM site_endpoints WHERE site_id=?`, siteID).Scan(&position); err != nil {
		return 0, err
	}
	now := NowMS()
	result, err := tx.ExecContext(ctx, `INSERT INTO site_endpoints(
site_id,name,base_url,wire_protocol,surface,adapter_kind,auth_scheme,header_template_json,secret_headers_cipher,cipher_version,
position,enabled,revision,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`, siteID, normalized.Name, normalized.BaseURL, normalized.WireProtocol,
		normalized.Surface, normalized.AdapterKind, normalized.AuthScheme, string(normalized.HeaderTemplate), nullableBytes(normalized.SecretHeadersCipher),
		normalized.SecretHeadersCipherVersion, position, boolInt(normalized.Enabled), now, now)
	if err != nil {
		return 0, normalizeInventoryConflict(err)
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

func (s *Store) GetSiteEndpoint(ctx context.Context, id int64) (SiteEndpoint, error) {
	var item SiteEndpoint
	var enabled int
	var headerTemplate, secretHeaders []byte
	err := s.DB.QueryRowContext(ctx, `SELECT id,site_id,name,base_url,wire_protocol,surface,adapter_kind,auth_scheme,
	header_template_json,secret_headers_cipher,cipher_version,position,enabled,revision,created_at,updated_at
FROM site_endpoints WHERE id=?`, id).
		Scan(&item.ID, &item.SiteID, &item.Name, &item.BaseURL, &item.WireProtocol, &item.Surface, &item.AdapterKind, &item.AuthScheme,
			&headerTemplate, &secretHeaders, &item.SecretHeadersCipherVersion, &item.Position, &enabled,
			&item.Revision, &item.CreatedAt, &item.UpdatedAt)
	item.HeaderTemplate = append(json.RawMessage(nil), headerTemplate...)
	item.SecretHeadersConfigured = len(secretHeaders) > 0
	item.Enabled = enabled == 1
	return item, err
}

func (s *Store) CreateSiteCredential(ctx context.Context, siteID int64, input SiteCredentialWrite) (int64, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return 0, errors.New("credential name is required")
	}
	if len(input.SecretCipher) == 0 {
		return 0, errors.New("encrypted credential secret is required")
	}
	if input.CipherVersion <= 0 {
		return 0, errors.New("positive credential cipher version is required")
	}
	now := NowMS()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO site_credentials(
site_id,name,secret_cipher,cipher_version,enabled,revision,created_at,updated_at) VALUES (?,?,?,?,?,1,?,?)`,
		siteID, input.Name, input.SecretCipher, input.CipherVersion, boolInt(input.Enabled), now, now)
	if err != nil {
		return 0, normalizeInventoryConflict(err)
	}
	credentialID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO credential_runtime_state(
credential_id,state,cooling_until,last_http_status,last_error_code,revision,updated_at)
VALUES (?,'active',NULL,NULL,NULL,1,?)`, credentialID, now); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return credentialID, nil
}

func (s *Store) GetSiteCredential(ctx context.Context, id int64) (SiteCredential, error) {
	var item SiteCredential
	var secret []byte
	var enabled int
	err := s.DB.QueryRowContext(ctx, `SELECT id,site_id,name,secret_cipher,cipher_version,enabled,revision,created_at,updated_at
FROM site_credentials WHERE id=?`, id).Scan(&item.ID, &item.SiteID, &item.Name, &secret, &item.CipherVersion,
		&enabled, &item.Revision, &item.CreatedAt, &item.UpdatedAt)
	item.SecretConfigured = len(secret) > 0
	item.Enabled = enabled == 1
	return item, err
}

// ReplaceEndpointCredentialBindings is the only inventory operation that
// defines which credentials an endpoint may use. An empty list explicitly
// leaves the endpoint without credentials; credentials elsewhere on the site
// are never inherited.
func (s *Store) ReplaceEndpointCredentialBindings(
	ctx context.Context,
	siteID, endpointID, expectedEndpointRevision int64,
	credentialIDs []int64,
) error {
	if siteID <= 0 || endpointID <= 0 {
		return errors.New("site and endpoint IDs must be positive")
	}
	if expectedEndpointRevision <= 0 {
		return errors.New("expected endpoint revision is required")
	}
	seen := make(map[int64]struct{}, len(credentialIDs))
	for _, credentialID := range credentialIDs {
		if credentialID <= 0 {
			return errors.New("credential IDs must be positive")
		}
		if _, duplicate := seen[credentialID]; duplicate {
			return fmt.Errorf("credential %d is duplicated", credentialID)
		}
		seen[credentialID] = struct{}{}
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, credentialID := range credentialIDs {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM site_credentials WHERE id=? AND site_id=?`, credentialID, siteID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("credential %d does not belong to site %d", credentialID, siteID)
			}
			return err
		}
	}
	now := NowMS()
	result, err := tx.ExecContext(ctx, `UPDATE site_endpoints SET revision=revision+1,updated_at=?
WHERE id=? AND site_id=? AND revision=?`, now, endpointID, siteID, expectedEndpointRevision)
	if err != nil {
		return err
	}
	if err := requireRevisionChange(ctx, tx, result, `SELECT 1 FROM site_endpoints WHERE id=? AND site_id=?`, endpointID, siteID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM credential_endpoint_bindings WHERE endpoint_id=?`, endpointID); err != nil {
		return err
	}
	for position, credentialID := range credentialIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO credential_endpoint_bindings(
site_id,endpoint_id,credential_id,position,enabled,created_at,updated_at) VALUES (?,?,?,?,1,?,?)`,
			siteID, endpointID, credentialID, position, now, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListEndpointCredentialBindings(ctx context.Context, endpointID int64) ([]CredentialEndpointBinding, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT b.site_id,b.endpoint_id,b.credential_id,c.name,b.position,b.enabled,b.created_at,b.updated_at
FROM credential_endpoint_bindings b
JOIN site_credentials c ON c.site_id=b.site_id AND c.id=b.credential_id
WHERE b.endpoint_id=? ORDER BY b.position,b.credential_id`, endpointID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]CredentialEndpointBinding, 0)
	for rows.Next() {
		var item CredentialEndpointBinding
		var enabled int
		if err := rows.Scan(&item.SiteID, &item.EndpointID, &item.CredentialID, &item.CredentialName,
			&item.Position, &enabled, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.Enabled = enabled == 1
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreateProviderModelTarget(ctx context.Context, input ProviderModelTargetWrite) (int64, error) {
	input.SourceModel = strings.TrimSpace(input.SourceModel)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if input.SiteID <= 0 || input.EndpointID <= 0 {
		return 0, errors.New("site and endpoint IDs must be positive")
	}
	if input.SourceModel == "" {
		return 0, errors.New("source model is required")
	}
	now := NowMS()
	result, err := s.DB.ExecContext(ctx, `INSERT INTO provider_model_targets(
site_id,endpoint_id,source_model,display_name,enabled,revision,last_seen_at,created_at,updated_at)
VALUES (?,?,?,?,?,1,?,?,?)`, input.SiteID, input.EndpointID, input.SourceModel, nullableString(input.DisplayName),
		boolInt(input.Enabled), input.LastSeenAt, now, now)
	if err != nil {
		return 0, normalizeInventoryConflict(err)
	}
	return result.LastInsertId()
}

func (s *Store) GetProviderModelTarget(ctx context.Context, id int64) (ProviderModelTarget, error) {
	var item ProviderModelTarget
	var display sql.NullString
	var lastSeen sql.NullInt64
	var enabled int
	err := s.DB.QueryRowContext(ctx, `SELECT id,site_id,endpoint_id,source_model,display_name,enabled,revision,
last_seen_at,created_at,updated_at FROM provider_model_targets WHERE id=?`, id).
		Scan(&item.ID, &item.SiteID, &item.EndpointID, &item.SourceModel, &display, &enabled, &item.Revision,
			&lastSeen, &item.CreatedAt, &item.UpdatedAt)
	item.DisplayName = display.String
	item.Enabled = enabled == 1
	if lastSeen.Valid {
		value := lastSeen.Int64
		item.LastSeenAt = &value
	}
	return item, err
}

func (s *Store) UpsertCredentialTargetAccess(ctx context.Context, input CredentialTargetAccessWrite) error {
	return upsertCredentialTargetAccess(ctx, s.DB, input, NowMS())
}

func upsertCredentialTargetAccess(ctx context.Context, exec inventoryExecer, input CredentialTargetAccessWrite, updatedAt int64) error {
	input.Availability = strings.ToLower(strings.TrimSpace(input.Availability))
	input.LastErrorCode = strings.TrimSpace(input.LastErrorCode)
	switch input.Availability {
	case "unknown", "supported", "unsupported", "forbidden":
	default:
		return errors.New("availability must be unknown, supported, unsupported, or forbidden")
	}
	if input.SiteID <= 0 || input.EndpointID <= 0 || input.CredentialID <= 0 || input.ProviderModelTargetID <= 0 {
		return errors.New("site, endpoint, credential, and provider target IDs must be positive")
	}
	if updatedAt <= 0 {
		return errors.New("positive access update timestamp is required")
	}
	_, err := exec.ExecContext(ctx, `INSERT INTO credential_target_access(
site_id,endpoint_id,credential_id,provider_model_target_id,availability,last_http_status,last_error_code,last_checked_at,revision,updated_at)
VALUES (?,?,?,?,?,?,?,?,1,?)
ON CONFLICT(credential_id,provider_model_target_id) DO UPDATE SET
	  site_id=excluded.site_id,
	  endpoint_id=excluded.endpoint_id,
  availability=excluded.availability,
  last_http_status=excluded.last_http_status,
  last_error_code=excluded.last_error_code,
  last_checked_at=excluded.last_checked_at,
  revision=credential_target_access.revision+1,
  updated_at=excluded.updated_at`, input.SiteID, input.EndpointID, input.CredentialID, input.ProviderModelTargetID,
		input.Availability, input.LastHTTPStatus, nullableString(input.LastErrorCode), input.LastCheckedAt, updatedAt)
	return err
}

func normalizeSiteEndpointWrite(input SiteEndpointWrite) (SiteEndpointWrite, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.BaseURL = strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	input.WireProtocol = strings.ToLower(strings.TrimSpace(input.WireProtocol))
	input.Surface = strings.ToLower(strings.TrimSpace(input.Surface))
	input.AdapterKind = strings.ToLower(strings.TrimSpace(input.AdapterKind))
	input.AuthScheme = strings.ToLower(strings.TrimSpace(input.AuthScheme))
	if input.Name == "" || input.BaseURL == "" || input.WireProtocol == "" || input.Surface == "" {
		return SiteEndpointWrite{}, errors.New("endpoint name, base URL, wire protocol, and surface are required")
	}
	wireProtocol, err := vnextprotocol.ParseProtocol(input.WireProtocol)
	if err != nil {
		return SiteEndpointWrite{}, err
	}
	surface, err := vnextprotocol.ParseSurface(input.Surface)
	if err != nil {
		return SiteEndpointWrite{}, err
	}
	if err := vnextprotocol.ValidatePair(wireProtocol, surface); err != nil {
		return SiteEndpointWrite{}, err
	}
	input.WireProtocol = string(wireProtocol)
	input.Surface = string(surface)
	parsed, err := url.Parse(input.BaseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return SiteEndpointWrite{}, errors.New("endpoint base URL must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	if input.AdapterKind == "" {
		input.AdapterKind = "generic"
	}
	if input.AuthScheme == "" {
		defaultScheme, err := vnextprotocol.DefaultAuthScheme(wireProtocol)
		if err != nil {
			return SiteEndpointWrite{}, err
		}
		input.AuthScheme = string(defaultScheme)
	}
	authScheme, err := vnextprotocol.ParseAuthScheme(input.AuthScheme)
	if err != nil {
		return SiteEndpointWrite{}, err
	}
	input.AuthScheme = string(authScheme)
	if (len(input.SecretHeadersCipher) == 0 && input.SecretHeadersCipherVersion != 0) ||
		(len(input.SecretHeadersCipher) > 0 && input.SecretHeadersCipherVersion <= 0) {
		return SiteEndpointWrite{}, errors.New("encrypted secret headers and positive cipher version must be provided together")
	}
	headerTemplate, err := normalizeHeaderTemplate(input.HeaderTemplate)
	if err != nil {
		return SiteEndpointWrite{}, fmt.Errorf("header template: %w", err)
	}
	input.HeaderTemplate = headerTemplate
	return input, nil
}

func normalizeHeaderTemplate(value json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(value)) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil || object == nil {
		return nil, errors.New("must be a JSON object")
	}
	for name := range object {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "api-key", "x-goog-api-key":
			return nil, fmt.Errorf("sensitive header %q must be stored in secret_headers_cipher", name)
		}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, value); err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), compact.Bytes()...), nil
}

func requireRevisionChange(ctx context.Context, tx *sql.Tx, result sql.Result, existsQuery string, existsArgs ...any) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 1 {
		return nil
	}
	var exists int
	if err := tx.QueryRowContext(ctx, existsQuery, existsArgs...).Scan(&exists); err != nil {
		return err
	}
	return ErrRevisionConflict
}
