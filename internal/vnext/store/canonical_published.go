package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func (s *Store) CreatePublishedModel(ctx context.Context, input PublishedModelWrite, orderedProviderTargetIDs []int64) (PublishedModel, error) {
	return s.createPublishedModel(ctx, 0, 0, input, orderedProviderTargetIDs)
}

func (s *Store) CreatePublishedModelCAS(
	ctx context.Context,
	defaultProfileID, expectedProfileRevision int64,
	input PublishedModelWrite,
	orderedProviderTargetIDs []int64,
) (PublishedModel, error) {
	if defaultProfileID <= 0 || expectedProfileRevision <= 0 {
		return PublishedModel{}, errors.New("default profile ID and expected revision are required")
	}
	return s.createPublishedModel(ctx, defaultProfileID, expectedProfileRevision, input, orderedProviderTargetIDs)
}

func (s *Store) createPublishedModel(
	ctx context.Context,
	defaultProfileID, expectedProfileRevision int64,
	input PublishedModelWrite,
	orderedProviderTargetIDs []int64,
) (PublishedModel, error) {
	input = normalizePublishedModelWrite(input)
	if err := validatePublishedModelWrite(input); err != nil {
		return PublishedModel{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return PublishedModel{}, err
	}
	defer tx.Rollback()
	if defaultProfileID == 0 {
		if err := tx.QueryRowContext(ctx, `SELECT id FROM routing_profiles WHERE is_default=1`).Scan(&defaultProfileID); err != nil {
			return PublishedModel{}, err
		}
	} else if err := requireDefaultProfileTx(ctx, tx, defaultProfileID); err != nil {
		return PublishedModel{}, err
	}
	targets, err := loadUniqueProviderTargetsTx(ctx, tx, orderedProviderTargetIDs)
	if err != nil {
		return PublishedModel{}, err
	}
	now := NowMS()
	if err := bumpRoutingProfileRevisionTx(ctx, tx, defaultProfileID, expectedProfileRevision, now); err != nil {
		return PublishedModel{}, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO published_models(
public_name,official_price_sku,enabled,revision,created_at,updated_at) VALUES (?,?,?,1,?,?)`,
		input.PublicName, input.OfficialPriceSKU, boolInt(input.Enabled), now, now)
	if err != nil {
		return PublishedModel{}, normalizeInventoryConflict(err)
	}
	modelID, err := result.LastInsertId()
	if err != nil {
		return PublishedModel{}, err
	}
	if err := insertPublishedModelTargetsTx(ctx, tx, modelID, targets, now); err != nil {
		return PublishedModel{}, err
	}
	if err := tx.Commit(); err != nil {
		return PublishedModel{}, err
	}
	return s.GetPublishedModel(ctx, modelID)
}

func (s *Store) GetPublishedModel(ctx context.Context, id int64) (PublishedModel, error) {
	var item PublishedModel
	var enabled int
	err := s.DB.QueryRowContext(ctx, `SELECT id,public_name,official_price_sku,enabled,revision,created_at,updated_at
FROM published_models WHERE id=?`, id).Scan(&item.ID, &item.PublicName, &item.OfficialPriceSKU, &enabled,
		&item.Revision, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return PublishedModel{}, err
	}
	item.Enabled = enabled == 1
	item.Targets, err = s.listPublishedModelTargets(ctx, item.ID)
	if err != nil {
		return PublishedModel{}, err
	}
	return item, nil
}

func (s *Store) ListPublishedModels(ctx context.Context) ([]PublishedModel, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,public_name,official_price_sku,enabled,revision,created_at,updated_at
FROM published_models ORDER BY public_name COLLATE BINARY,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]PublishedModel, 0)
	for rows.Next() {
		var item PublishedModel
		var enabled int
		if err := rows.Scan(&item.ID, &item.PublicName, &item.OfficialPriceSKU, &enabled,
			&item.Revision, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.Enabled = enabled == 1
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range items {
		items[index].Targets, err = s.listPublishedModelTargets(ctx, items[index].ID)
		if err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (s *Store) UpdatePublishedModel(ctx context.Context, id int64, input PublishedModelUpdate) (PublishedModel, error) {
	write := normalizePublishedModelWrite(PublishedModelWrite{
		PublicName: input.PublicName, OfficialPriceSKU: input.OfficialPriceSKU, Enabled: input.Enabled,
	})
	if id <= 0 || input.ExpectedRevision <= 0 {
		return PublishedModel{}, errors.New("published model ID and expected revision are required")
	}
	if err := validatePublishedModelWrite(write); err != nil {
		return PublishedModel{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return PublishedModel{}, err
	}
	defer tx.Rollback()
	now := NowMS()
	result, err := tx.ExecContext(ctx, `UPDATE published_models SET
public_name=?,official_price_sku=?,enabled=?,revision=revision+1,updated_at=? WHERE id=? AND revision=?`,
		write.PublicName, write.OfficialPriceSKU, boolInt(write.Enabled), now, id, input.ExpectedRevision)
	if err != nil {
		return PublishedModel{}, normalizeInventoryConflict(err)
	}
	if err := requireRevisionChange(ctx, tx, result, `SELECT 1 FROM published_models WHERE id=?`, id); err != nil {
		return PublishedModel{}, err
	}
	var defaultProfileID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM routing_profiles WHERE is_default=1`).Scan(&defaultProfileID); err != nil {
		return PublishedModel{}, err
	}
	if err := bumpRoutingProfileRevisionTx(ctx, tx, defaultProfileID, 0, now); err != nil {
		return PublishedModel{}, err
	}
	if err := tx.Commit(); err != nil {
		return PublishedModel{}, err
	}
	return s.GetPublishedModel(ctx, id)
}

// DeletePublishedModel removes one model from the canonical downstream
// catalog. Sparse profile overrides and monitor history cascade with the model,
// while request/accounting snapshots remain independent historical evidence.
func (s *Store) DeletePublishedModel(ctx context.Context, id, expectedRevision int64) error {
	if id <= 0 || expectedRevision <= 0 {
		return errors.New("published model ID and expected revision are required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var defaultProfileID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM routing_profiles WHERE is_default=1`).Scan(&defaultProfileID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM published_models WHERE id=? AND revision=?`, id, expectedRevision)
	if err != nil {
		return err
	}
	if err := requireRevisionChange(ctx, tx, result, `SELECT 1 FROM published_models WHERE id=?`, id); err != nil {
		return err
	}
	if err := bumpRoutingProfileRevisionTx(ctx, tx, defaultProfileID, 0, NowMS()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ReplacePublishedModelTargets(ctx context.Context, modelID, expectedRevision int64, orderedProviderTargetIDs []int64) (PublishedModel, error) {
	if modelID <= 0 || expectedRevision <= 0 {
		return PublishedModel{}, errors.New("published model ID and expected revision are required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return PublishedModel{}, err
	}
	defer tx.Rollback()
	targets, err := loadUniqueProviderTargetsTx(ctx, tx, orderedProviderTargetIDs)
	if err != nil {
		return PublishedModel{}, err
	}
	now := NowMS()
	result, err := tx.ExecContext(ctx, `UPDATE published_models SET revision=revision+1,updated_at=? WHERE id=? AND revision=?`,
		now, modelID, expectedRevision)
	if err != nil {
		return PublishedModel{}, err
	}
	if err := requireRevisionChange(ctx, tx, result, `SELECT 1 FROM published_models WHERE id=?`, modelID); err != nil {
		return PublishedModel{}, err
	}

	type existingTarget struct {
		id         int64
		providerID int64
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,provider_model_target_id FROM published_model_targets WHERE published_model_id=?`, modelID)
	if err != nil {
		return PublishedModel{}, err
	}
	existing := make(map[int64]existingTarget)
	for rows.Next() {
		var item existingTarget
		if err := rows.Scan(&item.id, &item.providerID); err != nil {
			rows.Close()
			return PublishedModel{}, err
		}
		existing[item.providerID] = item
	}
	if err := rows.Close(); err != nil {
		return PublishedModel{}, err
	}
	if err := rows.Err(); err != nil {
		return PublishedModel{}, err
	}
	keep := make(map[int64]struct{}, len(targets))
	for _, target := range targets {
		keep[target.ID] = struct{}{}
	}
	for providerID, item := range existing {
		if _, ok := keep[providerID]; ok {
			continue
		}
		var references int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM routing_profile_route_targets WHERE published_model_target_id=?`, item.id).Scan(&references); err != nil {
			return PublishedModel{}, err
		}
		if references > 0 {
			return PublishedModel{}, ErrPublishedTargetInUse
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE published_model_targets SET position=position+1000000 WHERE published_model_id=?`, modelID); err != nil {
		return PublishedModel{}, err
	}
	for position, target := range targets {
		if current, ok := existing[target.ID]; ok {
			if _, err := tx.ExecContext(ctx, `UPDATE published_model_targets SET
position=?,revision=revision+1,updated_at=? WHERE id=? AND published_model_id=?`, position, now, current.id, modelID); err != nil {
				return PublishedModel{}, err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO published_model_targets(
published_model_id,provider_model_target_id,position,revision,created_at,updated_at) VALUES (?,?,?,1,?,?)`,
			modelID, target.ID, position, now, now); err != nil {
			return PublishedModel{}, err
		}
	}
	for providerID, item := range existing {
		if _, ok := keep[providerID]; ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM published_model_targets WHERE id=? AND published_model_id=?`, item.id, modelID); err != nil {
			return PublishedModel{}, err
		}
	}
	var defaultProfileID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM routing_profiles WHERE is_default=1`).Scan(&defaultProfileID); err != nil {
		return PublishedModel{}, err
	}
	if err := bumpRoutingProfileRevisionTx(ctx, tx, defaultProfileID, 0, now); err != nil {
		return PublishedModel{}, err
	}
	if err := tx.Commit(); err != nil {
		return PublishedModel{}, err
	}
	return s.GetPublishedModel(ctx, modelID)
}

func (s *Store) listPublishedModelTargets(ctx context.Context, modelID int64) ([]PublishedModelTarget, error) {
	rows, err := s.DB.QueryContext(ctx, publishedModelTargetSelect+`
WHERE t.published_model_id=? ORDER BY t.position,t.id`, modelID)
	if err != nil {
		return nil, err
	}
	return scanPublishedModelTargets(rows)
}

func listPublishedModelTargetsTx(ctx context.Context, tx *sql.Tx, modelID int64) ([]PublishedModelTarget, error) {
	rows, err := tx.QueryContext(ctx, publishedModelTargetSelect+`
WHERE t.published_model_id=? ORDER BY t.position,t.id`, modelID)
	if err != nil {
		return nil, err
	}
	return scanPublishedModelTargets(rows)
}

const publishedModelTargetSelect = `SELECT t.id,t.published_model_id,p.site_id,s.name,p.endpoint_id,e.name,
t.provider_model_target_id,p.source_model,e.wire_protocol,e.surface,t.position,t.revision,t.created_at,t.updated_at
FROM published_model_targets t
JOIN provider_model_targets p ON p.id=t.provider_model_target_id
JOIN sites s ON s.id=p.site_id
JOIN site_endpoints e ON e.id=p.endpoint_id AND e.site_id=p.site_id`

func scanPublishedModelTargets(rows *sql.Rows) ([]PublishedModelTarget, error) {
	defer rows.Close()
	items := make([]PublishedModelTarget, 0)
	for rows.Next() {
		var item PublishedModelTarget
		if err := rows.Scan(&item.ID, &item.PublishedModelID, &item.SiteID, &item.SiteName,
			&item.EndpointID, &item.EndpointName, &item.ProviderModelTargetID, &item.SourceModel,
			&item.WireProtocol, &item.Surface, &item.Position, &item.Revision, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type providerTargetRef struct {
	ID          int64
	SiteID      int64
	SourceModel string
}

func loadUniqueProviderTargetsTx(ctx context.Context, tx *sql.Tx, orderedTargetIDs []int64) ([]providerTargetRef, error) {
	if len(orderedTargetIDs) == 0 {
		return nil, errors.New("a published model requires at least one provider target")
	}
	seenTargets := make(map[int64]struct{}, len(orderedTargetIDs))
	items := make([]providerTargetRef, 0, len(orderedTargetIDs))
	for _, targetID := range orderedTargetIDs {
		if targetID <= 0 {
			return nil, errors.New("provider target IDs must be positive")
		}
		if _, duplicate := seenTargets[targetID]; duplicate {
			return nil, fmt.Errorf("provider target %d is duplicated", targetID)
		}
		seenTargets[targetID] = struct{}{}
		var item providerTargetRef
		if err := tx.QueryRowContext(ctx, `SELECT id,site_id,source_model FROM provider_model_targets WHERE id=?`, targetID).
			Scan(&item.ID, &item.SiteID, &item.SourceModel); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func insertPublishedModelTargetsTx(ctx context.Context, tx *sql.Tx, modelID int64, targets []providerTargetRef, now int64) error {
	for position, target := range targets {
		if _, err := tx.ExecContext(ctx, `INSERT INTO published_model_targets(
published_model_id,provider_model_target_id,position,revision,created_at,updated_at) VALUES (?,?,?,1,?,?)`,
			modelID, target.ID, position, now, now); err != nil {
			return err
		}
	}
	return nil
}

func normalizePublishedModelWrite(input PublishedModelWrite) PublishedModelWrite {
	input.PublicName = strings.TrimSpace(input.PublicName)
	input.OfficialPriceSKU = strings.TrimSpace(input.OfficialPriceSKU)
	if input.OfficialPriceSKU == "" {
		input.OfficialPriceSKU = input.PublicName
	}
	return input
}

func validatePublishedModelWrite(input PublishedModelWrite) error {
	if input.PublicName == "" || input.OfficialPriceSKU == "" {
		return errors.New("published model name and official price SKU are required")
	}
	if len(input.PublicName) > 255 || len(input.OfficialPriceSKU) > 255 {
		return errors.New("published model name and official price SKU must not exceed 255 bytes")
	}
	return nil
}
