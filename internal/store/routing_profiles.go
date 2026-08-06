package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func (s *Store) CreateRoutingProfile(ctx context.Context, name string) (int64, error) {
	name, err := normalizeRoutingProfileName(name)
	if err != nil {
		return 0, err
	}
	now := NowMS()
	result, err := s.DB.ExecContext(ctx, `INSERT INTO routing_profiles(name,revision,created_at,updated_at)
VALUES (?,1,?,?)`, name, now, now)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) ListRoutingProfiles(ctx context.Context) ([]RoutingProfile, error) {
	rows, err := s.DB.QueryContext(ctx, routingProfileSelect+` GROUP BY p.id ORDER BY p.name COLLATE NOCASE,p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]RoutingProfile, 0)
	for rows.Next() {
		item, scanErr := scanRoutingProfile(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetRoutingProfile(ctx context.Context, id int64) (RoutingProfile, error) {
	if id <= 0 {
		return RoutingProfile{}, sql.ErrNoRows
	}
	return scanRoutingProfile(s.DB.QueryRowContext(ctx, routingProfileSelect+` WHERE p.id=? GROUP BY p.id`, id))
}

func (s *Store) UpdateRoutingProfile(ctx context.Context, id, expectedRevision int64, name string) error {
	name, err := normalizeRoutingProfileName(name)
	if err != nil {
		return err
	}
	query := `UPDATE routing_profiles SET name=?,revision=revision+1,updated_at=? WHERE id=?`
	args := []any{name, NowMS(), id}
	if expectedRevision > 0 {
		query += ` AND revision=?`
		args = append(args, expectedRevision)
	}
	result, err := s.DB.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	return revisionResult(ctx, s.DB, result, "routing_profiles", id)
}

func (s *Store) DeleteRoutingProfile(ctx context.Context, id, expectedRevision int64) error {
	query := `DELETE FROM routing_profiles WHERE id=?`
	args := []any{id}
	if expectedRevision > 0 {
		query += ` AND revision=?`
		args = append(args, expectedRevision)
	}
	result, err := s.DB.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	return revisionResult(ctx, s.DB, result, "routing_profiles", id)
}

// GetRoutingProfileModelRoute returns the effective ordered targets. When no
// override exists, Targets contains the published model's default route and
// InheritsDefault is true.
func (s *Store) GetRoutingProfileModelRoute(ctx context.Context, profileID, publishedModelID int64) (RoutingProfileModelRoute, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RoutingProfileModelRoute{}, err
	}
	defer tx.Rollback()

	var profileRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM routing_profiles WHERE id=?`, profileID).Scan(&profileRevision); err != nil {
		return RoutingProfileModelRoute{}, err
	}
	var modelExists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM published_models WHERE id=?`, publishedModelID).Scan(&modelExists); err != nil {
		return RoutingProfileModelRoute{}, err
	}

	items, err := listRoutingProfileTargetsTx(ctx, tx, profileID, publishedModelID)
	if err != nil {
		return RoutingProfileModelRoute{}, err
	}
	inheritsDefault := len(items) == 0
	if inheritsDefault {
		items, err = listDefaultRouteSiteTargetsTx(ctx, tx, publishedModelID)
		if err != nil {
			return RoutingProfileModelRoute{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return RoutingProfileModelRoute{}, err
	}
	return RoutingProfileModelRoute{
		RoutingProfileID: profileID,
		PublishedModelID: publishedModelID,
		ProfileRevision:  profileRevision,
		InheritsDefault:  inheritsDefault,
		Targets:          items,
	}, nil
}

// SetRoutingProfileModelTargets replaces one model override atomically. The
// ordered IDs must be a non-empty, duplicate-free subset of that model's
// existing route_site_targets.
func (s *Store) SetRoutingProfileModelTargets(ctx context.Context, profileID, publishedModelID, expectedRevision int64, targetIDs []int64) error {
	if len(targetIDs) == 0 {
		return errors.New("routing profile override must contain at least one target; clear the override to inherit the default route")
	}
	seen := make(map[int64]struct{}, len(targetIDs))
	for _, targetID := range targetIDs {
		if targetID <= 0 {
			return errors.New("routing profile target IDs must be positive")
		}
		if _, duplicate := seen[targetID]; duplicate {
			return fmt.Errorf("routing profile target %d is duplicated", targetID)
		}
		seen[targetID] = struct{}{}
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := NowMS()
	if err := bumpRoutingProfileRevisionTx(ctx, tx, profileID, expectedRevision, now); err != nil {
		return err
	}
	if err := validateRoutingProfileTargetsTx(ctx, tx, publishedModelID, targetIDs); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM routing_profile_model_targets WHERE routing_profile_id=? AND published_model_id=?`, profileID, publishedModelID); err != nil {
		return err
	}
	for position, targetID := range targetIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO routing_profile_model_targets(
routing_profile_id,published_model_id,route_site_target_id,position,created_at,updated_at)
VALUES (?,?,?,?,?,?)`, profileID, publishedModelID, targetID, position, now, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ClearRoutingProfileModelTargets(ctx context.Context, profileID, publishedModelID, expectedRevision int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := NowMS()
	if err := bumpRoutingProfileRevisionTx(ctx, tx, profileID, expectedRevision, now); err != nil {
		return err
	}
	var modelExists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM published_models WHERE id=?`, publishedModelID).Scan(&modelExists); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM routing_profile_model_targets WHERE routing_profile_id=? AND published_model_id=?`, profileID, publishedModelID); err != nil {
		return err
	}
	return tx.Commit()
}

func validateRoutingProfileTargetsTx(ctx context.Context, tx *sql.Tx, publishedModelID int64, targetIDs []int64) error {
	var modelExists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM published_models WHERE id=?`, publishedModelID).Scan(&modelExists); err != nil {
		return err
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(targetIDs)), ",")
	args := make([]any, 0, len(targetIDs)+1)
	args = append(args, publishedModelID)
	for _, targetID := range targetIDs {
		args = append(args, targetID)
	}
	var count int
	query := `SELECT COUNT(*) FROM route_site_targets WHERE published_model_id=? AND id IN (` + placeholders + `)`
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return err
	}
	if count != len(targetIDs) {
		return errors.New("every routing profile target must belong to the selected published model")
	}
	return nil
}

func listRoutingProfileTargetsTx(ctx context.Context, tx *sql.Tx, profileID, publishedModelID int64) ([]RouteSiteTarget, error) {
	rows, err := tx.QueryContext(ctx, routeSiteTargetSelect+`
JOIN routing_profile_model_targets rpt ON rpt.route_site_target_id=t.id
WHERE rpt.routing_profile_id=? AND rpt.published_model_id=?
ORDER BY rpt.position,t.id`, profileID, publishedModelID)
	if err != nil {
		return nil, err
	}
	return scanRouteSiteTargets(rows)
}

func listDefaultRouteSiteTargetsTx(ctx context.Context, tx *sql.Tx, publishedModelID int64) ([]RouteSiteTarget, error) {
	rows, err := tx.QueryContext(ctx, routeSiteTargetSelect+` WHERE t.published_model_id=? ORDER BY t.position,t.id`, publishedModelID)
	if err != nil {
		return nil, err
	}
	return scanRouteSiteTargets(rows)
}

func scanRouteSiteTargets(rows *sql.Rows) ([]RouteSiteTarget, error) {
	defer rows.Close()
	items := make([]RouteSiteTarget, 0)
	for rows.Next() {
		item, err := scanRouteSiteTarget(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func bumpRoutingProfileRevisionTx(ctx context.Context, tx *sql.Tx, profileID, expectedRevision, now int64) error {
	query := `UPDATE routing_profiles SET revision=revision+1,updated_at=? WHERE id=?`
	args := []any{now, profileID}
	if expectedRevision > 0 {
		query += ` AND revision=?`
		args = append(args, expectedRevision)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	return revisionResult(ctx, tx, result, "routing_profiles", profileID)
}

func normalizeRoutingProfileName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("routing profile name is required")
	}
	if len(name) > 120 {
		return "", errors.New("routing profile name must not exceed 120 bytes")
	}
	if strings.EqualFold(name, DefaultRoutingProfileName) {
		return "", fmt.Errorf("%q is reserved for the default route", DefaultRoutingProfileName)
	}
	return name, nil
}

const routingProfileSelect = `SELECT p.id,p.name,p.revision,COUNT(DISTINCT rpt.published_model_id),p.created_at,p.updated_at
FROM routing_profiles p
LEFT JOIN routing_profile_model_targets rpt ON rpt.routing_profile_id=p.id`

func scanRoutingProfile(row scanner) (RoutingProfile, error) {
	var item RoutingProfile
	err := row.Scan(&item.ID, &item.Name, &item.Revision, &item.ModelOverrideCount, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}
