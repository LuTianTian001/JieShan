package store

import (
	"context"
	"database/sql"
	"fmt"
)

type RouteWrite struct {
	PublicModel            string
	DisplayName            string
	Enabled                bool
	MonitorEnabled         bool
	MonitorIntervalSeconds int
	CooldownSeconds        int
	FailureThreshold       int
	FailureWindowSeconds   int
	TargetModelIDs         []int64
}

type RouteTargetSpec struct {
	UpstreamID  int64
	SourceModel string
}

func (s *Store) ResolveRouteTargetModels(ctx context.Context, specs []RouteTargetSpec) ([]int64, error) {
	ids := make([]int64, 0, len(specs))
	for _, spec := range specs {
		var id int64
		if err := s.DB.QueryRowContext(ctx, `SELECT id FROM upstream_models WHERE upstream_id=? AND model_name=? AND enabled=1`, spec.UpstreamID, spec.SourceModel).Scan(&id); err != nil {
			return nil, fmt.Errorf("resolve target %d/%s: %w", spec.UpstreamID, spec.SourceModel, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (s *Store) ListRoutes(ctx context.Context) ([]Route, error) {
	rows, err := s.DB.QueryContext(ctx, routeSelect+` ORDER BY r.public_model COLLATE NOCASE,r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Route, 0)
	for rows.Next() {
		item, err := scanRoute(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range items {
		targets, err := s.ListRouteTargets(ctx, items[i].ID)
		if err != nil {
			return nil, err
		}
		items[i].Targets = targets
	}
	return items, nil
}

func (s *Store) GetRoute(ctx context.Context, id int64) (Route, error) {
	item, err := scanRoute(s.DB.QueryRowContext(ctx, routeSelect+` WHERE r.id=?`, id))
	if err != nil {
		return Route{}, err
	}
	item.Targets, err = s.ListRouteTargets(ctx, id)
	return item, err
}

func (s *Store) RouteByPublicModel(ctx context.Context, model string) (Route, error) {
	item, err := scanRoute(s.DB.QueryRowContext(ctx, routeSelect+` WHERE r.public_model=? AND r.enabled=1`, model))
	if err != nil {
		return Route{}, err
	}
	item.Targets, err = s.ListRouteTargets(ctx, item.ID)
	return item, err
}

func (s *Store) CreateRoute(ctx context.Context, input RouteWrite) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := NowMS()
	result, err := tx.ExecContext(ctx, `INSERT INTO routes(public_model,display_name,enabled,monitor_enabled,monitor_interval_seconds,cooldown_seconds,failure_threshold,failure_window_seconds,revision,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,1,?,?)`, input.PublicModel, nullableString(input.DisplayName), boolInt(input.Enabled), boolInt(input.MonitorEnabled), input.MonitorIntervalSeconds,
		input.CooldownSeconds, input.FailureThreshold, input.FailureWindowSeconds, now, now)
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := replaceTargetsTx(ctx, tx, id, input.TargetModelIDs, now); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) UpdateRoute(ctx context.Context, id int64, input RouteWrite, replaceTargets bool) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := NowMS()
	result, err := tx.ExecContext(ctx, `UPDATE routes SET public_model=?,display_name=?,enabled=?,monitor_enabled=?,monitor_interval_seconds=?,cooldown_seconds=?,failure_threshold=?,failure_window_seconds=?,revision=revision+1,updated_at=? WHERE id=?`,
		input.PublicModel, nullableString(input.DisplayName), boolInt(input.Enabled), boolInt(input.MonitorEnabled), input.MonitorIntervalSeconds,
		input.CooldownSeconds, input.FailureThreshold, input.FailureWindowSeconds, now, id)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return sql.ErrNoRows
	}
	if replaceTargets {
		if _, err := tx.ExecContext(ctx, "DELETE FROM route_targets WHERE route_id=?", id); err != nil {
			return err
		}
		if err := replaceTargetsTx(ctx, tx, id, input.TargetModelIDs, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteRoute(ctx context.Context, id int64) error {
	result, err := s.DB.ExecContext(ctx, "DELETE FROM routes WHERE id=?", id)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ReorderRouteTargets(ctx context.Context, routeID int64, ids []int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM route_targets WHERE route_id=?", routeID).Scan(&count); err != nil {
		return err
	}
	if count != len(ids) {
		return fmt.Errorf("targetIds must contain every route target exactly once")
	}
	seen := make(map[int64]struct{}, len(ids))
	now := NowMS()
	for position, id := range ids {
		if _, exists := seen[id]; exists {
			return fmt.Errorf("duplicate target id %d", id)
		}
		seen[id] = struct{}{}
		result, err := tx.ExecContext(ctx, "UPDATE route_targets SET position=?,updated_at=? WHERE id=? AND route_id=?", position, now, id, routeID)
		if err != nil {
			return err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return fmt.Errorf("target %d does not belong to route", id)
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE routes SET revision=revision+1,updated_at=? WHERE id=?", now, routeID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListRouteTargets(ctx context.Context, routeID int64) ([]RouteTarget, error) {
	rows, err := s.DB.QueryContext(ctx, targetSelect+` WHERE t.route_id=? ORDER BY t.position,t.id`, routeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]RouteTarget, 0)
	for rows.Next() {
		item, err := scanTarget(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func replaceTargetsTx(ctx context.Context, tx *sql.Tx, routeID int64, modelIDs []int64, now int64) error {
	seen := make(map[int64]struct{}, len(modelIDs))
	for position, modelID := range modelIDs {
		if _, exists := seen[modelID]; exists {
			continue
		}
		seen[modelID] = struct{}{}
		var endpointID, credentialID int64
		var kind string
		err := tx.QueryRowContext(ctx, `SELECT
(SELECT id FROM upstream_endpoints WHERE upstream_id=m.upstream_id AND enabled=1 ORDER BY position,id LIMIT 1),
(SELECT id FROM upstream_credentials WHERE upstream_id=m.upstream_id AND enabled=1 ORDER BY id LIMIT 1),
u.kind
FROM upstream_models m JOIN upstreams u ON u.id=m.upstream_id WHERE m.id=?`, modelID).Scan(&endpointID, &credentialID, &kind)
		if err != nil {
			return fmt.Errorf("resolve upstream model %d: %w", modelID, err)
		}
		if kind != "openai" && kind != "compatible" {
			return fmt.Errorf("upstream model %d uses %s; V1 routes support only openai and compatible protocols", modelID, kind)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO route_targets(route_id,upstream_model_id,endpoint_id,credential_id,position,enabled,created_at,updated_at)
VALUES (?,?,?,?,?,1,?,?)`, routeID, modelID, endpointID, credentialID, position, now, now); err != nil {
			return err
		}
	}
	return nil
}

const routeSelect = `SELECT r.id,r.public_model,COALESCE(r.display_name,''),r.enabled,r.monitor_enabled,r.monitor_interval_seconds,
r.cooldown_seconds,r.failure_threshold,r.failure_window_seconds,r.revision,r.created_at,r.updated_at FROM routes r`

func scanRoute(row scanner) (Route, error) {
	var item Route
	var enabled, monitor int
	err := row.Scan(&item.ID, &item.PublicModel, &item.DisplayName, &enabled, &monitor, &item.MonitorIntervalSeconds,
		&item.CooldownSeconds, &item.FailureThreshold, &item.FailureWindowSeconds,
		&item.Revision, &item.CreatedAt, &item.UpdatedAt)
	item.Enabled = enabled == 1
	item.MonitorEnabled = monitor == 1
	return item, err
}

const targetSelect = `SELECT t.id,t.route_id,u.id,u.name,u.kind,m.id,
COALESCE(t.upstream_model_override,m.model_name),e.base_url,e.id,c.id,t.position,
CASE WHEN t.enabled=1 AND u.enabled=1 AND e.enabled=1 AND c.enabled=1 AND m.enabled=1 THEN 1 ELSE 0 END,
COALESCE(h.circuit_phase,'closed'),COALESCE(h.consecutive_failures,0),h.cooldown_until,
COALESCE(h.capability_state,'unknown'),
COALESCE((SELECT status FROM probe_results p WHERE p.target_id=t.id ORDER BY checked_at DESC,id DESC LIMIT 1),''),
(SELECT latency_ms FROM probe_results p WHERE p.target_id=t.id ORDER BY checked_at DESC,id DESC LIMIT 1),
(SELECT checked_at FROM probe_results p WHERE p.target_id=t.id ORDER BY checked_at DESC,id DESC LIMIT 1),
c.secret_cipher,u.custom_headers_json,c.runtime_state,c.name,COALESCE(h.last_error_message,''),r.cooldown_seconds,r.failure_threshold,r.failure_window_seconds
FROM route_targets t
JOIN routes r ON r.id=t.route_id
JOIN upstream_models m ON m.id=t.upstream_model_id
JOIN upstreams u ON u.id=m.upstream_id
JOIN upstream_endpoints e ON e.id=t.endpoint_id
JOIN upstream_credentials c ON c.id=t.credential_id
LEFT JOIN target_health h ON h.target_id=t.id`

func scanTarget(row scanner) (RouteTarget, error) {
	var item RouteTarget
	var enabled int
	var cooldown, probeLatency, probeAt sql.NullInt64
	err := row.Scan(&item.ID, &item.RouteID, &item.UpstreamID, &item.UpstreamName, &item.UpstreamKind,
		&item.UpstreamModelID, &item.UpstreamModel, &item.BaseURL, &item.EndpointID,
		&item.CredentialID, &item.Position, &enabled, &item.CircuitPhase,
		&item.ConsecutiveFails, &cooldown, &item.CapabilityState, &item.LastProbeStatus,
		&probeLatency, &probeAt, &item.SecretCipher, &item.CustomHeaders, &item.CredentialState,
		&item.CredentialName, &item.LastErrorMessage,
		&item.CooldownSeconds, &item.FailureThreshold, &item.FailureWindowSeconds)
	if err != nil {
		return RouteTarget{}, err
	}
	item.Enabled = enabled == 1
	if cooldown.Valid {
		value := cooldown.Int64
		item.CooldownUntil = &value
	}
	if probeLatency.Valid {
		value := probeLatency.Int64
		item.LastProbeLatency = &value
	}
	if probeAt.Valid {
		value := probeAt.Int64
		item.LastProbeAt = &value
	}
	return item, nil
}

func (s *Store) ListPublicModels(ctx context.Context) ([]Route, error) {
	rows, err := s.DB.QueryContext(ctx, routeSelect+` WHERE r.enabled=1 ORDER BY r.public_model COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Route, 0)
	for rows.Next() {
		item, err := scanRoute(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
