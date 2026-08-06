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
	{
		version: 2,
		name:    "upstream_accounts_v2",
		sql: `
CREATE TABLE upstream_accounts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  upstream_id INTEGER NOT NULL UNIQUE REFERENCES upstreams(id) ON DELETE CASCADE,
  adapter_kind TEXT NOT NULL CHECK (length(adapter_kind) > 0),
  api_origin TEXT NOT NULL CHECK (length(api_origin) > 0),
  auth_cipher BLOB NOT NULL CHECK (length(auth_cipher) > 0),
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  capabilities_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(capabilities_json)),
  sync_state TEXT NOT NULL DEFAULT 'pending',
  last_attempt_at INTEGER,
  last_success_at INTEGER,
  last_error_code TEXT,
  last_error_message TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX upstream_accounts_sync_idx ON upstream_accounts(enabled, sync_state, last_attempt_at);

CREATE TABLE upstream_account_snapshots (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  upstream_account_id INTEGER NOT NULL REFERENCES upstream_accounts(id) ON DELETE CASCADE,
  snapshot_json TEXT NOT NULL CHECK (json_valid(snapshot_json)),
  captured_at INTEGER NOT NULL
);
CREATE INDEX upstream_account_snapshots_latest_idx ON upstream_account_snapshots(upstream_account_id, captured_at DESC, id DESC);

CREATE TABLE upstream_account_usage_records (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  upstream_account_id INTEGER NOT NULL REFERENCES upstream_accounts(id) ON DELETE CASCADE,
  dedupe_key TEXT NOT NULL CHECK (length(dedupe_key) > 0),
  external_id TEXT,
  model_name TEXT,
  amount_text TEXT,
  unit TEXT,
  raw_json TEXT NOT NULL CHECK (json_valid(raw_json)),
  occurred_at INTEGER,
  synced_at INTEGER NOT NULL,
  UNIQUE(upstream_account_id, dedupe_key)
);
CREATE INDEX upstream_account_usage_latest_idx ON upstream_account_usage_records(upstream_account_id, id DESC);
CREATE INDEX upstream_account_usage_occurred_idx ON upstream_account_usage_records(upstream_account_id, occurred_at DESC, id DESC);
`,
	},
	{
		version: 3,
		name:    "site_routing_domain_v3",
		sql: `
CREATE TABLE sites (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL CHECK (length(trim(name)) > 0),
  dashboard_url TEXT,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX sites_name_unique ON sites(name COLLATE NOCASE);
CREATE INDEX sites_enabled_name_idx ON sites(enabled, name COLLATE NOCASE, id);

CREATE TABLE inference_endpoints (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
  name TEXT NOT NULL CHECK (length(trim(name)) > 0),
  base_url TEXT NOT NULL CHECK (length(trim(base_url)) > 0),
  wire_protocol TEXT NOT NULL CHECK (length(trim(wire_protocol)) > 0),
  compatibility_profile TEXT NOT NULL DEFAULT 'generic' CHECK (length(trim(compatibility_profile)) > 0),
  auth_scheme TEXT NOT NULL DEFAULT 'bearer' CHECK (length(trim(auth_scheme)) > 0),
  custom_headers_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(custom_headers_json)),
  position INTEGER NOT NULL DEFAULT 0 CHECK (position >= 0),
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(site_id, base_url, wire_protocol),
  UNIQUE(site_id, id)
);
CREATE INDEX inference_endpoints_order_idx ON inference_endpoints(site_id, position, id);
CREATE INDEX inference_endpoints_enabled_idx ON inference_endpoints(site_id, enabled, position, id);

CREATE TABLE inference_credentials (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
  name TEXT NOT NULL CHECK (length(trim(name)) > 0),
  secret_cipher BLOB NOT NULL CHECK (length(secret_cipher) > 0),
  position INTEGER NOT NULL DEFAULT 0 CHECK (position >= 0),
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  runtime_state TEXT NOT NULL DEFAULT 'active' CHECK (runtime_state IN ('active','invalid','exhausted','rate_limited')),
  cooldown_until INTEGER,
  last_test_at INTEGER,
  last_test_status TEXT,
  last_error_message TEXT,
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(site_id, id)
);
CREATE UNIQUE INDEX inference_credentials_name_unique ON inference_credentials(site_id, name COLLATE NOCASE);
CREATE INDEX inference_credentials_order_idx ON inference_credentials(site_id, position, id);
CREATE INDEX inference_credentials_runtime_idx ON inference_credentials(site_id, enabled, runtime_state, cooldown_until, position, id);

CREATE TABLE site_models (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  site_id INTEGER NOT NULL,
  endpoint_id INTEGER NOT NULL,
  model_name TEXT NOT NULL CHECK (length(trim(model_name)) > 0),
  display_name TEXT,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  stale INTEGER NOT NULL DEFAULT 0 CHECK (stale IN (0,1)),
  missing_count INTEGER NOT NULL DEFAULT 0 CHECK (missing_count >= 0),
  last_seen_at INTEGER,
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(endpoint_id, model_name),
  UNIQUE(site_id, id),
  UNIQUE(site_id, endpoint_id, id),
  FOREIGN KEY(site_id, endpoint_id) REFERENCES inference_endpoints(site_id, id) ON DELETE CASCADE
);
CREATE INDEX site_models_site_name_idx ON site_models(site_id, model_name COLLATE NOCASE, id);
CREATE INDEX site_models_endpoint_state_idx ON site_models(endpoint_id, enabled, stale, model_name COLLATE NOCASE, id);

CREATE TABLE credential_model_access (
  site_id INTEGER NOT NULL,
  credential_id INTEGER NOT NULL,
  site_model_id INTEGER NOT NULL,
  availability TEXT NOT NULL DEFAULT 'unknown' CHECK (availability IN ('unknown','supported','unsupported')),
  missing_count INTEGER NOT NULL DEFAULT 0 CHECK (missing_count >= 0),
  last_seen_at INTEGER,
  last_checked_at INTEGER,
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(credential_id, site_model_id),
  FOREIGN KEY(site_id, credential_id) REFERENCES inference_credentials(site_id, id) ON DELETE CASCADE,
  FOREIGN KEY(site_id, site_model_id) REFERENCES site_models(site_id, id) ON DELETE CASCADE
);
CREATE INDEX credential_model_access_model_idx ON credential_model_access(site_model_id, availability, credential_id);
CREATE INDEX credential_model_access_site_idx ON credential_model_access(site_id, availability, credential_id, site_model_id);

CREATE TABLE published_models (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  public_name TEXT NOT NULL UNIQUE CHECK (length(trim(public_name)) > 0),
  display_name TEXT,
  official_price_sku TEXT,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  monitor_enabled INTEGER NOT NULL DEFAULT 0 CHECK (monitor_enabled IN (0,1)),
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX published_models_enabled_name_idx ON published_models(enabled, public_name COLLATE NOCASE, id);
CREATE INDEX published_models_monitor_idx ON published_models(monitor_enabled, enabled, id);

CREATE TABLE route_site_targets (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  published_model_id INTEGER NOT NULL REFERENCES published_models(id) ON DELETE CASCADE,
  site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
  endpoint_id INTEGER NOT NULL,
  site_model_id INTEGER NOT NULL,
  position INTEGER NOT NULL DEFAULT 0 CHECK (position >= 0),
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(published_model_id, site_id),
  FOREIGN KEY(site_id, endpoint_id) REFERENCES inference_endpoints(site_id, id) ON DELETE CASCADE,
  FOREIGN KEY(site_id, endpoint_id, site_model_id) REFERENCES site_models(site_id, endpoint_id, id) ON DELETE CASCADE
);
CREATE INDEX route_site_targets_order_idx ON route_site_targets(published_model_id, position, id);
CREATE INDEX route_site_targets_site_idx ON route_site_targets(site_id, enabled, published_model_id, id);
`,
	},
	{
		version: 4,
		name:    "site_runtime_health_v4",
		sql: `
ALTER TABLE app_settings ADD COLUMN first_output_timeout_seconds INTEGER NOT NULL DEFAULT 30 CHECK (first_output_timeout_seconds BETWEEN 1 AND 600);
ALTER TABLE app_settings ADD COLUMN stream_idle_timeout_seconds INTEGER NOT NULL DEFAULT 60 CHECK (stream_idle_timeout_seconds BETWEEN 1 AND 3600);

ALTER TABLE published_models ADD COLUMN monitor_interval_seconds INTEGER NOT NULL DEFAULT 300 CHECK (monitor_interval_seconds BETWEEN 30 AND 86400);
ALTER TABLE published_models ADD COLUMN cooldown_seconds INTEGER NOT NULL DEFAULT 300 CHECK (cooldown_seconds BETWEEN 1 AND 86400);
ALTER TABLE published_models ADD COLUMN failure_threshold INTEGER NOT NULL DEFAULT 2 CHECK (failure_threshold BETWEEN 1 AND 10);
ALTER TABLE published_models ADD COLUMN failure_window_seconds INTEGER NOT NULL DEFAULT 300 CHECK (failure_window_seconds BETWEEN 1 AND 86400);
ALTER TABLE published_models ADD COLUMN first_output_timeout_seconds INTEGER NOT NULL DEFAULT 30 CHECK (first_output_timeout_seconds BETWEEN 1 AND 600);
ALTER TABLE published_models ADD COLUMN stream_idle_timeout_seconds INTEGER NOT NULL DEFAULT 60 CHECK (stream_idle_timeout_seconds BETWEEN 1 AND 3600);
ALTER TABLE published_models ADD COLUMN request_deadline_seconds INTEGER NOT NULL DEFAULT 120 CHECK (request_deadline_seconds BETWEEN 1 AND 3600);
ALTER TABLE published_models ADD COLUMN max_attempts INTEGER NOT NULL DEFAULT 3 CHECK (max_attempts BETWEEN 1 AND 20);

CREATE TABLE route_site_target_health (
  target_id INTEGER PRIMARY KEY REFERENCES route_site_targets(id) ON DELETE CASCADE,
  circuit_phase TEXT NOT NULL DEFAULT 'closed' CHECK (circuit_phase IN ('closed','open','half_open')),
  consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
  last_failure_at INTEGER,
  last_success_at INTEGER,
  cooldown_until INTEGER,
  half_open_lease_until INTEGER,
  capability_state TEXT NOT NULL DEFAULT 'unknown' CHECK (capability_state IN ('unknown','supported','unsupported')),
  last_error_class TEXT,
  last_error_message TEXT,
  last_incident_id TEXT,
  updated_at INTEGER NOT NULL
);
CREATE INDEX route_site_target_health_runtime_idx ON route_site_target_health(circuit_phase, cooldown_until, half_open_lease_until, target_id);
CREATE INDEX route_site_target_health_capability_idx ON route_site_target_health(capability_state, target_id);

CREATE TABLE probe_runs (
  id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
  published_model_id INTEGER NOT NULL REFERENCES published_models(id) ON DELETE CASCADE,
  published_model_revision INTEGER NOT NULL CHECK (published_model_revision > 0),
  trigger_kind TEXT NOT NULL CHECK (trigger_kind IN ('scheduled','manual','recovery')),
  status TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running','success','partial','failed','cancelled')),
  target_count INTEGER NOT NULL DEFAULT 0 CHECK (target_count >= 0),
  success_count INTEGER NOT NULL DEFAULT 0 CHECK (success_count >= 0),
  failure_count INTEGER NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
  skipped_count INTEGER NOT NULL DEFAULT 0 CHECK (skipped_count >= 0),
  error_message TEXT,
  started_at INTEGER NOT NULL,
  finished_at INTEGER
);
CREATE INDEX probe_runs_model_started_idx ON probe_runs(published_model_id, started_at DESC, id DESC);
CREATE INDEX probe_runs_status_started_idx ON probe_runs(status, started_at, id);

CREATE TABLE probe_attempts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  probe_run_id TEXT NOT NULL REFERENCES probe_runs(id) ON DELETE CASCADE,
  attempt_index INTEGER NOT NULL CHECK (attempt_index >= 0),
  route_site_target_id INTEGER REFERENCES route_site_targets(id) ON DELETE SET NULL,
  site_id INTEGER REFERENCES sites(id) ON DELETE SET NULL,
  endpoint_id INTEGER REFERENCES inference_endpoints(id) ON DELETE SET NULL,
  inference_credential_id INTEGER REFERENCES inference_credentials(id) ON DELETE SET NULL,
  site_model_id INTEGER REFERENCES site_models(id) ON DELETE SET NULL,
  site_name TEXT NOT NULL,
  endpoint_name TEXT NOT NULL,
  credential_name TEXT,
  source_model TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('success','failed','skipped')),
  http_status INTEGER,
  latency_ms INTEGER CHECK (latency_ms IS NULL OR latency_ms >= 0),
  first_output_ms INTEGER CHECK (first_output_ms IS NULL OR first_output_ms >= 0),
  error_class TEXT,
  error_message TEXT,
  started_at INTEGER NOT NULL,
  finished_at INTEGER NOT NULL,
  UNIQUE(probe_run_id, attempt_index)
);
CREATE INDEX probe_attempts_run_idx ON probe_attempts(probe_run_id, attempt_index, id);
CREATE INDEX probe_attempts_target_finished_idx ON probe_attempts(route_site_target_id, finished_at DESC, id DESC);
CREATE INDEX probe_attempts_site_finished_idx ON probe_attempts(site_id, finished_at DESC, id DESC);

CREATE TABLE model_discovery_runs (
  id TEXT PRIMARY KEY CHECK (length(trim(id)) > 0),
  site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
  endpoint_id INTEGER NOT NULL REFERENCES inference_endpoints(id) ON DELETE CASCADE,
  mode TEXT NOT NULL CHECK (mode IN ('selected','first_success','all')),
  status TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running','success','partial','failed','cancelled')),
  base_site_revision INTEGER NOT NULL CHECK (base_site_revision > 0),
  base_endpoint_revision INTEGER NOT NULL CHECK (base_endpoint_revision > 0),
  credential_count INTEGER NOT NULL DEFAULT 0 CHECK (credential_count >= 0),
  success_count INTEGER NOT NULL DEFAULT 0 CHECK (success_count >= 0),
  model_count INTEGER NOT NULL DEFAULT 0 CHECK (model_count >= 0),
  summary_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(summary_json)),
  error_message TEXT,
  started_at INTEGER NOT NULL,
  finished_at INTEGER
);
CREATE INDEX model_discovery_runs_site_started_idx ON model_discovery_runs(site_id, started_at DESC, id DESC);
CREATE INDEX model_discovery_runs_endpoint_started_idx ON model_discovery_runs(endpoint_id, started_at DESC, id DESC);
CREATE INDEX model_discovery_runs_status_started_idx ON model_discovery_runs(status, started_at, id);

CREATE TABLE model_discovery_attempts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  discovery_run_id TEXT NOT NULL REFERENCES model_discovery_runs(id) ON DELETE CASCADE,
  attempt_index INTEGER NOT NULL CHECK (attempt_index >= 0),
  inference_credential_id INTEGER REFERENCES inference_credentials(id) ON DELETE SET NULL,
  credential_name TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('success','failed','skipped')),
  model_count INTEGER NOT NULL DEFAULT 0 CHECK (model_count >= 0),
  complete INTEGER NOT NULL DEFAULT 1 CHECK (complete IN (0,1)),
  pages_fetched INTEGER NOT NULL DEFAULT 0 CHECK (pages_fetched >= 0),
  error_class TEXT,
  error_message TEXT,
  started_at INTEGER NOT NULL,
  finished_at INTEGER NOT NULL,
  UNIQUE(discovery_run_id, attempt_index)
);
CREATE INDEX model_discovery_attempts_run_idx ON model_discovery_attempts(discovery_run_id, attempt_index, id);
CREATE INDEX model_discovery_attempts_credential_finished_idx ON model_discovery_attempts(inference_credential_id, finished_at DESC, id DESC);

ALTER TABLE request_logs ADD COLUMN routing_generation TEXT NOT NULL DEFAULT 'legacy' CHECK (routing_generation IN ('legacy','v3'));
ALTER TABLE request_logs ADD COLUMN api_surface TEXT NOT NULL DEFAULT 'chat_completions' CHECK (api_surface IN ('chat_completions','responses'));
ALTER TABLE request_logs ADD COLUMN published_model_id INTEGER REFERENCES published_models(id) ON DELETE SET NULL;
ALTER TABLE request_logs ADD COLUMN published_model_revision INTEGER;
CREATE INDEX request_logs_published_started_idx ON request_logs(published_model_id, started_at DESC, id DESC);

ALTER TABLE request_attempts ADD COLUMN routing_generation TEXT NOT NULL DEFAULT 'legacy' CHECK (routing_generation IN ('legacy','v3'));
ALTER TABLE request_attempts ADD COLUMN route_site_target_id INTEGER REFERENCES route_site_targets(id) ON DELETE SET NULL;
ALTER TABLE request_attempts ADD COLUMN site_id INTEGER REFERENCES sites(id) ON DELETE SET NULL;
ALTER TABLE request_attempts ADD COLUMN endpoint_id INTEGER REFERENCES inference_endpoints(id) ON DELETE SET NULL;
ALTER TABLE request_attempts ADD COLUMN inference_credential_id INTEGER REFERENCES inference_credentials(id) ON DELETE SET NULL;
ALTER TABLE request_attempts ADD COLUMN site_model_id INTEGER REFERENCES site_models(id) ON DELETE SET NULL;
CREATE INDEX request_attempts_site_created_idx ON request_attempts(site_id, created_at DESC, id DESC);
CREATE INDEX request_attempts_site_target_created_idx ON request_attempts(route_site_target_id, created_at DESC, id DESC);
CREATE INDEX request_attempts_credential_created_idx ON request_attempts(inference_credential_id, created_at DESC, id DESC);
`,
	},
	{
		version: 5,
		name:    "site_accounts_and_legacy_migration_v5",
		sql: `
CREATE TABLE site_accounts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  site_id INTEGER NOT NULL UNIQUE REFERENCES sites(id) ON DELETE CASCADE,
  adapter_kind TEXT NOT NULL CHECK (length(adapter_kind) > 0),
  api_origin TEXT NOT NULL CHECK (length(api_origin) > 0),
  auth_cipher BLOB NOT NULL CHECK (length(auth_cipher) > 0),
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  capabilities_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(capabilities_json)),
  sync_state TEXT NOT NULL DEFAULT 'pending',
  last_attempt_at INTEGER,
  last_success_at INTEGER,
  last_error_code TEXT,
  last_error_message TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX site_accounts_sync_idx ON site_accounts(enabled, sync_state, last_attempt_at);

CREATE TABLE site_account_snapshots (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  site_account_id INTEGER NOT NULL REFERENCES site_accounts(id) ON DELETE CASCADE,
  snapshot_json TEXT NOT NULL CHECK (json_valid(snapshot_json)),
  captured_at INTEGER NOT NULL
);
CREATE INDEX site_account_snapshots_latest_idx ON site_account_snapshots(site_account_id, captured_at DESC, id DESC);

CREATE TABLE site_account_usage_records (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  site_account_id INTEGER NOT NULL REFERENCES site_accounts(id) ON DELETE CASCADE,
  dedupe_key TEXT NOT NULL CHECK (length(dedupe_key) > 0),
  external_id TEXT,
  model_name TEXT,
  amount_text TEXT,
  unit TEXT,
  raw_json TEXT NOT NULL CHECK (json_valid(raw_json)),
  occurred_at INTEGER,
  synced_at INTEGER NOT NULL,
  UNIQUE(site_account_id, dedupe_key)
);
CREATE INDEX site_account_usage_latest_idx ON site_account_usage_records(site_account_id, id DESC);
CREATE INDEX site_account_usage_occurred_idx ON site_account_usage_records(site_account_id, occurred_at DESC, id DESC);

CREATE TABLE legacy_upstream_site_mappings (
  upstream_id INTEGER PRIMARY KEY REFERENCES upstreams(id) ON DELETE CASCADE,
  site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
  endpoint_id INTEGER REFERENCES inference_endpoints(id) ON DELETE SET NULL,
  credential_id INTEGER REFERENCES inference_credentials(id) ON DELETE SET NULL,
  migrated_at INTEGER NOT NULL
);
CREATE INDEX legacy_upstream_site_mappings_site_idx ON legacy_upstream_site_mappings(site_id, upstream_id);

CREATE TABLE legacy_route_published_mappings (
  route_id INTEGER PRIMARY KEY REFERENCES routes(id) ON DELETE CASCADE,
  published_model_id INTEGER NOT NULL REFERENCES published_models(id) ON DELETE CASCADE,
  migrated_at INTEGER NOT NULL
);
CREATE INDEX legacy_route_published_mappings_model_idx ON legacy_route_published_mappings(published_model_id, route_id);
`,
	},
	{
		version: 6,
		name:    "downstream_routing_profiles_v6",
		sql: `
CREATE TABLE routing_profiles (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL CHECK (length(trim(name)) > 0),
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX routing_profiles_name_unique ON routing_profiles(name COLLATE NOCASE);

CREATE TABLE routing_profile_model_targets (
  routing_profile_id INTEGER NOT NULL REFERENCES routing_profiles(id) ON DELETE CASCADE,
  published_model_id INTEGER NOT NULL REFERENCES published_models(id) ON DELETE CASCADE,
  route_site_target_id INTEGER NOT NULL REFERENCES route_site_targets(id) ON DELETE CASCADE,
  position INTEGER NOT NULL CHECK (position >= 0),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(routing_profile_id, published_model_id, route_site_target_id),
  UNIQUE(routing_profile_id, published_model_id, position)
);
CREATE INDEX routing_profile_model_targets_model_idx ON routing_profile_model_targets(published_model_id, routing_profile_id, position);
CREATE INDEX routing_profile_model_targets_target_idx ON routing_profile_model_targets(route_site_target_id, routing_profile_id);

ALTER TABLE downstream_keys ADD COLUMN routing_profile_id INTEGER REFERENCES routing_profiles(id) ON DELETE SET NULL;
CREATE INDEX downstream_keys_routing_profile_idx ON downstream_keys(routing_profile_id, id);

ALTER TABLE request_logs ADD COLUMN routing_profile_id INTEGER;
ALTER TABLE request_logs ADD COLUMN routing_profile_name TEXT NOT NULL DEFAULT 'Default route';
CREATE INDEX request_logs_routing_profile_started_idx ON request_logs(routing_profile_id, started_at DESC, id DESC);
`,
	},
	{
		version: 7,
		name:    "request_attempt_resource_snapshots_v7",
		sql: `
ALTER TABLE request_attempts ADD COLUMN upstream_name TEXT;
ALTER TABLE request_attempts ADD COLUMN site_name TEXT;
ALTER TABLE request_attempts ADD COLUMN endpoint_name TEXT;
ALTER TABLE request_attempts ADD COLUMN credential_name TEXT;

UPDATE request_attempts SET
  upstream_name=(SELECT name FROM upstreams WHERE upstreams.id=request_attempts.upstream_id),
  site_name=(SELECT name FROM sites WHERE sites.id=request_attempts.site_id),
  endpoint_name=(SELECT name FROM inference_endpoints WHERE inference_endpoints.id=request_attempts.endpoint_id),
  credential_name=(SELECT name FROM inference_credentials WHERE inference_credentials.id=request_attempts.inference_credential_id);
`,
	},
	{
		version: 8,
		name:    "official_cache_write_billing_v8",
		sql: `
ALTER TABLE request_logs ADD COLUMN cache_write_tokens INTEGER;
ALTER TABLE request_logs ADD COLUMN cache_write_1h_tokens INTEGER;
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
