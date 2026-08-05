package store

import (
	"context"
	"database/sql"
	"fmt"
)

type migration struct {
	version int
	name    string
	sql     string
}

var migrations = []migration{
	{
		version: 1,
		name:    "initial_v1",
		sql: `
CREATE TABLE admin_users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE admin_sessions (
  token_hash BLOB PRIMARY KEY,
  admin_id INTEGER NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
  expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX admin_sessions_expires_idx ON admin_sessions(expires_at);

CREATE TABLE app_settings (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  default_cooldown_seconds INTEGER NOT NULL DEFAULT 300,
  failure_threshold INTEGER NOT NULL DEFAULT 2,
  failure_window_seconds INTEGER NOT NULL DEFAULT 300,
  probe_interval_seconds INTEGER NOT NULL DEFAULT 300,
  request_deadline_seconds INTEGER NOT NULL DEFAULT 120,
  max_attempts INTEGER NOT NULL DEFAULT 3,
  log_retention_days INTEGER NOT NULL DEFAULT 30,
  updated_at INTEGER NOT NULL
);

CREATE TABLE upstreams (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'openai',
  dashboard_url TEXT,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  custom_headers_json TEXT NOT NULL DEFAULT '{}',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX upstreams_name_unique ON upstreams(name COLLATE NOCASE);

CREATE TABLE upstream_endpoints (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  upstream_id INTEGER NOT NULL REFERENCES upstreams(id) ON DELETE CASCADE,
  name TEXT NOT NULL DEFAULT 'Primary',
  base_url TEXT NOT NULL,
  position INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(upstream_id, base_url)
);
CREATE INDEX upstream_endpoints_order_idx ON upstream_endpoints(upstream_id, position, id);

CREATE TABLE upstream_credentials (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  upstream_id INTEGER NOT NULL REFERENCES upstreams(id) ON DELETE CASCADE,
  name TEXT NOT NULL DEFAULT 'Default',
  secret_cipher BLOB NOT NULL,
  management_cipher BLOB,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  runtime_state TEXT NOT NULL DEFAULT 'active',
  balance_value TEXT,
  balance_currency TEXT,
  subscription_json TEXT,
  last_balance_sync_at INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX upstream_credentials_upstream_idx ON upstream_credentials(upstream_id, enabled, id);

CREATE TABLE upstream_models (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  upstream_id INTEGER NOT NULL REFERENCES upstreams(id) ON DELETE CASCADE,
  model_name TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  stale INTEGER NOT NULL DEFAULT 0 CHECK (stale IN (0,1)),
  missing_count INTEGER NOT NULL DEFAULT 0,
  last_seen_at INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(upstream_id, model_name)
);
CREATE INDEX upstream_models_name_idx ON upstream_models(model_name, enabled);

CREATE TABLE routes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  public_model TEXT NOT NULL UNIQUE,
  display_name TEXT,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  monitor_enabled INTEGER NOT NULL DEFAULT 0 CHECK (monitor_enabled IN (0,1)),
  monitor_interval_seconds INTEGER NOT NULL DEFAULT 300,
  cooldown_seconds INTEGER NOT NULL DEFAULT 300,
  failure_threshold INTEGER NOT NULL DEFAULT 2,
  failure_window_seconds INTEGER NOT NULL DEFAULT 300,
  revision INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE route_targets (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  route_id INTEGER NOT NULL REFERENCES routes(id) ON DELETE CASCADE,
  upstream_model_id INTEGER NOT NULL REFERENCES upstream_models(id) ON DELETE CASCADE,
  endpoint_id INTEGER NOT NULL REFERENCES upstream_endpoints(id) ON DELETE CASCADE,
  credential_id INTEGER NOT NULL REFERENCES upstream_credentials(id) ON DELETE CASCADE,
  upstream_model_override TEXT,
  position INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(route_id, upstream_model_id, endpoint_id, credential_id)
);
CREATE INDEX route_targets_order_idx ON route_targets(route_id, position, id);

CREATE TABLE target_health (
  target_id INTEGER PRIMARY KEY REFERENCES route_targets(id) ON DELETE CASCADE,
  circuit_phase TEXT NOT NULL DEFAULT 'closed',
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  last_failure_at INTEGER,
  last_success_at INTEGER,
  cooldown_until INTEGER,
  half_open_lease_until INTEGER,
  capability_state TEXT NOT NULL DEFAULT 'unknown',
  last_error_class TEXT,
  last_error_message TEXT,
  last_incident_id TEXT,
  updated_at INTEGER NOT NULL
);

CREATE TABLE probe_results (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  route_id INTEGER NOT NULL REFERENCES routes(id) ON DELETE CASCADE,
  target_id INTEGER NOT NULL REFERENCES route_targets(id) ON DELETE CASCADE,
  status TEXT NOT NULL,
  latency_ms INTEGER,
  error_class TEXT,
  error_message TEXT,
  checked_at INTEGER NOT NULL
);
CREATE INDEX probe_results_matrix_idx ON probe_results(route_id, target_id, checked_at DESC);

CREATE TABLE downstream_keys (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  key_prefix TEXT NOT NULL,
  key_hash BLOB NOT NULL UNIQUE,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  quota_micro_usd INTEGER,
  rpm_limit INTEGER NOT NULL DEFAULT 0,
  used_micro_usd INTEGER NOT NULL DEFAULT 0,
  reserved_micro_usd INTEGER NOT NULL DEFAULT 0,
  allowed_models_json TEXT NOT NULL DEFAULT '[]',
  expires_at INTEGER,
  last_used_at INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE downstream_key_rate_windows (
  downstream_key_id INTEGER NOT NULL REFERENCES downstream_keys(id) ON DELETE CASCADE,
  window_start INTEGER NOT NULL,
  request_count INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(downstream_key_id, window_start)
);

CREATE TABLE price_catalog_versions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  version TEXT NOT NULL UNIQUE,
  source TEXT NOT NULL,
  fx_snapshot_json TEXT NOT NULL DEFAULT '{}',
  active INTEGER NOT NULL DEFAULT 0 CHECK (active IN (0,1)),
  created_at INTEGER NOT NULL
);

CREATE TABLE model_prices (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  catalog_version_id INTEGER NOT NULL REFERENCES price_catalog_versions(id) ON DELETE CASCADE,
  model_pattern TEXT NOT NULL,
  input_micro_usd_per_million INTEGER NOT NULL,
  cache_read_micro_usd_per_million INTEGER NOT NULL DEFAULT 0,
  output_micro_usd_per_million INTEGER NOT NULL,
  reasoning_micro_usd_per_million INTEGER NOT NULL DEFAULT 0,
  UNIQUE(catalog_version_id, model_pattern)
);

CREATE TABLE request_logs (
  id TEXT PRIMARY KEY,
  downstream_key_id INTEGER REFERENCES downstream_keys(id) ON DELETE SET NULL,
  route_id INTEGER REFERENCES routes(id) ON DELETE SET NULL,
  route_revision INTEGER,
  requested_model TEXT NOT NULL,
  actual_model TEXT,
  reasoning_effort TEXT,
  thinking_budget INTEGER,
  status TEXT NOT NULL,
  http_status INTEGER,
  is_stream INTEGER NOT NULL DEFAULT 0,
  first_token_ms INTEGER,
  duration_ms INTEGER,
  input_tokens INTEGER,
  cache_read_tokens INTEGER,
  output_tokens INTEGER,
  reasoning_tokens INTEGER,
  cost_micro_usd INTEGER NOT NULL DEFAULT 0,
  price_snapshot_json TEXT,
  error_message TEXT,
  started_at INTEGER NOT NULL,
  finished_at INTEGER
);
CREATE INDEX request_logs_started_idx ON request_logs(started_at DESC);
CREATE INDEX request_logs_key_started_idx ON request_logs(downstream_key_id, started_at DESC);

CREATE TABLE request_attempts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  request_id TEXT NOT NULL REFERENCES request_logs(id) ON DELETE CASCADE,
  attempt_index INTEGER NOT NULL,
  target_id INTEGER REFERENCES route_targets(id) ON DELETE SET NULL,
  upstream_id INTEGER REFERENCES upstreams(id) ON DELETE SET NULL,
  upstream_model TEXT,
  status TEXT NOT NULL,
  http_status INTEGER,
  switch_reason TEXT,
  error_class TEXT,
  error_message TEXT,
  latency_ms INTEGER,
  first_token_ms INTEGER,
  created_at INTEGER NOT NULL,
  UNIQUE(request_id, attempt_index)
);
CREATE INDEX request_attempts_request_idx ON request_attempts(request_id, attempt_index);

CREATE TABLE quota_ledger (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  downstream_key_id INTEGER NOT NULL REFERENCES downstream_keys(id) ON DELETE CASCADE,
  request_id TEXT,
  entry_type TEXT NOT NULL,
  amount_micro_usd INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX quota_ledger_key_idx ON quota_ledger(downstream_key_id, created_at DESC);

CREATE TABLE upstream_usage_records (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  upstream_id INTEGER NOT NULL REFERENCES upstreams(id) ON DELETE CASCADE,
  external_id TEXT,
  model_name TEXT,
  amount_text TEXT,
  currency TEXT,
  raw_json TEXT NOT NULL,
  occurred_at INTEGER,
  synced_at INTEGER NOT NULL,
  UNIQUE(upstream_id, external_id)
);
CREATE INDEX upstream_usage_records_upstream_idx ON upstream_usage_records(upstream_id, occurred_at DESC);
`,
	},
}

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
version INTEGER PRIMARY KEY,
name TEXT NOT NULL,
applied_at INTEGER NOT NULL
)`); err != nil {
		return err
	}
	for _, item := range migrations {
		var exists int
		err := s.DB.QueryRowContext(ctx, "SELECT 1 FROM schema_migrations WHERE version = ?", item.version).Scan(&exists)
		if err == nil {
			continue
		}
		if err != sql.ErrNoRows {
			return err
		}
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, item.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d (%s): %w", item.version, item.name, err)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)", item.version, item.name, NowMS()); err != nil {
			tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO app_settings(id, updated_at) VALUES (1, ?)
ON CONFLICT(id) DO NOTHING`, NowMS())
	return err
}
