package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/LuTianTian001/JieShan/internal/inferenceprotocol"
)

func (s *Store) CreatePublishedModel(ctx context.Context, input PublishedModelWrite) (int64, error) {
	input, err := normalizePublishedModelWrite(input)
	if err != nil {
		return 0, err
	}
	now := NowMS()
	result, err := s.DB.ExecContext(ctx, `INSERT INTO published_models(
public_name,display_name,official_price_sku,enabled,monitor_enabled,monitor_interval_seconds,cooldown_seconds,
failure_threshold,failure_window_seconds,first_output_timeout_seconds,stream_idle_timeout_seconds,
request_deadline_seconds,max_attempts,revision,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`, input.PublicName, nullableString(input.DisplayName), nullableString(input.OfficialPriceSKU),
		boolInt(input.Enabled), boolInt(input.MonitorEnabled), input.MonitorIntervalSeconds, input.CooldownSeconds,
		input.FailureThreshold, input.FailureWindowSeconds, input.FirstOutputTimeoutSeconds, input.StreamIdleTimeoutSeconds,
		input.RequestDeadlineSeconds, input.MaxAttempts, now, now)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) GetPublishedModel(ctx context.Context, id int64) (PublishedModel, error) {
	return scanPublishedModel(s.DB.QueryRowContext(ctx, publishedModelSelect+` WHERE id=?`, id))
}

func (s *Store) GetPublishedModelByName(ctx context.Context, publicName string) (PublishedModel, error) {
	return scanPublishedModel(s.DB.QueryRowContext(ctx, publishedModelSelect+` WHERE public_name=?`, strings.TrimSpace(publicName)))
}

func (s *Store) ListPublishedModels(ctx context.Context) ([]PublishedModel, error) {
	rows, err := s.DB.QueryContext(ctx, publishedModelSelect+` ORDER BY public_name COLLATE NOCASE,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]PublishedModel, 0)
	for rows.Next() {
		item, err := scanPublishedModel(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ListPublishedModelRoutes uses two bulk queries and assembles all targets in
// memory, avoiding the route-per-query pattern used by the legacy store.
func (s *Store) ListPublishedModelRoutes(ctx context.Context) ([]PublishedModelRoute, error) {
	models, err := s.ListPublishedModels(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, routeSiteTargetSelect+` ORDER BY t.published_model_id,t.position,t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := make(map[int64][]RouteSiteTarget, len(models))
	for rows.Next() {
		item, err := scanRouteSiteTarget(rows)
		if err != nil {
			return nil, err
		}
		targets[item.PublishedModelID] = append(targets[item.PublishedModelID], item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	items := make([]PublishedModelRoute, 0, len(models))
	for _, model := range models {
		items = append(items, PublishedModelRoute{PublishedModel: model, Targets: append([]RouteSiteTarget(nil), targets[model.ID]...)})
	}
	return items, nil
}

// ResolvePublishedModel resolves the default site order.
func (s *Store) ResolvePublishedModel(ctx context.Context, publicName string, nowMS int64) (ResolvedPublishedModel, error) {
	return s.ResolvePublishedModelForProfile(ctx, publicName, nowMS, nil)
}

// ResolvePublishedModelForProfile returns the enabled site-level route and the
// eligible Key pool for every site at one point in time. A profile only
// overrides models for which it has explicit targets; every other model falls
// back to the default administrator order.
func (s *Store) ResolvePublishedModelForProfile(ctx context.Context, publicName string, nowMS int64, profileID *int64) (ResolvedPublishedModel, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ResolvedPublishedModel{}, err
	}
	defer tx.Rollback()
	model, err := scanPublishedModel(tx.QueryRowContext(ctx, publishedModelSelect+` WHERE public_name=?`, strings.TrimSpace(publicName)))
	if err != nil {
		return ResolvedPublishedModel{}, err
	}
	if !model.Enabled {
		return ResolvedPublishedModel{}, sql.ErrNoRows
	}
	resolved := ResolvedPublishedModel{
		PublishedModel:     model,
		RoutingProfileName: DefaultRoutingProfileName,
		Targets:            []ResolvedRouteSiteTarget{},
	}
	targetQuery := `SELECT t.id,t.published_model_id,t.site_id,s.name,t.endpoint_id,e.name,e.wire_protocol,
t.site_model_id,m.model_name,t.position,t.enabled,t.revision,t.created_at,t.updated_at,
e.base_url,e.compatibility_profile,e.auth_scheme,e.custom_headers_json
FROM route_site_targets t
JOIN sites s ON s.id=t.site_id
JOIN inference_endpoints e ON e.id=t.endpoint_id
JOIN site_models m ON m.id=t.site_model_id`
	targetArgs := []any{model.ID}
	profileApplied := false
	if profileID != nil && *profileID > 0 {
		var profileName string
		profileErr := tx.QueryRowContext(ctx, `SELECT name FROM routing_profiles WHERE id=?`, *profileID).Scan(&profileName)
		if profileErr != nil && !errors.Is(profileErr, sql.ErrNoRows) {
			return ResolvedPublishedModel{}, profileErr
		}
		if profileErr == nil {
			var hasOverride int
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM routing_profile_model_targets WHERE routing_profile_id=? AND published_model_id=?)`, *profileID, model.ID).Scan(&hasOverride); err != nil {
				return ResolvedPublishedModel{}, err
			}
			if hasOverride == 1 {
				profileApplied = true
				idCopy := *profileID
				resolved.RoutingProfileID = &idCopy
				resolved.RoutingProfileName = profileName
				targetQuery += `
JOIN routing_profile_model_targets rpt ON rpt.route_site_target_id=t.id
WHERE rpt.routing_profile_id=? AND rpt.published_model_id=? AND t.enabled=1 AND s.enabled=1 AND e.enabled=1 AND m.enabled=1
  AND lower(e.wire_protocol) IN ('openai','compatible','openai_chat_completions','openai_responses')
ORDER BY rpt.position,t.id`
				targetArgs = []any{*profileID, model.ID}
			}
		}
	}
	if !profileApplied {
		targetQuery += `
WHERE t.published_model_id=? AND t.enabled=1 AND s.enabled=1 AND e.enabled=1 AND m.enabled=1
  AND lower(e.wire_protocol) IN ('openai','compatible','openai_chat_completions','openai_responses')
ORDER BY t.position,t.id`
	}
	rows, err := tx.QueryContext(ctx, targetQuery, targetArgs...)
	if err != nil {
		return ResolvedPublishedModel{}, err
	}
	byID := make(map[int64]int)
	for rows.Next() {
		var target ResolvedRouteSiteTarget
		var enabled int
		var headers []byte
		if err := rows.Scan(&target.ID, &target.PublishedModelID, &target.SiteID, &target.SiteName,
			&target.EndpointID, &target.EndpointName, &target.WireProtocol, &target.SiteModelID, &target.SourceModel,
			&target.Position, &enabled, &target.Revision, &target.CreatedAt, &target.UpdatedAt,
			&target.BaseURL, &target.CompatibilityProfile, &target.AuthScheme, &headers); err != nil {
			rows.Close()
			return ResolvedPublishedModel{}, err
		}
		target.Enabled = enabled == 1
		target.CustomHeaders = headers
		target.Credentials = []InferenceCredentialSecret{}
		byID[target.ID] = len(resolved.Targets)
		resolved.Targets = append(resolved.Targets, target)
	}
	if err := rows.Close(); err != nil {
		return ResolvedPublishedModel{}, err
	}
	if len(resolved.Targets) == 0 {
		return resolved, nil
	}
	credentialRows, err := tx.QueryContext(ctx, `SELECT t.id,c.id,c.site_id,c.name,CASE WHEN length(c.secret_cipher)>0 THEN 1 ELSE 0 END,
c.position,c.enabled,c.runtime_state,c.cooldown_until,c.last_test_at,c.last_test_status,c.last_error_message,
c.revision,c.created_at,c.updated_at,c.secret_cipher
FROM route_site_targets t
JOIN inference_credentials c ON c.site_id=t.site_id
LEFT JOIN credential_model_access a ON a.credential_id=c.id AND a.site_model_id=t.site_model_id
WHERE t.published_model_id=? AND c.enabled=1
  AND (c.runtime_state='active' OR (c.runtime_state='rate_limited' AND c.cooldown_until IS NOT NULL AND c.cooldown_until<=?))
  AND COALESCE(a.availability,'unknown')!='unsupported'
ORDER BY t.position,t.id,c.position,c.id`, model.ID, nowMS)
	if err != nil {
		return ResolvedPublishedModel{}, err
	}
	for credentialRows.Next() {
		var targetID int64
		var credential InferenceCredentialSecret
		var configured, enabled int
		var cooldown, lastTest sql.NullInt64
		var lastStatus, lastError sql.NullString
		if err := credentialRows.Scan(&targetID, &credential.ID, &credential.SiteID, &credential.Name, &configured,
			&credential.Position, &enabled, &credential.RuntimeState, &cooldown, &lastTest, &lastStatus, &lastError,
			&credential.Revision, &credential.CreatedAt, &credential.UpdatedAt, &credential.SecretCipher); err != nil {
			return ResolvedPublishedModel{}, err
		}
		applyCredentialNulls(&credential.InferenceCredential, configured, enabled, cooldown, lastTest, lastStatus, lastError)
		if index, ok := byID[targetID]; ok {
			resolved.Targets[index].Credentials = append(resolved.Targets[index].Credentials, credential)
		}
	}
	if err := credentialRows.Err(); err != nil {
		credentialRows.Close()
		return ResolvedPublishedModel{}, err
	}
	if err := credentialRows.Close(); err != nil {
		return ResolvedPublishedModel{}, err
	}
	if err := tx.Commit(); err != nil {
		return ResolvedPublishedModel{}, err
	}
	return resolved, nil
}

func (s *Store) UpdatePublishedModel(ctx context.Context, id, expectedRevision int64, input PublishedModelWrite) error {
	current, err := s.GetPublishedModel(ctx, id)
	if err != nil {
		return err
	}
	input = preservePublishedModelRuntimeSettings(input, current)
	input, err = normalizePublishedModelWrite(input)
	if err != nil {
		return err
	}
	query := `UPDATE published_models SET public_name=?,display_name=?,official_price_sku=?,enabled=?,monitor_enabled=?,monitor_interval_seconds=?,cooldown_seconds=?,
failure_threshold=?,failure_window_seconds=?,first_output_timeout_seconds=?,stream_idle_timeout_seconds=?,request_deadline_seconds=?,max_attempts=?,
revision=revision+1,updated_at=? WHERE id=?`
	args := []any{input.PublicName, nullableString(input.DisplayName), nullableString(input.OfficialPriceSKU),
		boolInt(input.Enabled), boolInt(input.MonitorEnabled), input.MonitorIntervalSeconds, input.CooldownSeconds,
		input.FailureThreshold, input.FailureWindowSeconds, input.FirstOutputTimeoutSeconds, input.StreamIdleTimeoutSeconds,
		input.RequestDeadlineSeconds, input.MaxAttempts, NowMS(), id}
	if expectedRevision > 0 {
		query += " AND revision=?"
		args = append(args, expectedRevision)
	}
	result, err := s.DB.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	return revisionResult(ctx, s.DB, result, "published_models", id)
}

func (s *Store) DeletePublishedModel(ctx context.Context, id, expectedRevision int64) error {
	query := "DELETE FROM published_models WHERE id=?"
	args := []any{id}
	if expectedRevision > 0 {
		query += " AND revision=?"
		args = append(args, expectedRevision)
	}
	result, err := s.DB.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	return revisionResult(ctx, s.DB, result, "published_models", id)
}

func (s *Store) CreateRouteSiteTarget(ctx context.Context, publishedModelID, expectedPublishedRevision int64, input RouteSiteTargetWrite) (int64, error) {
	if input.SiteID <= 0 || input.EndpointID <= 0 || input.SiteModelID <= 0 {
		return 0, errors.New("site, endpoint, and site model are required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := NowMS()
	if err := validateRouteSiteTargetTx(ctx, tx, input); err != nil {
		return 0, err
	}
	if err := bumpPublishedModelRevisionTx(ctx, tx, publishedModelID, expectedPublishedRevision, now); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO route_site_targets(
published_model_id,site_id,endpoint_id,site_model_id,position,enabled,revision,created_at,updated_at)
SELECT ?,?,?,?,COALESCE(MAX(position)+1,0),?,1,?,? FROM route_site_targets WHERE published_model_id=?`,
		publishedModelID, input.SiteID, input.EndpointID, input.SiteModelID, boolInt(input.Enabled), now, now, publishedModelID)
	if err != nil {
		return 0, err
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

func (s *Store) GetRouteSiteTarget(ctx context.Context, id int64) (RouteSiteTarget, error) {
	return scanRouteSiteTarget(s.DB.QueryRowContext(ctx, routeSiteTargetSelect+` WHERE t.id=?`, id))
}

func (s *Store) ListRouteSiteTargets(ctx context.Context, publishedModelID int64) ([]RouteSiteTarget, error) {
	rows, err := s.DB.QueryContext(ctx, routeSiteTargetSelect+` WHERE t.published_model_id=? ORDER BY t.position,t.id`, publishedModelID)
	if err != nil {
		return nil, err
	}
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

func (s *Store) UpdateRouteSiteTarget(ctx context.Context, id, expectedTargetRevision, expectedPublishedRevision int64, input RouteSiteTargetWrite) error {
	if input.SiteID <= 0 || input.EndpointID <= 0 || input.SiteModelID <= 0 {
		return errors.New("site, endpoint, and site model are required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var publishedModelID int64
	if err := tx.QueryRowContext(ctx, "SELECT published_model_id FROM route_site_targets WHERE id=?", id).Scan(&publishedModelID); err != nil {
		return err
	}
	if err := validateRouteSiteTargetTx(ctx, tx, input); err != nil {
		return err
	}
	now := NowMS()
	if err := bumpPublishedModelRevisionTx(ctx, tx, publishedModelID, expectedPublishedRevision, now); err != nil {
		return err
	}
	query := `UPDATE route_site_targets SET site_id=?,endpoint_id=?,site_model_id=?,enabled=?,revision=revision+1,updated_at=? WHERE id=?`
	args := []any{input.SiteID, input.EndpointID, input.SiteModelID, boolInt(input.Enabled), now, id}
	if expectedTargetRevision > 0 {
		query += " AND revision=?"
		args = append(args, expectedTargetRevision)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if err := revisionResult(ctx, tx, result, "route_site_targets", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteRouteSiteTarget(ctx context.Context, id, expectedTargetRevision, expectedPublishedRevision int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var publishedModelID int64
	if err := tx.QueryRowContext(ctx, "SELECT published_model_id FROM route_site_targets WHERE id=?", id).Scan(&publishedModelID); err != nil {
		return err
	}
	now := NowMS()
	if err := bumpPublishedModelRevisionTx(ctx, tx, publishedModelID, expectedPublishedRevision, now); err != nil {
		return err
	}
	query := "DELETE FROM route_site_targets WHERE id=?"
	args := []any{id}
	if expectedTargetRevision > 0 {
		query += " AND revision=?"
		args = append(args, expectedTargetRevision)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if err := revisionResult(ctx, tx, result, "route_site_targets", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ReorderRouteSiteTargets(ctx context.Context, publishedModelID, expectedPublishedRevision int64, ids []int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := NowMS()
	if err := bumpPublishedModelRevisionTx(ctx, tx, publishedModelID, expectedPublishedRevision, now); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM route_site_targets WHERE published_model_id=?", publishedModelID).Scan(&count); err != nil {
		return err
	}
	if count != len(ids) {
		return errors.New("target IDs must contain every site target exactly once")
	}
	seen := make(map[int64]struct{}, len(ids))
	for position, id := range ids {
		if _, exists := seen[id]; exists {
			return fmt.Errorf("duplicate target id %d", id)
		}
		seen[id] = struct{}{}
		result, err := tx.ExecContext(ctx, `UPDATE route_site_targets SET position=?,revision=revision+1,updated_at=?
WHERE id=? AND published_model_id=?`, position, now, id, publishedModelID)
		if err != nil {
			return err
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return fmt.Errorf("target %d does not belong to published model", id)
		}
	}
	return tx.Commit()
}

func normalizePublishedModelWrite(input PublishedModelWrite) (PublishedModelWrite, error) {
	input.PublicName = strings.TrimSpace(input.PublicName)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.OfficialPriceSKU = strings.TrimSpace(input.OfficialPriceSKU)
	if input.PublicName == "" {
		return PublishedModelWrite{}, errors.New("public model name is required")
	}
	if input.MonitorIntervalSeconds <= 0 {
		input.MonitorIntervalSeconds = 300
	}
	if input.CooldownSeconds <= 0 {
		input.CooldownSeconds = 300
	}
	if input.FailureThreshold <= 0 {
		input.FailureThreshold = 2
	}
	if input.FailureWindowSeconds <= 0 {
		input.FailureWindowSeconds = 300
	}
	if input.FirstOutputTimeoutSeconds <= 0 {
		input.FirstOutputTimeoutSeconds = 30
	}
	if input.StreamIdleTimeoutSeconds <= 0 {
		input.StreamIdleTimeoutSeconds = 60
	}
	if input.RequestDeadlineSeconds <= 0 {
		input.RequestDeadlineSeconds = 120
	}
	if input.MaxAttempts <= 0 {
		input.MaxAttempts = 3
	}
	input.MonitorIntervalSeconds = clamp(input.MonitorIntervalSeconds, 30, 86400)
	input.CooldownSeconds = clamp(input.CooldownSeconds, 1, 86400)
	input.FailureThreshold = clamp(input.FailureThreshold, 2, 10)
	input.FailureWindowSeconds = clamp(input.FailureWindowSeconds, 1, 86400)
	input.FirstOutputTimeoutSeconds = clamp(input.FirstOutputTimeoutSeconds, 1, 600)
	input.StreamIdleTimeoutSeconds = clamp(input.StreamIdleTimeoutSeconds, 1, 3600)
	input.RequestDeadlineSeconds = clamp(input.RequestDeadlineSeconds, 1, 3600)
	input.MaxAttempts = clamp(input.MaxAttempts, 1, 20)
	return input, nil
}

func validateRouteSiteTargetTx(ctx context.Context, tx *sql.Tx, input RouteSiteTargetWrite) error {
	var protocol string
	err := tx.QueryRowContext(ctx, `SELECT e.wire_protocol
FROM sites s
JOIN inference_endpoints e ON e.site_id=s.id
JOIN site_models m ON m.site_id=s.id AND m.endpoint_id=e.id
WHERE s.id=? AND e.id=? AND m.id=?`, input.SiteID, input.EndpointID, input.SiteModelID).Scan(&protocol)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("site, endpoint, and site model must belong to the same site and endpoint")
	}
	if err != nil {
		return err
	}
	if !inferenceprotocol.For(protocol).RouteEligible {
		return fmt.Errorf("endpoint protocol %s supports model discovery only; OpenAI gateway routes require openai or compatible", protocol)
	}
	return nil
}

func preservePublishedModelRuntimeSettings(input PublishedModelWrite, current PublishedModel) PublishedModelWrite {
	if input.MonitorIntervalSeconds <= 0 {
		input.MonitorIntervalSeconds = current.MonitorIntervalSeconds
	}
	if input.CooldownSeconds <= 0 {
		input.CooldownSeconds = current.CooldownSeconds
	}
	if input.FailureThreshold <= 0 {
		input.FailureThreshold = current.FailureThreshold
	}
	if input.FailureWindowSeconds <= 0 {
		input.FailureWindowSeconds = current.FailureWindowSeconds
	}
	if input.FirstOutputTimeoutSeconds <= 0 {
		input.FirstOutputTimeoutSeconds = current.FirstOutputTimeoutSeconds
	}
	if input.StreamIdleTimeoutSeconds <= 0 {
		input.StreamIdleTimeoutSeconds = current.StreamIdleTimeoutSeconds
	}
	if input.RequestDeadlineSeconds <= 0 {
		input.RequestDeadlineSeconds = current.RequestDeadlineSeconds
	}
	if input.MaxAttempts <= 0 {
		input.MaxAttempts = current.MaxAttempts
	}
	return input
}

const publishedModelSelect = `SELECT id,public_name,display_name,official_price_sku,enabled,monitor_enabled,
monitor_interval_seconds,cooldown_seconds,failure_threshold,failure_window_seconds,first_output_timeout_seconds,
stream_idle_timeout_seconds,request_deadline_seconds,max_attempts,revision,created_at,updated_at FROM published_models`

func scanPublishedModel(row scanner) (PublishedModel, error) {
	var item PublishedModel
	var display, priceSKU sql.NullString
	var enabled, monitor int
	err := row.Scan(&item.ID, &item.PublicName, &display, &priceSKU, &enabled, &monitor,
		&item.MonitorIntervalSeconds, &item.CooldownSeconds, &item.FailureThreshold, &item.FailureWindowSeconds,
		&item.FirstOutputTimeoutSeconds, &item.StreamIdleTimeoutSeconds, &item.RequestDeadlineSeconds, &item.MaxAttempts,
		&item.Revision, &item.CreatedAt, &item.UpdatedAt)
	item.DisplayName = display.String
	item.OfficialPriceSKU = priceSKU.String
	item.Enabled = enabled == 1
	item.MonitorEnabled = monitor == 1
	return item, err
}

const routeSiteTargetSelect = `SELECT t.id,t.published_model_id,t.site_id,s.name,t.endpoint_id,e.name,e.wire_protocol,
t.site_model_id,m.model_name,t.position,t.enabled,t.revision,t.created_at,t.updated_at
FROM route_site_targets t
JOIN sites s ON s.id=t.site_id
JOIN inference_endpoints e ON e.id=t.endpoint_id
JOIN site_models m ON m.id=t.site_model_id`

func scanRouteSiteTarget(row scanner) (RouteSiteTarget, error) {
	var item RouteSiteTarget
	var enabled int
	err := row.Scan(&item.ID, &item.PublishedModelID, &item.SiteID, &item.SiteName, &item.EndpointID,
		&item.EndpointName, &item.WireProtocol, &item.SiteModelID, &item.SourceModel, &item.Position,
		&enabled, &item.Revision, &item.CreatedAt, &item.UpdatedAt)
	item.Enabled = enabled == 1
	return item, err
}

func bumpPublishedModelRevisionTx(ctx context.Context, tx *sql.Tx, publishedModelID, expectedRevision, now int64) error {
	query := "UPDATE published_models SET revision=revision+1,updated_at=? WHERE id=?"
	args := []any{now, publishedModelID}
	if expectedRevision > 0 {
		query += " AND revision=?"
		args = append(args, expectedRevision)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	return revisionResult(ctx, tx, result, "published_models", publishedModelID)
}

func bumpPublishedModelsForSiteModelTx(ctx context.Context, tx *sql.Tx, siteModelID, now int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE published_models SET revision=revision+1,updated_at=?
WHERE id IN (SELECT published_model_id FROM route_site_targets WHERE site_model_id=?)`, now, siteModelID)
	return err
}

func bumpPublishedModelsForSiteTx(ctx context.Context, tx *sql.Tx, siteID, now int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE published_models SET revision=revision+1,updated_at=?
WHERE id IN (SELECT published_model_id FROM route_site_targets WHERE site_id=?)`, now, siteID)
	return err
}

func bumpPublishedModelsForEndpointTx(ctx context.Context, tx *sql.Tx, endpointID, now int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE published_models SET revision=revision+1,updated_at=?
WHERE id IN (SELECT published_model_id FROM route_site_targets WHERE endpoint_id=?)`, now, endpointID)
	return err
}
