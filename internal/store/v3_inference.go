package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/LuTianTian001/JieShan/internal/inferenceprotocol"
	"github.com/LuTianTian001/JieShan/internal/redact"
)

func (s *Store) CreateInferenceEndpoint(ctx context.Context, siteID, expectedSiteRevision int64, input InferenceEndpointWrite) (int64, error) {
	input, headers, err := normalizeEndpointWrite(input)
	if err != nil {
		return 0, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := NowMS()
	result, err := tx.ExecContext(ctx, `INSERT INTO inference_endpoints(
site_id,name,base_url,wire_protocol,compatibility_profile,auth_scheme,custom_headers_json,position,enabled,revision,created_at,updated_at)
SELECT ?,?,?,?,?,?,?,COALESCE(MAX(position)+1,0),?,1,?,? FROM inference_endpoints WHERE site_id=?`,
		siteID, input.Name, input.BaseURL, input.WireProtocol, input.CompatibilityProfile, input.AuthScheme, headers,
		boolInt(input.Enabled), now, now, siteID)
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := bumpSiteRevisionTx(ctx, tx, siteID, expectedSiteRevision, now); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) GetInferenceEndpoint(ctx context.Context, id int64) (InferenceEndpoint, error) {
	return scanInferenceEndpoint(s.DB.QueryRowContext(ctx, endpointSelect+` WHERE id=?`, id))
}

func (s *Store) ListInferenceEndpoints(ctx context.Context, siteID int64) ([]InferenceEndpoint, error) {
	rows, err := s.DB.QueryContext(ctx, endpointSelect+` WHERE site_id=? ORDER BY position,id`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]InferenceEndpoint, 0)
	for rows.Next() {
		item, err := scanInferenceEndpoint(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) UpdateInferenceEndpoint(ctx context.Context, id, expectedRevision int64, input InferenceEndpointWrite) error {
	input, headers, err := normalizeEndpointWrite(input)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var siteID int64
	var currentProtocol string
	if err := tx.QueryRowContext(ctx, "SELECT site_id,wire_protocol FROM inference_endpoints WHERE id=?", id).Scan(&siteID, &currentProtocol); err != nil {
		return err
	}
	if inferenceprotocol.For(currentProtocol).RouteEligible && !inferenceprotocol.For(input.WireProtocol).RouteEligible {
		var routeCount int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM route_site_targets WHERE endpoint_id=?", id).Scan(&routeCount); err != nil {
			return err
		}
		if routeCount > 0 {
			return fmt.Errorf("endpoint protocol cannot change to %s while %d route target(s) use it; remove those route targets first", input.WireProtocol, routeCount)
		}
	}
	now := NowMS()
	if err := bumpPublishedModelsForEndpointTx(ctx, tx, id, now); err != nil {
		return err
	}
	query := `UPDATE inference_endpoints SET name=?,base_url=?,wire_protocol=?,compatibility_profile=?,auth_scheme=?,
custom_headers_json=?,enabled=?,revision=revision+1,updated_at=? WHERE id=?`
	args := []any{input.Name, input.BaseURL, input.WireProtocol, input.CompatibilityProfile, input.AuthScheme, headers, boolInt(input.Enabled), now, id}
	if expectedRevision > 0 {
		query += " AND revision=?"
		args = append(args, expectedRevision)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if err := revisionResult(ctx, tx, result, "inference_endpoints", id); err != nil {
		return err
	}
	if err := bumpSiteRevisionTx(ctx, tx, siteID, 0, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteInferenceEndpoint(ctx context.Context, id, expectedRevision int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var siteID int64
	if err := tx.QueryRowContext(ctx, "SELECT site_id FROM inference_endpoints WHERE id=?", id).Scan(&siteID); err != nil {
		return err
	}
	if err := bumpPublishedModelsForEndpointTx(ctx, tx, id, NowMS()); err != nil {
		return err
	}
	query := "DELETE FROM inference_endpoints WHERE id=?"
	args := []any{id}
	if expectedRevision > 0 {
		query += " AND revision=?"
		args = append(args, expectedRevision)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if err := revisionResult(ctx, tx, result, "inference_endpoints", id); err != nil {
		return err
	}
	if err := bumpSiteRevisionTx(ctx, tx, siteID, 0, NowMS()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ReorderInferenceEndpoints(ctx context.Context, siteID, expectedSiteRevision int64, ids []int64) error {
	return s.reorderSiteChildren(ctx, siteID, expectedSiteRevision, ids, "inference_endpoints")
}

func (s *Store) CreateInferenceCredential(ctx context.Context, siteID, expectedSiteRevision int64, input InferenceCredentialWrite) (int64, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.SecretCipher) == 0 {
		return 0, errors.New("credential name and encrypted secret are required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := NowMS()
	result, err := tx.ExecContext(ctx, `INSERT INTO inference_credentials(
site_id,name,secret_cipher,position,enabled,runtime_state,revision,created_at,updated_at)
SELECT ?,?,?,COALESCE(MAX(position)+1,0),?,'active',1,?,? FROM inference_credentials WHERE site_id=?`,
		siteID, input.Name, input.SecretCipher, boolInt(input.Enabled), now, now, siteID)
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := bumpSiteRevisionTx(ctx, tx, siteID, expectedSiteRevision, now); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) GetInferenceCredential(ctx context.Context, id int64) (InferenceCredential, error) {
	return scanInferenceCredential(s.DB.QueryRowContext(ctx, credentialSelect+` WHERE id=?`, id))
}

func (s *Store) GetInferenceCredentialSecret(ctx context.Context, id int64) (InferenceCredentialSecret, error) {
	var item InferenceCredentialSecret
	var configured, enabled int
	var cooldown, lastTest sql.NullInt64
	var lastStatus, lastError sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT id,site_id,name,CASE WHEN length(secret_cipher)>0 THEN 1 ELSE 0 END,
position,enabled,runtime_state,cooldown_until,last_test_at,last_test_status,last_error_message,revision,created_at,updated_at,secret_cipher
FROM inference_credentials WHERE id=?`, id).Scan(
		&item.ID, &item.SiteID, &item.Name, &configured, &item.Position, &enabled, &item.RuntimeState,
		&cooldown, &lastTest, &lastStatus, &lastError, &item.Revision, &item.CreatedAt, &item.UpdatedAt, &item.SecretCipher)
	if err != nil {
		return InferenceCredentialSecret{}, err
	}
	applyCredentialNulls(&item.InferenceCredential, configured, enabled, cooldown, lastTest, lastStatus, lastError)
	return item, nil
}

func (s *Store) ListInferenceCredentials(ctx context.Context, siteID int64) ([]InferenceCredential, error) {
	return s.listInferenceCredentials(ctx, credentialSelect+` WHERE site_id=? ORDER BY position,id`, siteID)
}

// ListRoutableInferenceCredentials returns a deterministic site-local Key pool.
// Rate-limited keys become candidates again after their recorded cooldown.
func (s *Store) ListRoutableInferenceCredentials(ctx context.Context, siteID, nowMS int64) ([]InferenceCredential, error) {
	return s.listInferenceCredentials(ctx, credentialSelect+` WHERE site_id=? AND enabled=1 AND (
runtime_state='active' OR (runtime_state='rate_limited' AND cooldown_until IS NOT NULL AND cooldown_until<=?))
ORDER BY position,id`, siteID, nowMS)
}

func (s *Store) listInferenceCredentials(ctx context.Context, query string, args ...any) ([]InferenceCredential, error) {
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]InferenceCredential, 0)
	for rows.Next() {
		item, err := scanInferenceCredential(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) UpdateInferenceCredential(ctx context.Context, id, expectedRevision int64, input InferenceCredentialUpdate) error {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return errors.New("credential name is required")
	}
	if input.ReplaceSecret && len(input.SecretCipher) == 0 {
		return errors.New("replacement encrypted secret is required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var siteID int64
	if err := tx.QueryRowContext(ctx, "SELECT site_id FROM inference_credentials WHERE id=?", id).Scan(&siteID); err != nil {
		return err
	}
	now := NowMS()
	query := "UPDATE inference_credentials SET name=?,enabled=?,revision=revision+1,updated_at=?"
	args := []any{input.Name, boolInt(input.Enabled), now}
	if input.ReplaceSecret {
		query += ",secret_cipher=?,runtime_state='active',cooldown_until=NULL,last_error_message=NULL"
		args = append(args, input.SecretCipher)
	}
	query += " WHERE id=?"
	args = append(args, id)
	if expectedRevision > 0 {
		query += " AND revision=?"
		args = append(args, expectedRevision)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if err := revisionResult(ctx, tx, result, "inference_credentials", id); err != nil {
		return err
	}
	if err := bumpSiteRevisionTx(ctx, tx, siteID, 0, now); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateInferenceCredentialRuntime changes operational state without touching
// the configuration revision or the parent site's revision.
func (s *Store) UpdateInferenceCredentialRuntime(ctx context.Context, id int64, input InferenceCredentialRuntimeUpdate) error {
	input.RuntimeState = strings.ToLower(strings.TrimSpace(input.RuntimeState))
	if !validCredentialRuntimeState(input.RuntimeState) {
		return fmt.Errorf("invalid credential runtime state %q", input.RuntimeState)
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE inference_credentials SET runtime_state=?,cooldown_until=?,last_test_at=?,
last_test_status=?,last_error_message=?,updated_at=? WHERE id=?`, input.RuntimeState, input.CooldownUntil, input.LastTestAt,
		nullableString(strings.TrimSpace(input.LastTestStatus)), nullableString(redact.String(input.LastErrorMessage)), NowMS(), id)
	if err != nil {
		return err
	}
	return revisionResult(ctx, s.DB, result, "inference_credentials", id)
}

func (s *Store) DeleteInferenceCredential(ctx context.Context, id, expectedRevision int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var siteID int64
	if err := tx.QueryRowContext(ctx, "SELECT site_id FROM inference_credentials WHERE id=?", id).Scan(&siteID); err != nil {
		return err
	}
	query := "DELETE FROM inference_credentials WHERE id=?"
	args := []any{id}
	if expectedRevision > 0 {
		query += " AND revision=?"
		args = append(args, expectedRevision)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if err := revisionResult(ctx, tx, result, "inference_credentials", id); err != nil {
		return err
	}
	if err := bumpSiteRevisionTx(ctx, tx, siteID, 0, NowMS()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ReorderInferenceCredentials(ctx context.Context, siteID, expectedSiteRevision int64, ids []int64) error {
	return s.reorderSiteChildren(ctx, siteID, expectedSiteRevision, ids, "inference_credentials")
}

func (s *Store) reorderSiteChildren(ctx context.Context, siteID, expectedSiteRevision int64, ids []int64, table string) error {
	if table != "inference_endpoints" && table != "inference_credentials" {
		return errors.New("unsupported site child order")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := NowMS()
	if err := bumpSiteRevisionTx(ctx, tx, siteID, expectedSiteRevision, now); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE site_id=?", table), siteID).Scan(&count); err != nil {
		return err
	}
	if count != len(ids) {
		return fmt.Errorf("ids must contain every %s row exactly once", table)
	}
	seen := make(map[int64]struct{}, len(ids))
	for position, id := range ids {
		if _, exists := seen[id]; exists {
			return fmt.Errorf("duplicate id %d", id)
		}
		seen[id] = struct{}{}
		result, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET position=?,revision=revision+1,updated_at=? WHERE id=? AND site_id=?`, table), position, now, id, siteID)
		if err != nil {
			return err
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return fmt.Errorf("id %d does not belong to site", id)
		}
	}
	return tx.Commit()
}

func normalizeEndpointWrite(input InferenceEndpointWrite) (InferenceEndpointWrite, string, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.BaseURL = strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	protocol, err := inferenceprotocol.Normalize(input.WireProtocol)
	if err != nil {
		return InferenceEndpointWrite{}, "", err
	}
	input.WireProtocol = protocol
	input.CompatibilityProfile = strings.ToLower(strings.TrimSpace(input.CompatibilityProfile))
	input.AuthScheme = strings.ToLower(strings.TrimSpace(input.AuthScheme))
	if input.CompatibilityProfile == "" {
		input.CompatibilityProfile = "generic"
	}
	if input.AuthScheme == "" {
		input.AuthScheme = inferenceprotocol.DefaultAuthScheme(input.WireProtocol)
	}
	if input.Name == "" || input.BaseURL == "" || input.WireProtocol == "" {
		return InferenceEndpointWrite{}, "", errors.New("endpoint name, base URL, and wire protocol are required")
	}
	headers, err := normalizedV3JSON(input.CustomHeaders)
	if err != nil {
		return InferenceEndpointWrite{}, "", err
	}
	return input, headers, nil
}

const endpointSelect = `SELECT id,site_id,name,base_url,wire_protocol,compatibility_profile,auth_scheme,
custom_headers_json,position,enabled,revision,created_at,updated_at FROM inference_endpoints`

func scanInferenceEndpoint(row scanner) (InferenceEndpoint, error) {
	var item InferenceEndpoint
	var enabled int
	var headers []byte
	err := row.Scan(&item.ID, &item.SiteID, &item.Name, &item.BaseURL, &item.WireProtocol,
		&item.CompatibilityProfile, &item.AuthScheme, &headers, &item.Position, &enabled,
		&item.Revision, &item.CreatedAt, &item.UpdatedAt)
	item.CustomHeaders = headers
	item.Capabilities = inferenceprotocol.For(item.WireProtocol)
	item.Enabled = enabled == 1
	return item, err
}

const credentialSelect = `SELECT id,site_id,name,CASE WHEN length(secret_cipher)>0 THEN 1 ELSE 0 END,
position,enabled,runtime_state,cooldown_until,last_test_at,last_test_status,last_error_message,revision,created_at,updated_at FROM inference_credentials`

func scanInferenceCredential(row scanner) (InferenceCredential, error) {
	var item InferenceCredential
	var configured, enabled int
	var cooldown, lastTest sql.NullInt64
	var lastStatus, lastError sql.NullString
	err := row.Scan(&item.ID, &item.SiteID, &item.Name, &configured, &item.Position, &enabled, &item.RuntimeState,
		&cooldown, &lastTest, &lastStatus, &lastError, &item.Revision, &item.CreatedAt, &item.UpdatedAt)
	applyCredentialNulls(&item, configured, enabled, cooldown, lastTest, lastStatus, lastError)
	return item, err
}

func applyCredentialNulls(item *InferenceCredential, configured, enabled int, cooldown, lastTest sql.NullInt64, lastStatus, lastError sql.NullString) {
	item.SecretConfigured = configured == 1
	item.Enabled = enabled == 1
	item.CooldownUntil = int64Ptr(cooldown)
	item.LastTestAt = int64Ptr(lastTest)
	item.LastTestStatus = lastStatus.String
	item.LastErrorMessage = lastError.String
}

func validCredentialRuntimeState(value string) bool {
	switch value {
	case "active", "invalid", "exhausted", "rate_limited":
		return true
	default:
		return false
	}
}
