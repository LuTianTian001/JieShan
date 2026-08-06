package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrRevisionConflict = errors.New("revision conflict")

func (s *Store) CreateSite(ctx context.Context, input SiteWrite) (int64, error) {
	input, err := normalizeSiteWrite(input)
	if err != nil {
		return 0, err
	}
	now := NowMS()
	result, err := s.DB.ExecContext(ctx, `INSERT INTO sites(name,dashboard_url,enabled,revision,created_at,updated_at)
VALUES (?,?,?,1,?,?)`, input.Name, nullableString(input.DashboardURL), boolInt(input.Enabled), now, now)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) GetSite(ctx context.Context, id int64) (Site, error) {
	return scanSite(s.DB.QueryRowContext(ctx, siteSelect+` WHERE id=?`, id))
}

func (s *Store) ListSites(ctx context.Context) ([]Site, error) {
	rows, err := s.DB.QueryContext(ctx, siteSelect+` ORDER BY name COLLATE NOCASE,id`)
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

// ListSiteSummaries loads the complete site inventory in one aggregate query.
// It deliberately exposes no synthetic site-wide status or latency.
func (s *Store) ListSiteSummaries(ctx context.Context) ([]SiteSummary, error) {
	rows, err := s.DB.QueryContext(ctx, `WITH
endpoint_counts AS (
  SELECT site_id,COUNT(*) total,SUM(CASE WHEN enabled=1 THEN 1 ELSE 0 END) enabled
  FROM inference_endpoints GROUP BY site_id
),
credential_counts AS (
  SELECT site_id,COUNT(*) total,SUM(CASE WHEN enabled=1 THEN 1 ELSE 0 END) enabled,
    SUM(CASE WHEN enabled=1 AND runtime_state!='active' THEN 1 ELSE 0 END) unavailable
  FROM inference_credentials GROUP BY site_id
),
model_counts AS (
  SELECT site_id,COUNT(*) total,SUM(CASE WHEN enabled=1 AND stale=0 THEN 1 ELSE 0 END) active,MAX(last_seen_at) last_seen_at
  FROM site_models GROUP BY site_id
),
published_counts AS (
  SELECT site_id,COUNT(DISTINCT published_model_id) total
  FROM route_site_targets GROUP BY site_id
)
SELECT s.id,s.name,s.dashboard_url,s.enabled,s.revision,s.created_at,s.updated_at,
  COALESCE(e.total,0),COALESCE(e.enabled,0),
  COALESCE(c.total,0),COALESCE(c.enabled,0),COALESCE(c.unavailable,0),
  COALESCE(m.total,0),COALESCE(m.active,0),COALESCE(p.total,0),m.last_seen_at
FROM sites s
LEFT JOIN endpoint_counts e ON e.site_id=s.id
LEFT JOIN credential_counts c ON c.site_id=s.id
LEFT JOIN model_counts m ON m.site_id=s.id
LEFT JOIN published_counts p ON p.site_id=s.id
ORDER BY s.name COLLATE NOCASE,s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]SiteSummary, 0)
	for rows.Next() {
		var item SiteSummary
		var dashboard sql.NullString
		var enabled int
		var lastSeen sql.NullInt64
		if err := rows.Scan(&item.ID, &item.Name, &dashboard, &enabled, &item.Revision, &item.CreatedAt, &item.UpdatedAt,
			&item.EndpointCount, &item.EnabledEndpointCount, &item.CredentialCount, &item.EnabledCredentialCount,
			&item.UnavailableCredentialCount, &item.ModelCount, &item.ActiveModelCount, &item.PublishedModelCount, &lastSeen); err != nil {
			return nil, err
		}
		item.DashboardURL = dashboard.String
		item.Enabled = enabled == 1
		item.LastModelSeenAt = int64Ptr(lastSeen)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) UpdateSite(ctx context.Context, id, expectedRevision int64, input SiteWrite) error {
	input, err := normalizeSiteWrite(input)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := NowMS()
	if err := bumpPublishedModelsForSiteTx(ctx, tx, id, now); err != nil {
		return err
	}
	query := `UPDATE sites SET name=?,dashboard_url=?,enabled=?,revision=revision+1,updated_at=? WHERE id=?`
	args := []any{input.Name, nullableString(input.DashboardURL), boolInt(input.Enabled), now, id}
	if expectedRevision > 0 {
		query += " AND revision=?"
		args = append(args, expectedRevision)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if err := revisionResult(ctx, tx, result, "sites", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteSite(ctx context.Context, id, expectedRevision int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := bumpPublishedModelsForSiteTx(ctx, tx, id, NowMS()); err != nil {
		return err
	}
	query := "DELETE FROM sites WHERE id=?"
	args := []any{id}
	if expectedRevision > 0 {
		query += " AND revision=?"
		args = append(args, expectedRevision)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if err := revisionResult(ctx, tx, result, "sites", id); err != nil {
		return err
	}
	return tx.Commit()
}

func normalizeSiteWrite(input SiteWrite) (SiteWrite, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.DashboardURL = strings.TrimRight(strings.TrimSpace(input.DashboardURL), "/")
	if input.Name == "" {
		return SiteWrite{}, errors.New("site name is required")
	}
	return input, nil
}

const siteSelect = `SELECT id,name,dashboard_url,enabled,revision,created_at,updated_at FROM sites`

func scanSite(row scanner) (Site, error) {
	var item Site
	var dashboard sql.NullString
	var enabled int
	err := row.Scan(&item.ID, &item.Name, &dashboard, &enabled, &item.Revision, &item.CreatedAt, &item.UpdatedAt)
	item.DashboardURL = dashboard.String
	item.Enabled = enabled == 1
	return item, err
}

type revisionQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func revisionResult(ctx context.Context, queryer revisionQuerier, result sql.Result, table string, id int64) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 1 {
		return nil
	}
	var exists int
	if err := queryer.QueryRowContext(ctx, fmt.Sprintf("SELECT 1 FROM %s WHERE id=?", table), id).Scan(&exists); err != nil {
		return err
	}
	return ErrRevisionConflict
}

func bumpSiteRevisionTx(ctx context.Context, tx *sql.Tx, siteID, expectedRevision, now int64) error {
	query := "UPDATE sites SET revision=revision+1,updated_at=? WHERE id=?"
	args := []any{now, siteID}
	if expectedRevision > 0 {
		query += " AND revision=?"
		args = append(args, expectedRevision)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	return revisionResult(ctx, tx, result, "sites", siteID)
}

func normalizedV3JSON(value json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(value)) == 0 {
		return "{}", nil
	}
	if !json.Valid(value) {
		return "", errors.New("custom headers must be valid JSON")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, value); err != nil {
		return "", err
	}
	return compact.String(), nil
}
