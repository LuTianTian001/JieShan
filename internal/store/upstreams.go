package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type UpstreamWrite struct {
	Name             string
	Kind             string
	DashboardURL     string
	BaseURL          string
	Enabled          bool
	CustomHeaders    json.RawMessage
	SecretCipher     []byte
	ManagementCipher []byte
}

func (s *Store) CreateUpstream(ctx context.Context, input UpstreamWrite) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := NowMS()
	result, err := tx.ExecContext(ctx, `INSERT INTO upstreams(name, kind, dashboard_url, enabled, custom_headers_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`, input.Name, input.Kind, nullableString(input.DashboardURL), boolInt(input.Enabled), normalizedJSON(input.CustomHeaders), now, now)
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO upstream_endpoints(upstream_id, name, base_url, position, enabled, created_at, updated_at)
VALUES (?, 'Primary', ?, 0, 1, ?, ?)`, id, strings.TrimRight(input.BaseURL, "/"), now, now); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO upstream_credentials(upstream_id, name, secret_cipher, management_cipher, enabled, runtime_state, created_at, updated_at)
VALUES (?, 'Default', ?, ?, 1, 'active', ?, ?)`, id, input.SecretCipher, nullableBytes(input.ManagementCipher), now, now); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) ListUpstreams(ctx context.Context) ([]Upstream, error) {
	rows, err := s.DB.QueryContext(ctx, upstreamSelect+` ORDER BY u.name COLLATE NOCASE, u.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Upstream, 0)
	for rows.Next() {
		item, err := scanUpstream(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetUpstream(ctx context.Context, id int64) (Upstream, error) {
	row := s.DB.QueryRowContext(ctx, upstreamSelect+` WHERE u.id=?`, id)
	return scanUpstream(row)
}

func (s *Store) GetUpstreamSecret(ctx context.Context, id int64) (UpstreamSecret, error) {
	var result UpstreamSecret
	var dashboard, balanceValue, balanceCurrency, subscription sql.NullString
	var lastSync sql.NullInt64
	var enabled int
	var custom []byte
	err := s.DB.QueryRowContext(ctx, `SELECT u.id,u.name,u.kind,u.dashboard_url,u.enabled,u.custom_headers_json,
u.created_at,u.updated_at,e.id,e.base_url,c.id,c.secret_cipher,c.management_cipher,c.runtime_state,
c.balance_value,c.balance_currency,c.subscription_json,c.last_balance_sync_at,
(SELECT COUNT(*) FROM upstream_models m WHERE m.upstream_id=u.id AND m.enabled=1),
(SELECT COUNT(*) FROM upstream_credentials c2 WHERE c2.upstream_id=u.id),
(SELECT COUNT(*) FROM upstream_credentials c2 WHERE c2.upstream_id=u.id AND c2.enabled=1),
(SELECT COUNT(*) FROM upstream_credentials c2 WHERE c2.upstream_id=u.id AND c2.enabled=1 AND c2.runtime_state NOT IN ('active','healthy'))
FROM upstreams u
JOIN upstream_endpoints e ON e.id=(SELECT id FROM upstream_endpoints WHERE upstream_id=u.id AND enabled=1 ORDER BY position,id LIMIT 1)
JOIN upstream_credentials c ON c.id=(SELECT id FROM upstream_credentials WHERE upstream_id=u.id AND enabled=1 ORDER BY id LIMIT 1)
WHERE u.id=?`, id).Scan(
		&result.ID, &result.Name, &result.Kind, &dashboard, &enabled, &custom,
		&result.CreatedAt, &result.UpdatedAt, &result.EndpointID, &result.BaseURL,
		&result.CredentialID, &result.SecretCipher, &result.ManagementCipher, &result.CredentialState,
		&balanceValue, &balanceCurrency, &subscription, &lastSync, &result.ModelCount,
		&result.CredentialCount, &result.EnabledCredentialCount, &result.UnavailableCredentialCount,
	)
	if err != nil {
		return UpstreamSecret{}, err
	}
	result.DashboardURL = dashboard.String
	result.Enabled = enabled == 1
	result.CustomHeaders = json.RawMessage(custom)
	result.CredentialConfigured = len(result.SecretCipher) > 0
	result.BalanceValue = balanceValue.String
	result.BalanceCurrency = balanceCurrency.String
	if subscription.Valid {
		result.Subscription = json.RawMessage(subscription.String)
	}
	if lastSync.Valid {
		value := lastSync.Int64
		result.LastBalanceSyncAt = &value
	}
	return result, nil
}

func (s *Store) UpdateUpstream(ctx context.Context, id int64, input UpstreamWrite, replaceSecret, replaceManagement bool) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := NowMS()
	result, err := tx.ExecContext(ctx, `UPDATE upstreams SET name=?,kind=?,dashboard_url=?,enabled=?,custom_headers_json=?,updated_at=? WHERE id=?`,
		input.Name, input.Kind, nullableString(input.DashboardURL), boolInt(input.Enabled), normalizedJSON(input.CustomHeaders), now, id)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return sql.ErrNoRows
	}
	if _, err = tx.ExecContext(ctx, `UPDATE upstream_endpoints SET base_url=?,updated_at=? WHERE id=(SELECT id FROM upstream_endpoints WHERE upstream_id=? ORDER BY position,id LIMIT 1)`,
		strings.TrimRight(input.BaseURL, "/"), now, id); err != nil {
		return err
	}
	if replaceSecret {
		if _, err = tx.ExecContext(ctx, `UPDATE upstream_credentials SET secret_cipher=?,runtime_state='active',updated_at=? WHERE id=(SELECT id FROM upstream_credentials WHERE upstream_id=? ORDER BY id LIMIT 1)`, input.SecretCipher, now, id); err != nil {
			return err
		}
	}
	if replaceManagement {
		if _, err = tx.ExecContext(ctx, `UPDATE upstream_credentials SET management_cipher=?,updated_at=? WHERE id=(SELECT id FROM upstream_credentials WHERE upstream_id=? ORDER BY id LIMIT 1)`, nullableBytes(input.ManagementCipher), now, id); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE target_health SET capability_state='unknown',circuit_phase='closed',consecutive_failures=0,cooldown_until=NULL,half_open_lease_until=NULL,updated_at=?
WHERE target_id IN (SELECT t.id FROM route_targets t JOIN upstream_models m ON m.id=t.upstream_model_id WHERE m.upstream_id=?)`, now, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteUpstream(ctx context.Context, id int64) error {
	result, err := s.DB.ExecContext(ctx, "DELETE FROM upstreams WHERE id=?", id)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ListUpstreamModels(ctx context.Context, upstreamID int64) ([]UpstreamModel, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,upstream_id,model_name,enabled,stale,missing_count,last_seen_at
FROM upstream_models WHERE upstream_id=? ORDER BY model_name COLLATE NOCASE`, upstreamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]UpstreamModel, 0)
	for rows.Next() {
		var item UpstreamModel
		var enabled, stale int
		var seen sql.NullInt64
		if err := rows.Scan(&item.ID, &item.UpstreamID, &item.ModelName, &enabled, &stale, &item.MissingCount, &seen); err != nil {
			return nil, err
		}
		item.Enabled = enabled == 1
		item.Stale = stale == 1
		if seen.Valid {
			value := seen.Int64
			item.LastSeenAt = &value
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type ModelApplyResult struct {
	Added    []string `json:"added"`
	Restored []string `json:"restored"`
	Missing  []string `json:"missing"`
	Disabled []string `json:"disabled"`
}

type ModelDiscoveryStage struct {
	Added     []string
	Removed   []string
	Unchanged []string
}

func (s *Store) StageDiscoveredModels(ctx context.Context, upstreamID int64, discovered []string) (ModelDiscoveryStage, error) {
	current, err := s.ListUpstreamModels(ctx, upstreamID)
	if err != nil {
		return ModelDiscoveryStage{}, err
	}
	seen := make(map[string]struct{}, len(discovered))
	for _, name := range discovered {
		name = strings.TrimSpace(name)
		if name != "" {
			seen[name] = struct{}{}
		}
	}
	currentEnabled := make(map[string]struct{}, len(current))
	for _, model := range current {
		if model.Enabled {
			currentEnabled[model.ModelName] = struct{}{}
		}
	}
	stage := ModelDiscoveryStage{}
	for name := range seen {
		if _, exists := currentEnabled[name]; exists {
			stage.Unchanged = append(stage.Unchanged, name)
		} else {
			stage.Added = append(stage.Added, name)
		}
	}
	for name := range currentEnabled {
		if _, exists := seen[name]; !exists {
			stage.Removed = append(stage.Removed, name)
		}
	}
	sort.Strings(stage.Added)
	sort.Strings(stage.Removed)
	sort.Strings(stage.Unchanged)
	return stage, nil
}

func (s *Store) ApplyDiscoveredModels(ctx context.Context, upstreamID int64, discovered []string) (ModelApplyResult, error) {
	unique := make(map[string]struct{}, len(discovered))
	for _, name := range discovered {
		name = strings.TrimSpace(name)
		if name != "" {
			unique[name] = struct{}{}
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ModelApplyResult{}, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT model_name,enabled,missing_count FROM upstream_models WHERE upstream_id=?", upstreamID)
	if err != nil {
		return ModelApplyResult{}, err
	}
	type oldModel struct{ enabled, missing int }
	old := map[string]oldModel{}
	for rows.Next() {
		var name string
		var enabled, missing int
		if err := rows.Scan(&name, &enabled, &missing); err != nil {
			rows.Close()
			return ModelApplyResult{}, err
		}
		old[name] = oldModel{enabled: enabled, missing: missing}
	}
	if err := rows.Close(); err != nil {
		return ModelApplyResult{}, err
	}
	now := NowMS()
	result := ModelApplyResult{}
	for name := range unique {
		previous, exists := old[name]
		if !exists {
			result.Added = append(result.Added, name)
		} else if previous.enabled == 0 {
			result.Restored = append(result.Restored, name)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO upstream_models(upstream_id,model_name,enabled,stale,missing_count,last_seen_at,created_at,updated_at)
VALUES (?,?,1,0,0,?,?,?)
ON CONFLICT(upstream_id,model_name) DO UPDATE SET enabled=1,stale=0,missing_count=0,last_seen_at=excluded.last_seen_at,updated_at=excluded.updated_at`,
			upstreamID, name, now, now, now); err != nil {
			return ModelApplyResult{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE target_health SET capability_state='unknown',circuit_phase='closed',consecutive_failures=0,cooldown_until=NULL,half_open_lease_until=NULL,updated_at=?
WHERE target_id IN (
 SELECT t.id FROM route_targets t JOIN upstream_models m ON m.id=t.upstream_model_id
 WHERE m.upstream_id=? AND m.model_name IN (SELECT model_name FROM upstream_models WHERE upstream_id=? AND stale=0)
)`, now, upstreamID, upstreamID); err != nil {
		return ModelApplyResult{}, err
	}
	var kind string
	if err := tx.QueryRowContext(ctx, "SELECT kind FROM upstreams WHERE id=?", upstreamID).Scan(&kind); err != nil {
		return ModelApplyResult{}, err
	}
	if kind == "openai" || kind == "compatible" {
		var cooldown, threshold, window int
		if err := tx.QueryRowContext(ctx, "SELECT default_cooldown_seconds,failure_threshold,failure_window_seconds FROM app_settings WHERE id=1").Scan(&cooldown, &threshold, &window); err != nil {
			return ModelApplyResult{}, err
		}
		for name := range unique {
			var modelID, endpointID, credentialID int64
			if err := tx.QueryRowContext(ctx, "SELECT id FROM upstream_models WHERE upstream_id=? AND model_name=?", upstreamID, name).Scan(&modelID); err != nil {
				return ModelApplyResult{}, err
			}
			if err := tx.QueryRowContext(ctx, "SELECT id FROM upstream_endpoints WHERE upstream_id=? AND enabled=1 ORDER BY position,id LIMIT 1", upstreamID).Scan(&endpointID); err != nil {
				return ModelApplyResult{}, err
			}
			if err := tx.QueryRowContext(ctx, "SELECT id FROM upstream_credentials WHERE upstream_id=? AND enabled=1 ORDER BY id LIMIT 1", upstreamID).Scan(&credentialID); err != nil {
				return ModelApplyResult{}, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO routes(public_model,display_name,enabled,monitor_enabled,monitor_interval_seconds,cooldown_seconds,failure_threshold,failure_window_seconds,revision,created_at,updated_at)
VALUES (?,?,1,0,300,?,?,?,1,?,?) ON CONFLICT(public_model) DO NOTHING`, name, name, cooldown, threshold, window, now, now); err != nil {
				return ModelApplyResult{}, err
			}
			var routeID int64
			if err := tx.QueryRowContext(ctx, "SELECT id FROM routes WHERE public_model=?", name).Scan(&routeID); err != nil {
				return ModelApplyResult{}, err
			}
			inserted, err := tx.ExecContext(ctx, `INSERT INTO route_targets(route_id,upstream_model_id,endpoint_id,credential_id,position,enabled,created_at,updated_at)
SELECT ?,?,?,?,COALESCE(MAX(position)+1,0),1,?,? FROM route_targets WHERE route_id=?
ON CONFLICT(route_id,upstream_model_id,endpoint_id,credential_id) DO NOTHING`, routeID, modelID, endpointID, credentialID, now, now, routeID)
			if err != nil {
				return ModelApplyResult{}, err
			}
			if changed, _ := inserted.RowsAffected(); changed > 0 {
				_, _ = tx.ExecContext(ctx, "UPDATE routes SET revision=revision+1,updated_at=? WHERE id=?", now, routeID)
			}
		}
	}
	for name, previous := range old {
		if _, exists := unique[name]; exists {
			continue
		}
		result.Missing = append(result.Missing, name)
		nextMissing := previous.missing + 1
		enabled := previous.enabled
		if nextMissing >= 2 {
			enabled = 0
			result.Disabled = append(result.Disabled, name)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE upstream_models SET enabled=?,stale=1,missing_count=?,updated_at=? WHERE upstream_id=? AND model_name=?`, enabled, nextMissing, now, upstreamID, name); err != nil {
			return ModelApplyResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ModelApplyResult{}, err
	}
	return result, nil
}

func (s *Store) SetCredentialRuntimeState(ctx context.Context, credentialID int64, state string) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE upstream_credentials SET runtime_state=?,updated_at=? WHERE id=?", state, NowMS(), credentialID)
	return err
}

const upstreamSelect = `SELECT u.id,u.name,u.kind,u.dashboard_url,u.enabled,u.custom_headers_json,
u.created_at,u.updated_at,
COALESCE(e.id,0),COALESCE(e.base_url,''),COALESCE(c.id,0),
CASE WHEN length(c.secret_cipher)>0 THEN 1 ELSE 0 END,COALESCE(c.runtime_state,'active'),
c.balance_value,c.balance_currency,c.subscription_json,c.last_balance_sync_at,
(SELECT COUNT(*) FROM upstream_models m WHERE m.upstream_id=u.id AND m.enabled=1),
(SELECT COUNT(*) FROM upstream_credentials c2 WHERE c2.upstream_id=u.id),
(SELECT COUNT(*) FROM upstream_credentials c2 WHERE c2.upstream_id=u.id AND c2.enabled=1),
(SELECT COUNT(*) FROM upstream_credentials c2 WHERE c2.upstream_id=u.id AND c2.enabled=1 AND c2.runtime_state NOT IN ('active','healthy'))
FROM upstreams u
LEFT JOIN upstream_endpoints e ON e.id=(SELECT id FROM upstream_endpoints WHERE upstream_id=u.id ORDER BY enabled DESC,position,id LIMIT 1)
LEFT JOIN upstream_credentials c ON c.id=(SELECT id FROM upstream_credentials WHERE upstream_id=u.id ORDER BY enabled DESC,id LIMIT 1)`

type scanner interface{ Scan(...any) error }

func scanUpstream(row scanner) (Upstream, error) {
	var item Upstream
	var dashboard, balanceValue, balanceCurrency, subscription sql.NullString
	var lastSync sql.NullInt64
	var enabled, configured int
	var custom []byte
	err := row.Scan(
		&item.ID, &item.Name, &item.Kind, &dashboard, &enabled, &custom,
		&item.CreatedAt, &item.UpdatedAt, &item.EndpointID, &item.BaseURL,
		&item.CredentialID, &configured, &item.CredentialState,
		&balanceValue, &balanceCurrency, &subscription, &lastSync, &item.ModelCount,
		&item.CredentialCount, &item.EnabledCredentialCount, &item.UnavailableCredentialCount,
	)
	if err != nil {
		return Upstream{}, err
	}
	item.DashboardURL = dashboard.String
	item.Enabled = enabled == 1
	item.CredentialConfigured = configured == 1
	item.CustomHeaders = json.RawMessage(custom)
	item.BalanceValue = balanceValue.String
	item.BalanceCurrency = balanceCurrency.String
	if subscription.Valid {
		item.Subscription = json.RawMessage(subscription.String)
	}
	if lastSync.Valid {
		value := lastSync.Int64
		item.LastBalanceSyncAt = &value
	}
	return item, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func normalizedJSON(value []byte) string {
	if len(value) == 0 || !json.Valid(value) {
		return "{}"
	}
	return string(value)
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func NormalizeKind(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "openai":
		return "openai", nil
	case "compatible", "openai-compatible", "newapi", "new-api", "oneapi", "one-api", "sub2api":
		return "compatible", nil
	case "anthropic":
		return "anthropic", nil
	case "gemini":
		return "gemini", nil
	default:
		return "", fmt.Errorf("unsupported upstream kind %q", value)
	}
}
