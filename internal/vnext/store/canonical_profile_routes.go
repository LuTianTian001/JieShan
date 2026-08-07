package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func (s *Store) GetRoutingProfileRoute(ctx context.Context, profileID, publishedModelID int64) (RoutingProfileRoute, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RoutingProfileRoute{}, err
	}
	defer tx.Rollback()
	item, err := loadEffectiveRoutingProfileRouteTx(ctx, tx, profileID, publishedModelID)
	if err != nil {
		return RoutingProfileRoute{}, err
	}
	if err := tx.Commit(); err != nil {
		return RoutingProfileRoute{}, err
	}
	return item, nil
}

func (s *Store) ListRoutingProfileRoutes(ctx context.Context, profileID int64) ([]RoutingProfileRoute, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM routing_profiles WHERE id=?`, profileID).Scan(&exists); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM published_models ORDER BY public_name COLLATE BINARY,id`)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	items := make([]RoutingProfileRoute, 0, len(ids))
	for _, modelID := range ids {
		item, err := loadEffectiveRoutingProfileRouteTx(ctx, tx, profileID, modelID)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) CreateRoutingProfileRoute(ctx context.Context, profileID, expectedProfileRevision int64, input RoutingProfileRouteWrite) (RoutingProfileRoute, error) {
	if profileID <= 0 || expectedProfileRevision <= 0 || input.PublishedModelID <= 0 {
		return RoutingProfileRoute{}, errors.New("profile, published model, and expected profile revision are required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return RoutingProfileRoute{}, err
	}
	defer tx.Rollback()
	if err := requireCustomProfileTx(ctx, tx, profileID); err != nil {
		return RoutingProfileRoute{}, err
	}
	if err := validatePublishedModelTargetIDsTx(ctx, tx, input.PublishedModelID, input.TargetIDs); err != nil {
		return RoutingProfileRoute{}, err
	}
	now := NowMS()
	if err := bumpRoutingProfileRevisionTx(ctx, tx, profileID, expectedProfileRevision, now); err != nil {
		return RoutingProfileRoute{}, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO routing_profile_model_routes(
routing_profile_id,published_model_id,enabled,targets_overridden,revision,created_at,updated_at)
SELECT ?,id,?,?,1,?,? FROM published_models WHERE id=?`, profileID, boolInt(input.Enabled), boolInt(len(input.TargetIDs) > 0),
		now, now, input.PublishedModelID)
	if err != nil {
		return RoutingProfileRoute{}, normalizeInventoryConflict(err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return RoutingProfileRoute{}, err
	}
	if changed != 1 {
		return RoutingProfileRoute{}, sql.ErrNoRows
	}
	if err := insertRoutingProfileTargetsTx(ctx, tx, profileID, input.PublishedModelID, input.TargetIDs, now); err != nil {
		return RoutingProfileRoute{}, err
	}
	if err := tx.Commit(); err != nil {
		return RoutingProfileRoute{}, err
	}
	return s.GetRoutingProfileRoute(ctx, profileID, input.PublishedModelID)
}

func (s *Store) UpdateRoutingProfileRoute(ctx context.Context, profileID, publishedModelID, expectedRevision int64, enabled bool) (RoutingProfileRoute, error) {
	if profileID <= 0 || publishedModelID <= 0 || expectedRevision <= 0 {
		return RoutingProfileRoute{}, errors.New("profile, published model, and expected route revision are required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return RoutingProfileRoute{}, err
	}
	defer tx.Rollback()
	if err := requireCustomProfileTx(ctx, tx, profileID); err != nil {
		return RoutingProfileRoute{}, err
	}
	now := NowMS()
	result, err := tx.ExecContext(ctx, `UPDATE routing_profile_model_routes SET
enabled=?,revision=revision+1,updated_at=? WHERE routing_profile_id=? AND published_model_id=? AND revision=?`,
		boolInt(enabled), now, profileID, publishedModelID, expectedRevision)
	if err != nil {
		return RoutingProfileRoute{}, err
	}
	if err := requireRevisionChange(ctx, tx, result, `SELECT 1 FROM routing_profile_model_routes WHERE routing_profile_id=? AND published_model_id=?`, profileID, publishedModelID); err != nil {
		return RoutingProfileRoute{}, err
	}
	if err := bumpRoutingProfileRevisionTx(ctx, tx, profileID, 0, now); err != nil {
		return RoutingProfileRoute{}, err
	}
	if err := tx.Commit(); err != nil {
		return RoutingProfileRoute{}, err
	}
	return s.GetRoutingProfileRoute(ctx, profileID, publishedModelID)
}

func (s *Store) ReplaceRoutingProfileRouteTargets(ctx context.Context, profileID, publishedModelID, expectedRevision int64, targetIDs []int64) (RoutingProfileRoute, error) {
	if profileID <= 0 || publishedModelID <= 0 || expectedRevision <= 0 {
		return RoutingProfileRoute{}, errors.New("profile, published model, and expected route revision are required")
	}
	if len(targetIDs) == 0 {
		return RoutingProfileRoute{}, errors.New("a target override must contain at least one published model target")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return RoutingProfileRoute{}, err
	}
	defer tx.Rollback()
	if err := requireCustomProfileTx(ctx, tx, profileID); err != nil {
		return RoutingProfileRoute{}, err
	}
	if err := validatePublishedModelTargetIDsTx(ctx, tx, publishedModelID, targetIDs); err != nil {
		return RoutingProfileRoute{}, err
	}
	now := NowMS()
	var localRevision int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM routing_profile_model_routes
WHERE routing_profile_id=? AND published_model_id=?`, profileID, publishedModelID).Scan(&localRevision)
	switch {
	case err == nil:
		if localRevision != expectedRevision {
			return RoutingProfileRoute{}, ErrRevisionConflict
		}
		result, err := tx.ExecContext(ctx, `UPDATE routing_profile_model_routes SET
targets_overridden=1,revision=revision+1,updated_at=? WHERE routing_profile_id=? AND published_model_id=? AND revision=?`,
			now, profileID, publishedModelID, expectedRevision)
		if err != nil {
			return RoutingProfileRoute{}, err
		}
		if err := requireRevisionChange(ctx, tx, result, `SELECT 1 FROM routing_profile_model_routes WHERE routing_profile_id=? AND published_model_id=?`, profileID, publishedModelID); err != nil {
			return RoutingProfileRoute{}, err
		}
	case errors.Is(err, sql.ErrNoRows):
		var enabled int
		var publishedRevision int64
		if err := tx.QueryRowContext(ctx, `SELECT enabled,revision FROM published_models WHERE id=?`, publishedModelID).Scan(&enabled, &publishedRevision); err != nil {
			return RoutingProfileRoute{}, err
		}
		if publishedRevision != expectedRevision {
			return RoutingProfileRoute{}, ErrRevisionConflict
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO routing_profile_model_routes(
routing_profile_id,published_model_id,enabled,targets_overridden,revision,created_at,updated_at)
VALUES (?,?,?,1,1,?,?)`, profileID, publishedModelID, enabled, now, now); err != nil {
			return RoutingProfileRoute{}, err
		}
	default:
		return RoutingProfileRoute{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM routing_profile_route_targets WHERE routing_profile_id=? AND published_model_id=?`, profileID, publishedModelID); err != nil {
		return RoutingProfileRoute{}, err
	}
	if err := insertRoutingProfileTargetsTx(ctx, tx, profileID, publishedModelID, targetIDs, now); err != nil {
		return RoutingProfileRoute{}, err
	}
	if err := bumpRoutingProfileRevisionTx(ctx, tx, profileID, 0, now); err != nil {
		return RoutingProfileRoute{}, err
	}
	if err := tx.Commit(); err != nil {
		return RoutingProfileRoute{}, err
	}
	return s.GetRoutingProfileRoute(ctx, profileID, publishedModelID)
}

func (s *Store) DeleteRoutingProfileRoute(ctx context.Context, profileID, publishedModelID, expectedRevision int64) error {
	if profileID <= 0 || publishedModelID <= 0 || expectedRevision <= 0 {
		return errors.New("profile, published model, and expected route revision are required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireCustomProfileTx(ctx, tx, profileID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM routing_profile_model_routes
WHERE routing_profile_id=? AND published_model_id=? AND revision=?`, profileID, publishedModelID, expectedRevision)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		var exists int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM routing_profile_model_routes WHERE routing_profile_id=? AND published_model_id=?`, profileID, publishedModelID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRoutingProfileRouteNotFound
		}
		if err != nil {
			return err
		}
		return ErrRevisionConflict
	}
	if err := bumpRoutingProfileRevisionTx(ctx, tx, profileID, 0, NowMS()); err != nil {
		return err
	}
	return tx.Commit()
}

func loadEffectiveRoutingProfileRouteTx(ctx context.Context, tx *sql.Tx, profileID, publishedModelID int64) (RoutingProfileRoute, error) {
	var profileName string
	var isDefault int
	if err := tx.QueryRowContext(ctx, `SELECT name,is_default FROM routing_profiles WHERE id=?`, profileID).Scan(&profileName, &isDefault); err != nil {
		return RoutingProfileRoute{}, err
	}
	var item RoutingProfileRoute
	var modelEnabled int
	if err := tx.QueryRowContext(ctx, `SELECT id,public_name,official_price_sku,enabled,revision,created_at,updated_at
FROM published_models WHERE id=?`, publishedModelID).Scan(&item.PublishedModelID, &item.PublicName, &item.OfficialPriceSKU,
		&modelEnabled, &item.PublishedModelRevision, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return RoutingProfileRoute{}, err
	}
	item.RoutingProfileID = profileID
	item.RoutingProfileName = profileName
	item.Enabled = modelEnabled == 1
	item.Revision = item.PublishedModelRevision
	item.SourceProfileID = profileID
	item.SourceProfileName = profileName
	var defaultID int64
	var defaultName string
	if err := tx.QueryRowContext(ctx, `SELECT id,name FROM routing_profiles WHERE is_default=1`).Scan(&defaultID, &defaultName); err != nil {
		return RoutingProfileRoute{}, err
	}
	if isDefault == 0 {
		var localEnabled, targetsOverridden int
		var localRevision, createdAt, updatedAt int64
		err := tx.QueryRowContext(ctx, `SELECT enabled,targets_overridden,revision,created_at,updated_at
FROM routing_profile_model_routes WHERE routing_profile_id=? AND published_model_id=?`, profileID, publishedModelID).
			Scan(&localEnabled, &targetsOverridden, &localRevision, &createdAt, &updatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			item.Inherited = true
			item.SourceProfileID = defaultID
			item.SourceProfileName = defaultName
		} else if err != nil {
			return RoutingProfileRoute{}, err
		} else {
			item.Enabled = localEnabled == 1
			item.TargetsOverridden = targetsOverridden == 1
			item.Revision = localRevision
			item.CreatedAt = createdAt
			item.UpdatedAt = updatedAt
		}
	}
	var err error
	if item.TargetsOverridden {
		item.Targets, err = listRoutingProfileRouteTargetsTx(ctx, tx, profileID, publishedModelID)
	} else {
		item.Targets, err = listPublishedModelTargetsTx(ctx, tx, publishedModelID)
	}
	if err != nil {
		return RoutingProfileRoute{}, err
	}
	return item, nil
}

func listRoutingProfileRouteTargetsTx(ctx context.Context, tx *sql.Tx, profileID, modelID int64) ([]PublishedModelTarget, error) {
	rows, err := tx.QueryContext(ctx, `SELECT t.id,t.published_model_id,p.site_id,s.name,p.endpoint_id,e.name,
t.provider_model_target_id,p.source_model,e.wire_protocol,e.surface,r.position,t.revision,t.created_at,t.updated_at
FROM published_model_targets t
JOIN provider_model_targets p ON p.id=t.provider_model_target_id
JOIN sites s ON s.id=p.site_id
JOIN site_endpoints e ON e.id=p.endpoint_id AND e.site_id=p.site_id
JOIN routing_profile_route_targets r ON r.published_model_target_id=t.id AND r.published_model_id=t.published_model_id
WHERE r.routing_profile_id=? AND r.published_model_id=? ORDER BY r.position,t.id`, profileID, modelID)
	if err != nil {
		return nil, err
	}
	return scanPublishedModelTargets(rows)
}

func insertRoutingProfileTargetsTx(ctx context.Context, tx *sql.Tx, profileID, modelID int64, targetIDs []int64, now int64) error {
	for position, targetID := range targetIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO routing_profile_route_targets(
routing_profile_id,published_model_id,published_model_target_id,position,created_at,updated_at)
VALUES (?,?,?,?,?,?)`, profileID, modelID, targetID, position, now, now); err != nil {
			return err
		}
	}
	return nil
}

func validatePublishedModelTargetIDsTx(ctx context.Context, tx *sql.Tx, modelID int64, targetIDs []int64) error {
	seen := make(map[int64]struct{}, len(targetIDs))
	for _, targetID := range targetIDs {
		if targetID <= 0 {
			return errors.New("published model target IDs must be positive")
		}
		if _, duplicate := seen[targetID]; duplicate {
			return fmt.Errorf("published model target %d is duplicated", targetID)
		}
		seen[targetID] = struct{}{}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM published_model_targets WHERE id=? AND published_model_id=?`, targetID, modelID).Scan(&exists); err != nil {
			return errors.New("every routing profile target must belong to the published model default target set")
		}
	}
	return nil
}

func requireCustomProfileTx(ctx context.Context, tx *sql.Tx, profileID int64) error {
	var isDefault int
	if err := tx.QueryRowContext(ctx, `SELECT is_default FROM routing_profiles WHERE id=?`, profileID).Scan(&isDefault); err != nil {
		return err
	}
	if isDefault == 1 {
		return ErrDefaultRoutingProfile
	}
	return nil
}

func requireDefaultProfileTx(ctx context.Context, tx *sql.Tx, profileID int64) error {
	var isDefault int
	if err := tx.QueryRowContext(ctx, `SELECT is_default FROM routing_profiles WHERE id=?`, profileID).Scan(&isDefault); err != nil {
		return err
	}
	if isDefault != 1 {
		return ErrDefaultRoutingProfile
	}
	return nil
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
	return requireRevisionChange(ctx, tx, result, `SELECT 1 FROM routing_profiles WHERE id=?`, profileID)
}
