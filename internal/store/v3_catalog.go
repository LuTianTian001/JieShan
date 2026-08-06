package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func (s *Store) CreateSiteModel(ctx context.Context, input SiteModelWrite) (int64, error) {
	input, err := normalizeSiteModelWrite(input)
	if err != nil {
		return 0, err
	}
	now := NowMS()
	result, err := s.DB.ExecContext(ctx, `INSERT INTO site_models(
site_id,endpoint_id,model_name,display_name,enabled,stale,missing_count,last_seen_at,revision,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,1,?,?)`, input.SiteID, input.EndpointID, input.ModelName, nullableString(input.DisplayName),
		boolInt(input.Enabled), boolInt(input.Stale), input.MissingCount, input.LastSeenAt, now, now)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// UpsertSiteModel is the discovery-friendly write path. It returns the durable
// catalog row ID and never creates a public model or a route as a side effect.
func (s *Store) UpsertSiteModel(ctx context.Context, input SiteModelWrite) (int64, error) {
	input, err := normalizeSiteModelWrite(input)
	if err != nil {
		return 0, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := NowMS()
	var existingID int64
	existingErr := tx.QueryRowContext(ctx, "SELECT id FROM site_models WHERE endpoint_id=? AND model_name=?", input.EndpointID, input.ModelName).Scan(&existingID)
	if existingErr != nil && !errors.Is(existingErr, sql.ErrNoRows) {
		return 0, existingErr
	}
	if existingErr == nil {
		if err := bumpPublishedModelsForSiteModelTx(ctx, tx, existingID, now); err != nil {
			return 0, err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO site_models(
site_id,endpoint_id,model_name,display_name,enabled,stale,missing_count,last_seen_at,revision,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,1,?,?)
ON CONFLICT(endpoint_id,model_name) DO UPDATE SET
display_name=excluded.display_name,enabled=excluded.enabled,stale=excluded.stale,missing_count=excluded.missing_count,
last_seen_at=excluded.last_seen_at,revision=site_models.revision+1,updated_at=excluded.updated_at
WHERE site_models.site_id=excluded.site_id`, input.SiteID, input.EndpointID, input.ModelName, nullableString(input.DisplayName),
		boolInt(input.Enabled), boolInt(input.Stale), input.MissingCount, input.LastSeenAt, now, now)
	if err != nil {
		return 0, err
	}
	var id int64
	if err := tx.QueryRowContext(ctx, "SELECT id FROM site_models WHERE site_id=? AND endpoint_id=? AND model_name=?",
		input.SiteID, input.EndpointID, input.ModelName).Scan(&id); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) GetSiteModel(ctx context.Context, id int64) (SiteModel, error) {
	return scanSiteModel(s.DB.QueryRowContext(ctx, siteModelSelect+` WHERE id=?`, id))
}

func (s *Store) ListSiteModels(ctx context.Context, siteID int64) ([]SiteModel, error) {
	return s.listSiteModels(ctx, siteModelSelect+` WHERE site_id=? ORDER BY model_name COLLATE NOCASE,id`, siteID)
}

func (s *Store) ListEndpointModels(ctx context.Context, endpointID int64) ([]SiteModel, error) {
	return s.listSiteModels(ctx, siteModelSelect+` WHERE endpoint_id=? ORDER BY model_name COLLATE NOCASE,id`, endpointID)
}

func (s *Store) listSiteModels(ctx context.Context, query string, args ...any) ([]SiteModel, error) {
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]SiteModel, 0)
	for rows.Next() {
		item, err := scanSiteModel(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ListSiteModelsWithCoverage reports how many enabled credentials have a
// confirmed, denied, or still-unknown view of every model in one bulk query.
func (s *Store) ListSiteModelsWithCoverage(ctx context.Context, siteID int64) ([]SiteModelCoverage, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT m.id,m.site_id,m.endpoint_id,m.model_name,m.display_name,m.enabled,m.stale,m.missing_count,
m.last_seen_at,m.revision,m.created_at,m.updated_at,
COUNT(c.id),
SUM(CASE WHEN c.id IS NOT NULL AND a.availability='supported' THEN 1 ELSE 0 END),
SUM(CASE WHEN c.id IS NOT NULL AND a.availability='unsupported' THEN 1 ELSE 0 END),
SUM(CASE WHEN c.id IS NOT NULL AND COALESCE(a.availability,'unknown')='unknown' THEN 1 ELSE 0 END)
FROM site_models m
LEFT JOIN inference_credentials c ON c.site_id=m.site_id AND c.enabled=1
LEFT JOIN credential_model_access a ON a.credential_id=c.id AND a.site_model_id=m.id
WHERE m.site_id=?
GROUP BY m.id,m.site_id,m.endpoint_id,m.model_name,m.display_name,m.enabled,m.stale,m.missing_count,
m.last_seen_at,m.revision,m.created_at,m.updated_at
ORDER BY m.model_name COLLATE NOCASE,m.id`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]SiteModelCoverage, 0)
	for rows.Next() {
		var item SiteModelCoverage
		var display sql.NullString
		var enabled, stale int
		var lastSeen sql.NullInt64
		if err := rows.Scan(&item.ID, &item.SiteID, &item.EndpointID, &item.ModelName, &display, &enabled, &stale,
			&item.MissingCount, &lastSeen, &item.Revision, &item.CreatedAt, &item.UpdatedAt,
			&item.CredentialCount, &item.SupportedCredentialCount, &item.UnsupportedCredentialCount, &item.UnknownCredentialCount); err != nil {
			return nil, err
		}
		item.DisplayName = display.String
		item.Enabled = enabled == 1
		item.Stale = stale == 1
		item.LastSeenAt = int64Ptr(lastSeen)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) UpdateSiteModel(ctx context.Context, id, expectedRevision int64, input SiteModelWrite) error {
	input, err := normalizeSiteModelWrite(input)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := NowMS()
	if err := bumpPublishedModelsForSiteModelTx(ctx, tx, id, now); err != nil {
		return err
	}
	query := `UPDATE site_models SET site_id=?,endpoint_id=?,model_name=?,display_name=?,enabled=?,stale=?,missing_count=?,
last_seen_at=?,revision=revision+1,updated_at=? WHERE id=?`
	args := []any{input.SiteID, input.EndpointID, input.ModelName, nullableString(input.DisplayName), boolInt(input.Enabled),
		boolInt(input.Stale), input.MissingCount, input.LastSeenAt, now, id}
	if expectedRevision > 0 {
		query += " AND revision=?"
		args = append(args, expectedRevision)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if err := revisionResult(ctx, tx, result, "site_models", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteSiteModel(ctx context.Context, id, expectedRevision int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := bumpPublishedModelsForSiteModelTx(ctx, tx, id, NowMS()); err != nil {
		return err
	}
	query := "DELETE FROM site_models WHERE id=?"
	args := []any{id}
	if expectedRevision > 0 {
		query += " AND revision=?"
		args = append(args, expectedRevision)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if err := revisionResult(ctx, tx, result, "site_models", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpsertCredentialModelAccess(ctx context.Context, input CredentialModelAccessWrite) error {
	input.Availability = strings.ToLower(strings.TrimSpace(input.Availability))
	if !validModelAvailability(input.Availability) {
		return fmt.Errorf("invalid model availability %q", input.Availability)
	}
	if input.SiteID <= 0 || input.CredentialID <= 0 || input.SiteModelID <= 0 || input.MissingCount < 0 {
		return errors.New("site, credential, model, and non-negative missing count are required")
	}
	now := NowMS()
	_, err := s.DB.ExecContext(ctx, `INSERT INTO credential_model_access(
site_id,credential_id,site_model_id,availability,missing_count,last_seen_at,last_checked_at,revision,updated_at)
VALUES (?,?,?,?,?,?,?,1,?)
ON CONFLICT(credential_id,site_model_id) DO UPDATE SET
availability=excluded.availability,missing_count=excluded.missing_count,last_seen_at=excluded.last_seen_at,
last_checked_at=excluded.last_checked_at,revision=credential_model_access.revision+1,updated_at=excluded.updated_at
WHERE credential_model_access.site_id=excluded.site_id`, input.SiteID, input.CredentialID, input.SiteModelID,
		input.Availability, input.MissingCount, input.LastSeenAt, input.LastCheckedAt, now)
	return err
}

func (s *Store) GetCredentialModelAccess(ctx context.Context, credentialID, siteModelID int64) (CredentialModelAccess, error) {
	return scanCredentialModelAccess(s.DB.QueryRowContext(ctx, credentialAccessSelect+` WHERE credential_id=? AND site_model_id=?`, credentialID, siteModelID))
}

func (s *Store) ListCredentialModelAccess(ctx context.Context, credentialID int64) ([]CredentialModelAccess, error) {
	rows, err := s.DB.QueryContext(ctx, credentialAccessSelect+` WHERE credential_id=? ORDER BY site_model_id`, credentialID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]CredentialModelAccess, 0)
	for rows.Next() {
		item, err := scanCredentialModelAccess(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) DeleteCredentialModelAccess(ctx context.Context, credentialID, siteModelID int64) error {
	result, err := s.DB.ExecContext(ctx, "DELETE FROM credential_model_access WHERE credential_id=? AND site_model_id=?", credentialID, siteModelID)
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

func normalizeSiteModelWrite(input SiteModelWrite) (SiteModelWrite, error) {
	input.ModelName = strings.TrimSpace(input.ModelName)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if input.SiteID <= 0 || input.EndpointID <= 0 || input.ModelName == "" {
		return SiteModelWrite{}, errors.New("site, endpoint, and model name are required")
	}
	if input.MissingCount < 0 {
		return SiteModelWrite{}, errors.New("missing count cannot be negative")
	}
	return input, nil
}

const siteModelSelect = `SELECT id,site_id,endpoint_id,model_name,display_name,enabled,stale,missing_count,
last_seen_at,revision,created_at,updated_at FROM site_models`

func scanSiteModel(row scanner) (SiteModel, error) {
	var item SiteModel
	var display sql.NullString
	var enabled, stale int
	var lastSeen sql.NullInt64
	err := row.Scan(&item.ID, &item.SiteID, &item.EndpointID, &item.ModelName, &display, &enabled, &stale,
		&item.MissingCount, &lastSeen, &item.Revision, &item.CreatedAt, &item.UpdatedAt)
	item.DisplayName = display.String
	item.Enabled = enabled == 1
	item.Stale = stale == 1
	item.LastSeenAt = int64Ptr(lastSeen)
	return item, err
}

const credentialAccessSelect = `SELECT site_id,credential_id,site_model_id,availability,missing_count,
last_seen_at,last_checked_at,revision,updated_at FROM credential_model_access`

func scanCredentialModelAccess(row scanner) (CredentialModelAccess, error) {
	var item CredentialModelAccess
	var lastSeen, lastChecked sql.NullInt64
	err := row.Scan(&item.SiteID, &item.CredentialID, &item.SiteModelID, &item.Availability,
		&item.MissingCount, &lastSeen, &lastChecked, &item.Revision, &item.UpdatedAt)
	item.LastSeenAt = int64Ptr(lastSeen)
	item.LastCheckedAt = int64Ptr(lastChecked)
	return item, err
}

func validModelAvailability(value string) bool {
	switch value {
	case "unknown", "supported", "unsupported":
		return true
	default:
		return false
	}
}
