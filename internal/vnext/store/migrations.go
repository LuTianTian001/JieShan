package store

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
)

type migration struct {
	version int
	name    string
	sql     string
}

//go:embed schema/0010_runtime_settings.sql
var runtimeSettingsMigrationSQL string

//go:embed schema/0011_tiered_price_catalog.sql
var tieredPriceCatalogMigrationSQL string

//go:embed schema/0012_request_route_snapshot.sql
var requestRouteSnapshotMigrationSQL string

//go:embed schema/0013_downstream_billing_policy.sql
var downstreamBillingPolicyMigrationSQL string

//go:embed schema/0014_multiplier_charge_constraints.sql
var multiplierChargeConstraintsMigrationSQL string

//go:embed schema/0015_first_output_timeout_default.sql
var firstOutputTimeoutDefaultMigrationSQL string

//go:embed schema/0016_hourly_reservation_repair.sql
var hourlyReservationRepairMigrationSQL string

var migrations = []migration{
	{
		version: 1,
		name:    "vnext_core_domain_v1",
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

CREATE TABLE site_endpoints (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
  name TEXT NOT NULL CHECK (length(trim(name)) > 0),
  base_url TEXT NOT NULL CHECK (length(trim(base_url)) > 0),
  wire_protocol TEXT NOT NULL CHECK (length(trim(wire_protocol)) > 0),
  surface TEXT NOT NULL CHECK (length(trim(surface)) > 0),
  adapter_kind TEXT NOT NULL DEFAULT 'generic' CHECK (length(trim(adapter_kind)) > 0),
  auth_scheme TEXT NOT NULL DEFAULT 'bearer' CHECK (length(trim(auth_scheme)) > 0),
  header_template_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(header_template_json)),
  secret_headers_cipher BLOB,
  cipher_version INTEGER NOT NULL DEFAULT 0 CHECK (cipher_version >= 0),
  position INTEGER NOT NULL CHECK (position >= 0),
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  CHECK (
    (secret_headers_cipher IS NULL AND cipher_version = 0) OR
    (secret_headers_cipher IS NOT NULL AND length(secret_headers_cipher) > 0 AND cipher_version > 0)
  ),
  UNIQUE(site_id, id),
  UNIQUE(site_id, position),
  UNIQUE(site_id, base_url, wire_protocol, surface)
);
CREATE INDEX site_endpoints_order_idx ON site_endpoints(site_id, position, id);
CREATE INDEX site_endpoints_protocol_idx ON site_endpoints(wire_protocol, surface, enabled, site_id, id);

CREATE TABLE site_credentials (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
  name TEXT NOT NULL CHECK (length(trim(name)) > 0),
  secret_cipher BLOB NOT NULL CHECK (length(secret_cipher) > 0),
  cipher_version INTEGER NOT NULL CHECK (cipher_version > 0),
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(site_id, id)
);
CREATE UNIQUE INDEX site_credentials_name_unique ON site_credentials(site_id, name COLLATE NOCASE);
CREATE INDEX site_credentials_enabled_idx ON site_credentials(site_id, enabled, id);

-- A credential is usable only through an explicit endpoint binding. There is
-- deliberately no site-wide credential fallback.
CREATE TABLE credential_endpoint_bindings (
  site_id INTEGER NOT NULL,
  endpoint_id INTEGER NOT NULL,
  credential_id INTEGER NOT NULL,
  position INTEGER NOT NULL CHECK (position >= 0),
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(endpoint_id, credential_id),
  UNIQUE(site_id, endpoint_id, credential_id),
  UNIQUE(endpoint_id, position),
  FOREIGN KEY(site_id, endpoint_id) REFERENCES site_endpoints(site_id, id) ON DELETE CASCADE,
  FOREIGN KEY(site_id, credential_id) REFERENCES site_credentials(site_id, id) ON DELETE CASCADE
);
CREATE INDEX credential_endpoint_bindings_credential_idx ON credential_endpoint_bindings(credential_id, enabled, endpoint_id);
CREATE INDEX credential_endpoint_bindings_endpoint_idx ON credential_endpoint_bindings(endpoint_id, enabled, position, credential_id);

-- A provider model target is the physical upstream identity: one source model
-- on one concrete endpoint. It is inventory, not a downstream route.
CREATE TABLE provider_model_targets (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  site_id INTEGER NOT NULL,
  endpoint_id INTEGER NOT NULL,
  source_model TEXT NOT NULL CHECK (length(trim(source_model)) > 0),
  display_name TEXT,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  last_seen_at INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(endpoint_id, source_model),
  UNIQUE(site_id, id),
  UNIQUE(site_id, endpoint_id, id),
  FOREIGN KEY(site_id, endpoint_id) REFERENCES site_endpoints(site_id, id) ON DELETE CASCADE
);
CREATE INDEX provider_model_targets_site_name_idx ON provider_model_targets(site_id, source_model, id);
CREATE INDEX provider_model_targets_endpoint_idx ON provider_model_targets(endpoint_id, enabled, source_model, id);

-- Credential access is scoped to one explicit endpoint/model pair. This is
-- where model-level 403s and discovery support are recorded; a credential can
-- never acquire access merely because it belongs to the same site.
CREATE TABLE credential_target_access (
  site_id INTEGER NOT NULL,
  endpoint_id INTEGER NOT NULL,
  credential_id INTEGER NOT NULL,
  provider_model_target_id INTEGER NOT NULL,
  availability TEXT NOT NULL DEFAULT 'unknown'
    CHECK (availability IN ('unknown','supported','unsupported','forbidden')),
  last_http_status INTEGER CHECK (last_http_status IS NULL OR last_http_status BETWEEN 100 AND 599),
  last_error_code TEXT,
  last_checked_at INTEGER,
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(credential_id, provider_model_target_id),
  FOREIGN KEY(site_id, endpoint_id, credential_id)
    REFERENCES credential_endpoint_bindings(site_id, endpoint_id, credential_id) ON DELETE CASCADE,
  FOREIGN KEY(site_id, endpoint_id, provider_model_target_id)
    REFERENCES provider_model_targets(site_id, endpoint_id, id) ON DELETE CASCADE
);
CREATE INDEX credential_target_access_target_idx ON credential_target_access(provider_model_target_id, availability, credential_id);
CREATE INDEX credential_target_access_binding_idx ON credential_target_access(endpoint_id, credential_id, availability, provider_model_target_id);

-- Exactly one default profile exists. A downstream key stores NULL to follow
-- it, so changing the global default route never requires rewriting keys.
CREATE TABLE routing_profiles (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL CHECK (length(trim(name)) > 0),
  is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0,1)),
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX routing_profiles_name_unique ON routing_profiles(name COLLATE NOCASE);
CREATE UNIQUE INDEX routing_profiles_single_default_idx ON routing_profiles(is_default) WHERE is_default=1;
INSERT INTO routing_profiles(id,name,is_default,revision,created_at,updated_at)
VALUES (1,'Default',1,1,0,0);

-- Published models are the canonical downstream model catalog. Their ordered
-- targets are shared by every key unless a named routing profile overrides a
-- model with a strict ordered subset.
CREATE TABLE published_models (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  public_name TEXT NOT NULL CHECK (length(trim(public_name)) > 0),
  official_price_sku TEXT NOT NULL CHECK (length(trim(official_price_sku)) > 0),
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX published_models_name_unique ON published_models(public_name COLLATE NOCASE);
CREATE INDEX published_models_enabled_name_idx ON published_models(enabled,public_name COLLATE NOCASE,id);

CREATE TABLE published_model_targets (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  published_model_id INTEGER NOT NULL REFERENCES published_models(id) ON DELETE CASCADE,
  provider_model_target_id INTEGER NOT NULL REFERENCES provider_model_targets(id) ON DELETE RESTRICT,
  position INTEGER NOT NULL CHECK (position >= 0),
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(published_model_id,provider_model_target_id),
  UNIQUE(published_model_id,position),
  UNIQUE(published_model_id,id)
);
CREATE INDEX published_model_targets_order_idx ON published_model_targets(published_model_id,position,id);
CREATE INDEX published_model_targets_provider_idx ON published_model_targets(provider_model_target_id,published_model_id,id);

-- Named profiles are sparse. Absence means inherit. A present row with
-- enabled=0 explicitly hides the model and must never fall back to default.
-- targets_overridden distinguishes inherited targets from an explicit ordered
-- subset, so deleting a target can never accidentally widen a custom route.
CREATE TABLE routing_profile_model_routes (
  routing_profile_id INTEGER NOT NULL REFERENCES routing_profiles(id) ON DELETE CASCADE,
  published_model_id INTEGER NOT NULL REFERENCES published_models(id) ON DELETE CASCADE,
  enabled INTEGER NOT NULL CHECK (enabled IN (0,1)),
  targets_overridden INTEGER NOT NULL DEFAULT 0 CHECK (targets_overridden IN (0,1)),
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(routing_profile_id,published_model_id)
);
CREATE INDEX routing_profile_model_routes_model_idx ON routing_profile_model_routes(published_model_id,routing_profile_id);

CREATE TABLE routing_profile_route_targets (
  routing_profile_id INTEGER NOT NULL,
  published_model_id INTEGER NOT NULL,
  published_model_target_id INTEGER NOT NULL,
  position INTEGER NOT NULL CHECK (position >= 0),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(routing_profile_id,published_model_id,published_model_target_id),
  UNIQUE(routing_profile_id,published_model_id,position),
  FOREIGN KEY(routing_profile_id,published_model_id)
    REFERENCES routing_profile_model_routes(routing_profile_id,published_model_id) ON DELETE CASCADE,
  FOREIGN KEY(published_model_id,published_model_target_id)
    REFERENCES published_model_targets(published_model_id,id) ON DELETE RESTRICT
);
CREATE INDEX routing_profile_route_targets_order_idx
  ON routing_profile_route_targets(routing_profile_id,published_model_id,position,published_model_target_id);

CREATE TABLE downstream_keys (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL CHECK (length(trim(name)) > 0),
  key_prefix TEXT NOT NULL CHECK (length(trim(key_prefix)) > 0),
  key_digest BLOB NOT NULL UNIQUE CHECK (length(key_digest) = 32),
  encrypted_secret BLOB,
  reveal_version INTEGER NOT NULL DEFAULT 0 CHECK (reveal_version >= 0),
  routing_profile_id INTEGER REFERENCES routing_profiles(id) ON DELETE SET NULL,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  quota_nano_usd INTEGER CHECK (quota_nano_usd IS NULL OR quota_nano_usd >= 0),
  used_nano_usd INTEGER NOT NULL DEFAULT 0 CHECK (used_nano_usd >= 0),
  reserved_nano_usd INTEGER NOT NULL DEFAULT 0 CHECK (reserved_nano_usd >= 0),
  rpm_limit INTEGER NOT NULL DEFAULT 0 CHECK (rpm_limit >= 0),
  expires_at INTEGER,
  last_used_at INTEGER,
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  CHECK (
    (encrypted_secret IS NULL AND reveal_version = 0) OR
    (encrypted_secret IS NOT NULL AND length(encrypted_secret) > 0 AND reveal_version > 0)
  ),
  CHECK (quota_nano_usd IS NULL OR used_nano_usd <= quota_nano_usd),
  CHECK (quota_nano_usd IS NULL OR reserved_nano_usd <= quota_nano_usd - used_nano_usd)
);
CREATE UNIQUE INDEX downstream_keys_name_unique ON downstream_keys(name COLLATE NOCASE);
CREATE INDEX downstream_keys_enabled_idx ON downstream_keys(enabled, expires_at, id);
CREATE INDEX downstream_keys_profile_idx ON downstream_keys(routing_profile_id,enabled,id);
`,
	},
	{
		version: 2,
		name:    "vnext_target_health_v1",
		sql: `
-- Attempt sequences are durable and monotonic per physical provider target.
-- They are allocated before an upstream request is sent and never reset when
-- target configuration changes, so completions from older attempts remain
-- distinguishable after process restarts.
CREATE TABLE target_attempt_sequences (
  provider_model_target_id INTEGER PRIMARY KEY
    REFERENCES provider_model_targets(id) ON DELETE CASCADE,
  last_allocated_sequence INTEGER NOT NULL DEFAULT 0
    CHECK (last_allocated_sequence >= 0),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

-- Health belongs to one physical endpoint/model target. config_revision is
-- the provider target revision whose observations produced this state;
-- state_version is the compare-and-swap token for durable reducer updates.
CREATE TABLE target_health (
  provider_model_target_id INTEGER PRIMARY KEY
    REFERENCES provider_model_targets(id) ON DELETE CASCADE,
  config_revision INTEGER NOT NULL CHECK (config_revision > 0),
  state_version INTEGER NOT NULL DEFAULT 1 CHECK (state_version > 0),
  phase TEXT NOT NULL DEFAULT 'closed'
    CHECK (phase IN ('closed','suspect','open','half_open')),
  capability TEXT NOT NULL DEFAULT 'unknown'
    CHECK (capability IN ('unknown','supported','unsupported')),
  consecutive_failures INTEGER NOT NULL DEFAULT 0
    CHECK (consecutive_failures >= 0),
  failure_window_started_at INTEGER,
  last_failure_at INTEGER,
  last_success_at INTEGER,
  last_failure_incident_id TEXT,
  last_failure_kind TEXT,
  cooldown_until INTEGER,
  last_event_sequence INTEGER NOT NULL DEFAULT 0
    CHECK (last_event_sequence >= 0),
  last_event_at INTEGER,
  half_open_sequence INTEGER NOT NULL DEFAULT 0
    CHECK (half_open_sequence >= 0),
  half_open_lease_until INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  CHECK (
    (phase = 'half_open' AND half_open_sequence > 0 AND half_open_lease_until IS NOT NULL) OR
    (phase <> 'half_open' AND half_open_sequence = 0 AND half_open_lease_until IS NULL)
  )
);
CREATE INDEX target_health_phase_cooldown_idx
  ON target_health(phase, cooldown_until, provider_model_target_id);
CREATE INDEX target_health_revision_idx
  ON target_health(config_revision, provider_model_target_id);
`,
	},
	{
		version: 3,
		name:    "vnext_downstream_key_prefix_index_v1",
		sql: `
CREATE INDEX downstream_keys_prefix_idx ON downstream_keys(key_prefix, id);
`,
	},
	{
		version: 4,
		name:    "vnext_request_accounting_v1",
		sql: `
CREATE TABLE request_logs (
  id TEXT PRIMARY KEY CHECK (length(trim(id)) BETWEEN 1 AND 128),
  downstream_key_id INTEGER NOT NULL REFERENCES downstream_keys(id) ON DELETE RESTRICT,
  downstream_key_name_snapshot TEXT NOT NULL CHECK (length(trim(downstream_key_name_snapshot)) > 0),
  published_model_id INTEGER NOT NULL CHECK (published_model_id > 0),
  published_model_revision INTEGER NOT NULL CHECK (published_model_revision > 0),
  effective_routing_profile_id INTEGER NOT NULL CHECK (effective_routing_profile_id > 0),
  effective_routing_profile_name_snapshot TEXT NOT NULL
    CHECK (length(trim(effective_routing_profile_name_snapshot)) > 0),
  source_routing_profile_id INTEGER NOT NULL CHECK (source_routing_profile_id > 0),
  source_routing_profile_name_snapshot TEXT NOT NULL
    CHECK (length(trim(source_routing_profile_name_snapshot)) > 0),
  public_model TEXT NOT NULL CHECK (length(trim(public_model)) > 0),
  api_surface TEXT NOT NULL CHECK (length(trim(api_surface)) > 0),
  reasoning_effort TEXT,
  thinking_budget_tokens INTEGER CHECK (thinking_budget_tokens IS NULL OR thinking_budget_tokens >= 0),
  is_stream INTEGER NOT NULL CHECK (is_stream IN (0,1)),
  price_catalog_version TEXT NOT NULL CHECK (length(trim(price_catalog_version)) > 0),
  price_sku TEXT NOT NULL CHECK (length(trim(price_sku)) > 0),
  reservation_nano_usd INTEGER NOT NULL CHECK (reservation_nano_usd >= 0),
  status TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running','success','failed','cancelled')),
  final_attempt_index INTEGER CHECK (final_attempt_index IS NULL OR final_attempt_index >= 0),
  http_status INTEGER CHECK (http_status IS NULL OR http_status BETWEEN 100 AND 599),
  first_token_ms INTEGER CHECK (first_token_ms IS NULL OR first_token_ms >= 0),
  duration_ms INTEGER CHECK (duration_ms IS NULL OR duration_ms >= 0),
  input_tokens INTEGER CHECK (input_tokens IS NULL OR input_tokens >= 0),
  output_tokens INTEGER CHECK (output_tokens IS NULL OR output_tokens >= 0),
  cache_read_tokens INTEGER CHECK (cache_read_tokens IS NULL OR cache_read_tokens >= 0),
  cache_write_tokens INTEGER CHECK (cache_write_tokens IS NULL OR cache_write_tokens >= 0),
  cache_write_5m_tokens INTEGER CHECK (cache_write_5m_tokens IS NULL OR cache_write_5m_tokens >= 0),
  cache_write_1h_tokens INTEGER CHECK (cache_write_1h_tokens IS NULL OR cache_write_1h_tokens >= 0),
  reasoning_tokens INTEGER CHECK (reasoning_tokens IS NULL OR reasoning_tokens >= 0),
  official_cost_nano_usd INTEGER NOT NULL DEFAULT 0 CHECK (official_cost_nano_usd >= 0),
  charged_nano_usd INTEGER NOT NULL DEFAULT 0 CHECK (charged_nano_usd >= 0),
  quota_capped INTEGER NOT NULL DEFAULT 0 CHECK (quota_capped IN (0,1)),
  error_code TEXT,
  started_at INTEGER NOT NULL,
  finished_at INTEGER,
  CHECK (charged_nano_usd <= official_cost_nano_usd),
  CHECK (quota_capped = 0 OR charged_nano_usd < official_cost_nano_usd),
  CHECK (first_token_ms IS NULL OR duration_ms IS NULL OR first_token_ms <= duration_ms),
  CHECK (
    (status = 'running' AND finished_at IS NULL) OR
    (status <> 'running' AND finished_at IS NOT NULL AND finished_at >= started_at)
  )
);
CREATE INDEX request_logs_started_idx ON request_logs(started_at DESC, id DESC);
CREATE INDEX request_logs_key_started_idx ON request_logs(downstream_key_id, started_at DESC, id DESC);
CREATE INDEX request_logs_model_started_idx ON request_logs(public_model, started_at DESC, id DESC);

CREATE TABLE request_attempts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  request_id TEXT NOT NULL REFERENCES request_logs(id) ON DELETE CASCADE,
  attempt_index INTEGER NOT NULL CHECK (attempt_index >= 0),
  published_model_target_id INTEGER NOT NULL CHECK (published_model_target_id > 0),
  published_model_target_revision INTEGER NOT NULL CHECK (published_model_target_revision > 0),
  provider_model_target_id INTEGER NOT NULL CHECK (provider_model_target_id > 0),
  provider_model_target_revision INTEGER NOT NULL CHECK (provider_model_target_revision > 0),
  site_id INTEGER NOT NULL CHECK (site_id > 0),
  endpoint_id INTEGER NOT NULL CHECK (endpoint_id > 0),
  credential_id INTEGER NOT NULL CHECK (credential_id > 0),
  site_name_snapshot TEXT NOT NULL CHECK (length(trim(site_name_snapshot)) > 0),
  endpoint_name_snapshot TEXT NOT NULL CHECK (length(trim(endpoint_name_snapshot)) > 0),
  credential_name_snapshot TEXT NOT NULL CHECK (length(trim(credential_name_snapshot)) > 0),
  source_model TEXT NOT NULL CHECK (length(trim(source_model)) > 0),
  wire_protocol TEXT NOT NULL CHECK (length(trim(wire_protocol)) > 0),
  api_surface TEXT NOT NULL CHECK (length(trim(api_surface)) > 0),
  status TEXT NOT NULL CHECK (status IN ('success','failed','cancelled')),
  http_status INTEGER CHECK (http_status IS NULL OR http_status BETWEEN 100 AND 599),
  failure_kind TEXT,
  error_code TEXT,
  switch_reason TEXT,
  first_token_ms INTEGER CHECK (first_token_ms IS NULL OR first_token_ms >= 0),
  duration_ms INTEGER NOT NULL CHECK (duration_ms >= 0),
  started_at INTEGER NOT NULL,
  finished_at INTEGER NOT NULL CHECK (finished_at >= started_at),
  CHECK (first_token_ms IS NULL OR first_token_ms <= duration_ms),
  UNIQUE(request_id, attempt_index)
);
CREATE INDEX request_attempts_request_order_idx ON request_attempts(request_id, attempt_index, id);
CREATE INDEX request_attempts_target_time_idx ON request_attempts(provider_model_target_id, started_at DESC, id DESC);
CREATE INDEX request_attempts_credential_time_idx ON request_attempts(credential_id, started_at DESC, id DESC);

CREATE TABLE quota_ledger (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  downstream_key_id INTEGER NOT NULL REFERENCES downstream_keys(id) ON DELETE RESTRICT,
  request_id TEXT NOT NULL REFERENCES request_logs(id) ON DELETE RESTRICT,
  event_type TEXT NOT NULL CHECK (event_type IN ('reserve','settle')),
  reserved_delta_nano_usd INTEGER NOT NULL,
  used_delta_nano_usd INTEGER NOT NULL CHECK (used_delta_nano_usd >= 0),
  price_catalog_version TEXT NOT NULL CHECK (length(trim(price_catalog_version)) > 0),
  price_sku TEXT NOT NULL CHECK (length(trim(price_sku)) > 0),
  created_at INTEGER NOT NULL,
  UNIQUE(request_id, event_type),
  CHECK (
    (event_type = 'reserve' AND reserved_delta_nano_usd > 0 AND used_delta_nano_usd = 0) OR
    (event_type = 'settle' AND reserved_delta_nano_usd <= 0 AND
      (reserved_delta_nano_usd < 0 OR used_delta_nano_usd > 0))
  )
);
CREATE INDEX quota_ledger_key_time_idx ON quota_ledger(downstream_key_id, created_at DESC, id DESC);
CREATE INDEX quota_ledger_request_idx ON quota_ledger(request_id, id);
`,
	},
	{
		version: 5,
		name:    "vnext_credential_runtime_state_v1",
		sql: `
CREATE TABLE credential_runtime_state (
  credential_id INTEGER PRIMARY KEY REFERENCES site_credentials(id) ON DELETE CASCADE,
  state TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active','invalid','exhausted','cooling')),
  cooling_until INTEGER,
  last_http_status INTEGER CHECK (last_http_status IS NULL OR last_http_status BETWEEN 100 AND 599),
  last_error_code TEXT,
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  updated_at INTEGER NOT NULL,
  CHECK (
    (state = 'cooling' AND cooling_until IS NOT NULL) OR
    (state <> 'cooling' AND cooling_until IS NULL)
  )
);
INSERT INTO credential_runtime_state(credential_id,state,cooling_until,last_http_status,last_error_code,revision,updated_at)
SELECT id,'active',NULL,NULL,NULL,1,updated_at FROM site_credentials;
CREATE INDEX credential_runtime_state_state_idx ON credential_runtime_state(state, cooling_until, credential_id);
`,
	},
	{
		version: 6,
		name:    "vnext_model_monitoring_v1",
		sql: `
-- Monitoring is canonical per published model. Keys and named profiles never
-- create duplicate probe schedules or histories.
CREATE TABLE model_monitor_settings (
  published_model_id INTEGER PRIMARY KEY REFERENCES published_models(id) ON DELETE CASCADE,
  enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0,1)),
  interval_ms INTEGER NOT NULL DEFAULT 300000 CHECK (interval_ms > 0),
  history_limit INTEGER NOT NULL DEFAULT 288 CHECK (history_limit BETWEEN 1 AND 10000),
  next_probe_at INTEGER NOT NULL,
  last_probe_started_at INTEGER,
  last_probe_finished_at INTEGER,
  lease_owner TEXT,
  lease_until INTEGER,
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  CHECK (
    (lease_owner IS NULL AND lease_until IS NULL) OR
    (lease_owner IS NOT NULL AND length(trim(lease_owner)) > 0 AND lease_until IS NOT NULL)
  )
);
CREATE INDEX model_monitor_settings_due_idx
  ON model_monitor_settings(enabled, next_probe_at, lease_until, published_model_id);

CREATE TABLE model_probe_runs (
  id TEXT PRIMARY KEY CHECK (length(trim(id)) BETWEEN 1 AND 128),
  published_model_id INTEGER NOT NULL,
  published_model_revision INTEGER NOT NULL CHECK (published_model_revision > 0),
  public_model_snapshot TEXT NOT NULL CHECK (length(trim(public_model_snapshot)) > 0),
  trigger_kind TEXT NOT NULL CHECK (trigger_kind IN ('scheduled','manual')),
  status TEXT NOT NULL DEFAULT 'running'
    CHECK (status IN ('running','completed','cancelled','internal_error')),
  target_count INTEGER NOT NULL CHECK (target_count >= 0),
  success_count INTEGER NOT NULL DEFAULT 0 CHECK (success_count >= 0),
  failure_count INTEGER NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
  skipped_count INTEGER NOT NULL DEFAULT 0 CHECK (skipped_count >= 0),
  started_at INTEGER NOT NULL,
  finished_at INTEGER,
  UNIQUE(id, published_model_id),
  FOREIGN KEY(published_model_id)
    REFERENCES published_models(id) ON DELETE CASCADE,
  CHECK (success_count + failure_count + skipped_count <= target_count),
  CHECK (
    (status = 'running' AND finished_at IS NULL) OR
    (status <> 'running' AND finished_at IS NOT NULL AND finished_at >= started_at)
  )
);
CREATE INDEX model_probe_runs_route_time_idx
  ON model_probe_runs(published_model_id, started_at DESC, id DESC);
CREATE INDEX model_probe_runs_status_time_idx
  ON model_probe_runs(status, started_at, id);

-- One final point is retained for every model x physical target in a run.
-- Attempt-level credential detail belongs to the executor and must not inflate
-- the success-rate denominator or status history.
CREATE TABLE model_probe_results (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id TEXT NOT NULL REFERENCES model_probe_runs(id) ON DELETE CASCADE,
  published_model_id INTEGER NOT NULL,
  published_model_target_id INTEGER NOT NULL CHECK (published_model_target_id > 0),
  published_model_target_revision INTEGER NOT NULL CHECK (published_model_target_revision > 0),
  provider_model_target_id INTEGER NOT NULL CHECK (provider_model_target_id > 0),
  provider_model_target_revision INTEGER NOT NULL CHECK (provider_model_target_revision > 0),
  target_position INTEGER NOT NULL CHECK (target_position >= 0),
  site_id INTEGER NOT NULL CHECK (site_id > 0),
  endpoint_id INTEGER NOT NULL CHECK (endpoint_id > 0),
  site_name_snapshot TEXT NOT NULL CHECK (length(trim(site_name_snapshot)) > 0),
  endpoint_name_snapshot TEXT NOT NULL CHECK (length(trim(endpoint_name_snapshot)) > 0),
  source_model_snapshot TEXT NOT NULL CHECK (length(trim(source_model_snapshot)) > 0),
  wire_protocol TEXT NOT NULL CHECK (length(trim(wire_protocol)) > 0),
  api_surface TEXT NOT NULL CHECK (length(trim(api_surface)) > 0),
  outcome TEXT NOT NULL CHECK (outcome IN ('success','failure','skipped')),
  permit_mode TEXT CHECK (permit_mode IS NULL OR permit_mode IN ('normal','half_open')),
  permit_reason TEXT,
  http_status INTEGER CHECK (http_status IS NULL OR http_status BETWEEN 100 AND 599),
  failure_kind TEXT,
  error_code TEXT,
  latency_ms INTEGER NOT NULL CHECK (latency_ms >= 0),
  first_output_ms INTEGER CHECK (first_output_ms IS NULL OR first_output_ms >= 0),
  started_at INTEGER NOT NULL,
  finished_at INTEGER NOT NULL CHECK (finished_at >= started_at),
  health_applied INTEGER NOT NULL DEFAULT 0 CHECK (health_applied IN (0,1)),
  health_apply_reason TEXT,
  health_error_code TEXT,
  UNIQUE(run_id, provider_model_target_id),
  FOREIGN KEY(run_id, published_model_id)
    REFERENCES model_probe_runs(id, published_model_id) ON DELETE CASCADE,
  CHECK (first_output_ms IS NULL OR first_output_ms <= latency_ms),
  CHECK (
    (outcome = 'success' AND failure_kind IS NULL) OR
    (outcome = 'failure' AND failure_kind IS NOT NULL AND length(trim(failure_kind)) > 0) OR
    (outcome = 'skipped' AND permit_reason IS NOT NULL AND length(trim(permit_reason)) > 0)
  )
);
CREATE INDEX model_probe_results_route_target_time_idx
  ON model_probe_results(published_model_id, provider_model_target_id, finished_at DESC, id DESC);
CREATE INDEX model_probe_results_run_order_idx
  ON model_probe_results(run_id, target_position, id);
`,
	},
	{
		version: 7,
		name:    "vnext_site_accounts_v1",
		sql: `
-- A site account is optional control-plane state. It is never an inference
-- credential and never participates in routing or downstream accounting.
CREATE TABLE site_account_connections (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  site_id INTEGER NOT NULL UNIQUE REFERENCES sites(id) ON DELETE CASCADE,
  adapter_kind TEXT NOT NULL CHECK (length(trim(adapter_kind)) > 0),
  origin TEXT NOT NULL CHECK (length(trim(origin)) > 0),
  secrets_cipher BLOB NOT NULL CHECK (length(secrets_cipher) > 0),
  cipher_version INTEGER NOT NULL CHECK (cipher_version > 0),
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  last_session_refresh_at INTEGER,
  last_balance_refresh_at INTEGER,
  last_usage_refresh_at INTEGER,
  last_error_operation TEXT,
  last_error_code TEXT,
  last_error_at INTEGER,
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(site_id, id),
  CHECK (
    (last_error_operation IS NULL AND last_error_code IS NULL AND last_error_at IS NULL) OR
    (last_error_operation IS NOT NULL AND length(trim(last_error_operation)) > 0 AND
      last_error_code IS NOT NULL AND length(trim(last_error_code)) > 0 AND last_error_at IS NOT NULL)
  )
);
CREATE INDEX site_account_connections_adapter_idx
  ON site_account_connections(adapter_kind, enabled, site_id);

-- Values remain exact decimal strings in the unit reported by the upstream.
-- They are informational and are deliberately separate from nano-USD quota.
CREATE TABLE site_balance_snapshots (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  site_account_connection_id INTEGER NOT NULL,
  site_id INTEGER NOT NULL,
  adapter_kind TEXT NOT NULL CHECK (length(trim(adapter_kind)) > 0),
  account_remote_id TEXT,
  account_name TEXT,
  available_value TEXT NOT NULL CHECK (length(trim(available_value)) > 0),
  available_unit TEXT NOT NULL CHECK (length(trim(available_unit)) > 0),
  used_value TEXT,
  used_unit TEXT,
  captured_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  FOREIGN KEY(site_id, site_account_connection_id)
    REFERENCES site_account_connections(site_id, id) ON DELETE CASCADE,
  CHECK (
    (used_value IS NULL AND used_unit IS NULL) OR
    (used_value IS NOT NULL AND length(trim(used_value)) > 0 AND
      used_unit IS NOT NULL AND length(trim(used_unit)) > 0)
  )
);
CREATE INDEX site_balance_snapshots_latest_idx
  ON site_balance_snapshots(site_id, captured_at DESC, id DESC);

-- Source usage is a reconciliation feed from the upstream site. It does not
-- replace the gateway request log and cannot debit a downstream key.
CREATE TABLE site_usage_records (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  site_account_connection_id INTEGER NOT NULL,
  site_id INTEGER NOT NULL,
  adapter_kind TEXT NOT NULL CHECK (length(trim(adapter_kind)) > 0),
  dedup_key TEXT NOT NULL CHECK (length(trim(dedup_key)) > 0),
  remote_id TEXT,
  request_id TEXT,
  upstream_request_id TEXT,
  occurred_at INTEGER NOT NULL,
  model TEXT,
  upstream_model TEXT,
  status TEXT,
  http_status INTEGER CHECK (http_status IS NULL OR http_status BETWEEN 100 AND 599),
  input_tokens INTEGER CHECK (input_tokens IS NULL OR input_tokens >= 0),
  output_tokens INTEGER CHECK (output_tokens IS NULL OR output_tokens >= 0),
  cache_read_tokens INTEGER CHECK (cache_read_tokens IS NULL OR cache_read_tokens >= 0),
  cache_write_tokens INTEGER CHECK (cache_write_tokens IS NULL OR cache_write_tokens >= 0),
  reasoning_tokens INTEGER CHECK (reasoning_tokens IS NULL OR reasoning_tokens >= 0),
  total_tokens INTEGER CHECK (total_tokens IS NULL OR total_tokens >= 0),
  charge_value TEXT,
  charge_unit TEXT,
  duration_ms INTEGER CHECK (duration_ms IS NULL OR duration_ms >= 0),
  api_key_name TEXT,
  source_fetched_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  UNIQUE(site_account_connection_id, dedup_key),
  FOREIGN KEY(site_id, site_account_connection_id)
    REFERENCES site_account_connections(site_id, id) ON DELETE CASCADE,
  CHECK (
    (charge_value IS NULL AND charge_unit IS NULL) OR
    (charge_value IS NOT NULL AND length(trim(charge_value)) > 0 AND
      charge_unit IS NOT NULL AND length(trim(charge_unit)) > 0)
  )
);
CREATE INDEX site_usage_records_site_time_idx
  ON site_usage_records(site_id, occurred_at DESC, id DESC);
CREATE INDEX site_usage_records_site_model_time_idx
  ON site_usage_records(site_id, model, occurred_at DESC, id DESC);
CREATE INDEX site_usage_records_site_status_time_idx
  ON site_usage_records(site_id, status, occurred_at DESC, id DESC);
CREATE INDEX site_usage_records_request_idx
  ON site_usage_records(site_id, request_id, occurred_at DESC, id DESC);
`,
	},
	{
		version: 8,
		name:    "vnext_official_price_catalog_v1",
		sql: `
-- Price catalog rows are assembled once, sealed, and thereafter immutable.
-- Activation is a separate revisioned pointer so historical request pricing
-- remains reproducible when a newer catalog becomes current.
CREATE TABLE price_catalogs (
  version TEXT PRIMARY KEY CHECK (length(trim(version)) BETWEEN 1 AND 128),
  schema_version INTEGER NOT NULL CHECK (schema_version > 0),
  digest TEXT NOT NULL UNIQUE CHECK (length(digest) = 71 AND substr(digest,1,7) = 'sha256:'),
  settlement_currency TEXT NOT NULL CHECK (settlement_currency = 'USD'),
  source_name TEXT NOT NULL CHECK (length(trim(source_name)) > 0),
  source_digest TEXT NOT NULL CHECK (length(source_digest) = 71 AND substr(source_digest,1,7) = 'sha256:'),
  fx_version TEXT,
  fx_source_url TEXT,
  fx_source_digest TEXT,
  fx_verified_at INTEGER,
  fetched_at INTEGER NOT NULL,
  verified_at INTEGER NOT NULL,
  effective_at INTEGER NOT NULL,
  imported_at INTEGER NOT NULL,
  sealed INTEGER NOT NULL DEFAULT 0 CHECK (sealed IN (0,1)),
  CHECK (
    (fx_source_url IS NULL AND fx_source_digest IS NULL AND fx_verified_at IS NULL) OR
    (fx_version IS NOT NULL AND length(trim(fx_version)) > 0 AND
      fx_source_url IS NOT NULL AND length(trim(fx_source_url)) > 0 AND
      fx_source_digest IS NOT NULL AND length(fx_source_digest) = 71 AND
      substr(fx_source_digest,1,7) = 'sha256:' AND fx_verified_at IS NOT NULL)
  )
);
CREATE INDEX price_catalogs_effective_idx ON price_catalogs(effective_at DESC, imported_at DESC, version);

CREATE TABLE price_catalog_entries (
  catalog_version TEXT NOT NULL REFERENCES price_catalogs(version) ON DELETE RESTRICT,
  sku TEXT NOT NULL CHECK (length(trim(sku)) BETWEEN 1 AND 128),
  provider TEXT NOT NULL CHECK (length(trim(provider)) > 0),
  model_pattern TEXT NOT NULL CHECK (length(trim(model_pattern)) > 0),
  pricing_basis TEXT NOT NULL CHECK (pricing_basis = 'flat_tokens_per_million'),
  verification_status TEXT NOT NULL CHECK (verification_status = 'verified'),
  source_url TEXT NOT NULL CHECK (length(trim(source_url)) > 0),
  source_digest TEXT NOT NULL CHECK (length(source_digest) = 71 AND substr(source_digest,1,7) = 'sha256:'),
  verified_at INTEGER NOT NULL,
  native_currency TEXT NOT NULL CHECK (length(native_currency) = 3),
  usd_per_native_unit TEXT NOT NULL CHECK (length(trim(usd_per_native_unit)) > 0),
  position INTEGER NOT NULL CHECK (position >= 0),
  PRIMARY KEY(catalog_version, sku)
);
CREATE UNIQUE INDEX price_catalog_entries_sku_unique
  ON price_catalog_entries(catalog_version, sku COLLATE NOCASE);
CREATE INDEX price_catalog_entries_provider_idx
  ON price_catalog_entries(catalog_version, provider, position, sku);

CREATE TABLE price_catalog_rates (
  catalog_version TEXT NOT NULL,
  sku TEXT NOT NULL,
  token_class TEXT NOT NULL CHECK (
    token_class IN ('input','output','cache_read','cache_write','cache_write_5m','cache_write_1h','reasoning')
  ),
  native_price_per_million TEXT NOT NULL CHECK (length(trim(native_price_per_million)) > 0),
  nano_usd_per_million INTEGER NOT NULL CHECK (nano_usd_per_million >= 0),
  position INTEGER NOT NULL CHECK (position >= 0),
  PRIMARY KEY(catalog_version, sku, token_class),
  FOREIGN KEY(catalog_version, sku)
    REFERENCES price_catalog_entries(catalog_version, sku) ON DELETE RESTRICT
);
CREATE INDEX price_catalog_rates_catalog_order_idx
  ON price_catalog_rates(catalog_version, position, sku, token_class);

CREATE TABLE price_catalog_state (
  singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
  active_version TEXT REFERENCES price_catalogs(version) ON DELETE RESTRICT,
  revision INTEGER NOT NULL DEFAULT 0 CHECK (revision >= 0),
  updated_at INTEGER NOT NULL
);
INSERT INTO price_catalog_state(singleton_id,active_version,revision,updated_at) VALUES (1,NULL,0,0);

CREATE TRIGGER price_catalogs_sealed_update
BEFORE UPDATE ON price_catalogs
WHEN OLD.sealed = 1
BEGIN
  SELECT RAISE(ABORT, 'sealed price catalog is immutable');
END;
CREATE TRIGGER price_catalogs_sealed_delete
BEFORE DELETE ON price_catalogs
BEGIN
  SELECT RAISE(ABORT, 'sealed price catalog is immutable');
END;
CREATE TRIGGER price_catalog_entries_sealed_insert
BEFORE INSERT ON price_catalog_entries
WHEN (SELECT sealed FROM price_catalogs WHERE version=NEW.catalog_version) <> 0
BEGIN
  SELECT RAISE(ABORT, 'sealed price catalog is immutable');
END;
CREATE TRIGGER price_catalog_entries_immutable_update
BEFORE UPDATE ON price_catalog_entries
BEGIN
  SELECT RAISE(ABORT, 'price catalog entry is immutable');
END;
CREATE TRIGGER price_catalog_entries_immutable_delete
BEFORE DELETE ON price_catalog_entries
BEGIN
  SELECT RAISE(ABORT, 'price catalog entry is immutable');
END;
CREATE TRIGGER price_catalog_rates_sealed_insert
BEFORE INSERT ON price_catalog_rates
WHEN (SELECT sealed FROM price_catalogs WHERE version=NEW.catalog_version) <> 0
BEGIN
  SELECT RAISE(ABORT, 'sealed price catalog is immutable');
END;
CREATE TRIGGER price_catalog_rates_immutable_update
BEFORE UPDATE ON price_catalog_rates
BEGIN
  SELECT RAISE(ABORT, 'price catalog rate is immutable');
END;
CREATE TRIGGER price_catalog_rates_immutable_delete
BEFORE DELETE ON price_catalog_rates
BEGIN
  SELECT RAISE(ABORT, 'price catalog rate is immutable');
END;
`,
	},
	{
		version: 9,
		name:    "vnext_admin_auth_v1",
		sql: `
-- VNext owns one administrator identity. Passwords use an encoded Argon2id
-- verifier; neither this table nor any other VNext table stores plaintext.
CREATE TABLE admin_users (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  username TEXT NOT NULL UNIQUE COLLATE NOCASE CHECK (username = 'admin'),
  password_hash TEXT NOT NULL CHECK (
    length(password_hash) BETWEEN 64 AND 512 AND substr(password_hash,1,10) = '$argon2id$'
  ),
  password_changed_at INTEGER NOT NULL,
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL CHECK (updated_at >= created_at),
  CHECK (password_changed_at >= created_at AND password_changed_at <= updated_at)
);

-- Browser secrets are never persisted. token_hash and csrf_hash are SHA-256
-- digests of independent 256-bit random values returned only to the client.
CREATE TABLE admin_sessions (
  token_hash BLOB PRIMARY KEY CHECK (length(token_hash) = 32),
  admin_user_id INTEGER NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
  csrf_hash BLOB NOT NULL CHECK (length(csrf_hash) = 32),
  expires_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  CHECK (expires_at > created_at),
  CHECK (last_seen_at >= created_at AND last_seen_at <= expires_at)
) WITHOUT ROWID;
CREATE INDEX admin_sessions_expiry_idx ON admin_sessions(expires_at, token_hash);
CREATE INDEX admin_sessions_user_seen_idx ON admin_sessions(admin_user_id, last_seen_at DESC, token_hash);
`,
	},
	{
		version: 10,
		name:    "vnext_runtime_settings_v1",
		sql:     runtimeSettingsMigrationSQL,
	},
	{
		version: 11,
		name:    "vnext_tiered_price_catalog_v1",
		sql:     tieredPriceCatalogMigrationSQL,
	},
	{
		version: 12,
		name:    "vnext_request_route_snapshot_v1",
		sql:     requestRouteSnapshotMigrationSQL,
	},
	{
		version: 13,
		name:    "vnext_downstream_billing_policy_v1",
		sql:     downstreamBillingPolicyMigrationSQL,
	},
	{
		version: 14,
		name:    "vnext_multiplier_charge_constraints_v1",
		sql:     multiplierChargeConstraintsMigrationSQL,
	},
	{
		version: 15,
		name:    "vnext_first_output_timeout_default_v1",
		sql:     firstOutputTimeoutDefaultMigrationSQL,
	},
	{
		version: 16,
		name:    "vnext_hourly_reservation_repair_v1",
		sql:     hourlyReservationRepairMigrationSQL,
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
	previousVersion := 0
	for _, item := range migrations {
		if item.version <= previousVersion {
			return fmt.Errorf("VNext migrations must be strictly increasing: %d follows %d", item.version, previousVersion)
		}
		previousVersion = item.version
		var name string
		err := s.DB.QueryRowContext(ctx, "SELECT name FROM schema_migrations WHERE version=?", item.version).Scan(&name)
		if err == nil {
			if name != item.name {
				return fmt.Errorf("migration %d name mismatch: database has %q, code has %q", item.version, name, item.name)
			}
			continue
		}
		if err != sql.ErrNoRows {
			return err
		}
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, item.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d (%s): %w", item.version, item.name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,applied_at) VALUES (?,?,?)`, item.version, item.name, NowMS()); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
