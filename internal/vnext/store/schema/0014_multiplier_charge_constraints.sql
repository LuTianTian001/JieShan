-- V4 assumed the downstream charge could never exceed the official base
-- cost. A configurable multiplier intentionally invalidates that assumption.
-- Rebuild only the parent request table while preserving every snapshot field
-- and child-table foreign key.
PRAGMA legacy_alter_table=ON;
ALTER TABLE request_attempts RENAME TO request_attempts_before_multiplier_constraints;
ALTER TABLE quota_ledger RENAME TO quota_ledger_before_multiplier_constraints;
ALTER TABLE request_route_candidates RENAME TO request_route_candidates_before_multiplier_constraints;
ALTER TABLE request_logs RENAME TO request_logs_before_multiplier_constraints;

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
  route_revision INTEGER NOT NULL DEFAULT 0 CHECK (route_revision >= 0),
  metering_status TEXT NOT NULL DEFAULT 'pending'
    CHECK (metering_status IN ('pending','metered','unavailable','not_applicable')),
  metering_error_code TEXT,
  billing_multiplier_bps INTEGER NOT NULL DEFAULT 10000
    CHECK (billing_multiplier_bps BETWEEN 0 AND 10000000),
  CHECK (first_token_ms IS NULL OR duration_ms IS NULL OR first_token_ms <= duration_ms),
  CHECK (
    (status = 'running' AND finished_at IS NULL) OR
    (status <> 'running' AND finished_at IS NOT NULL AND finished_at >= started_at)
  )
);

INSERT INTO request_logs(
  id,downstream_key_id,downstream_key_name_snapshot,published_model_id,published_model_revision,
  effective_routing_profile_id,effective_routing_profile_name_snapshot,
  source_routing_profile_id,source_routing_profile_name_snapshot,public_model,api_surface,
  reasoning_effort,thinking_budget_tokens,is_stream,price_catalog_version,price_sku,
  reservation_nano_usd,status,final_attempt_index,http_status,first_token_ms,duration_ms,
  input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,cache_write_5m_tokens,
  cache_write_1h_tokens,reasoning_tokens,official_cost_nano_usd,charged_nano_usd,quota_capped,
  error_code,started_at,finished_at,route_revision,metering_status,metering_error_code,
  billing_multiplier_bps
)
SELECT
  id,downstream_key_id,downstream_key_name_snapshot,published_model_id,published_model_revision,
  effective_routing_profile_id,effective_routing_profile_name_snapshot,
  source_routing_profile_id,source_routing_profile_name_snapshot,public_model,api_surface,
  reasoning_effort,thinking_budget_tokens,is_stream,price_catalog_version,price_sku,
  reservation_nano_usd,status,final_attempt_index,http_status,first_token_ms,duration_ms,
  input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,cache_write_5m_tokens,
  cache_write_1h_tokens,reasoning_tokens,official_cost_nano_usd,charged_nano_usd,quota_capped,
  error_code,started_at,finished_at,route_revision,metering_status,metering_error_code,
  billing_multiplier_bps
FROM request_logs_before_multiplier_constraints;

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
  response_model TEXT,
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
INSERT INTO request_attempts(
  id,request_id,attempt_index,published_model_target_id,published_model_target_revision,
  provider_model_target_id,provider_model_target_revision,site_id,endpoint_id,credential_id,
  site_name_snapshot,endpoint_name_snapshot,credential_name_snapshot,source_model,response_model,
  wire_protocol,api_surface,status,http_status,failure_kind,error_code,switch_reason,first_token_ms,
  duration_ms,started_at,finished_at
)
SELECT
  id,request_id,attempt_index,published_model_target_id,published_model_target_revision,
  provider_model_target_id,provider_model_target_revision,site_id,endpoint_id,credential_id,
  site_name_snapshot,endpoint_name_snapshot,credential_name_snapshot,source_model,response_model,
  wire_protocol,api_surface,status,http_status,failure_kind,error_code,switch_reason,first_token_ms,
  duration_ms,started_at,finished_at
FROM request_attempts_before_multiplier_constraints;

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
INSERT INTO quota_ledger(
  id,downstream_key_id,request_id,event_type,reserved_delta_nano_usd,used_delta_nano_usd,
  price_catalog_version,price_sku,created_at
)
SELECT
  id,downstream_key_id,request_id,event_type,reserved_delta_nano_usd,used_delta_nano_usd,
  price_catalog_version,price_sku,created_at
FROM quota_ledger_before_multiplier_constraints;

CREATE TABLE request_route_candidates (
  request_id TEXT NOT NULL REFERENCES request_logs(id) ON DELETE CASCADE,
  position INTEGER NOT NULL CHECK (position >= 0),
  published_model_target_id INTEGER NOT NULL CHECK (published_model_target_id > 0),
  published_model_target_revision INTEGER NOT NULL CHECK (published_model_target_revision > 0),
  provider_model_target_id INTEGER NOT NULL CHECK (provider_model_target_id > 0),
  provider_model_target_revision INTEGER NOT NULL CHECK (provider_model_target_revision > 0),
  site_id INTEGER NOT NULL CHECK (site_id > 0),
  site_name_snapshot TEXT NOT NULL CHECK (length(trim(site_name_snapshot)) > 0),
  endpoint_id INTEGER NOT NULL CHECK (endpoint_id > 0),
  endpoint_name_snapshot TEXT NOT NULL CHECK (length(trim(endpoint_name_snapshot)) > 0),
  source_model TEXT NOT NULL CHECK (length(trim(source_model)) > 0),
  wire_protocol TEXT NOT NULL CHECK (length(trim(wire_protocol)) > 0),
  api_surface TEXT NOT NULL CHECK (length(trim(api_surface)) > 0),
  credentials_json TEXT NOT NULL DEFAULT '[]'
    CHECK (json_valid(credentials_json) AND json_type(credentials_json) = 'array'),
  initial_eligibility TEXT NOT NULL CHECK (initial_eligibility IN ('eligible','skipped')),
  initial_reason TEXT NOT NULL CHECK (length(trim(initial_reason)) BETWEEN 1 AND 128),
  disposition TEXT NOT NULL DEFAULT 'pending'
    CHECK (disposition IN ('pending','attempted','skipped','not_attempted')),
  disposition_reason TEXT CHECK (disposition_reason IS NULL OR length(trim(disposition_reason)) BETWEEN 1 AND 128),
  attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  first_attempt_index INTEGER CHECK (first_attempt_index IS NULL OR first_attempt_index >= 0),
  last_attempt_index INTEGER CHECK (last_attempt_index IS NULL OR last_attempt_index >= 0),
  PRIMARY KEY(request_id, position),
  UNIQUE(request_id, provider_model_target_id),
  CHECK (
    (attempt_count = 0 AND first_attempt_index IS NULL AND last_attempt_index IS NULL) OR
    (attempt_count > 0 AND first_attempt_index IS NOT NULL AND last_attempt_index IS NOT NULL
      AND first_attempt_index <= last_attempt_index)
  )
);
INSERT INTO request_route_candidates(
  request_id,position,published_model_target_id,published_model_target_revision,
  provider_model_target_id,provider_model_target_revision,site_id,site_name_snapshot,endpoint_id,
  endpoint_name_snapshot,source_model,wire_protocol,api_surface,credentials_json,
  initial_eligibility,initial_reason,disposition,disposition_reason,attempt_count,
  first_attempt_index,last_attempt_index
)
SELECT
  request_id,position,published_model_target_id,published_model_target_revision,
  provider_model_target_id,provider_model_target_revision,site_id,site_name_snapshot,endpoint_id,
  endpoint_name_snapshot,source_model,wire_protocol,api_surface,credentials_json,
  initial_eligibility,initial_reason,disposition,disposition_reason,attempt_count,
  first_attempt_index,last_attempt_index
FROM request_route_candidates_before_multiplier_constraints;

DROP TABLE request_attempts_before_multiplier_constraints;
DROP TABLE quota_ledger_before_multiplier_constraints;
DROP TABLE request_route_candidates_before_multiplier_constraints;
DROP TABLE request_logs_before_multiplier_constraints;
CREATE INDEX request_logs_started_idx ON request_logs(started_at DESC, id DESC);
CREATE INDEX request_logs_key_started_idx ON request_logs(downstream_key_id, started_at DESC, id DESC);
CREATE INDEX request_logs_model_started_idx ON request_logs(public_model, started_at DESC, id DESC);
CREATE INDEX request_attempts_request_order_idx ON request_attempts(request_id, attempt_index, id);
CREATE INDEX request_attempts_target_time_idx ON request_attempts(provider_model_target_id, started_at DESC, id DESC);
CREATE INDEX request_attempts_credential_time_idx ON request_attempts(credential_id, started_at DESC, id DESC);
CREATE INDEX quota_ledger_key_time_idx ON quota_ledger(downstream_key_id, created_at DESC, id DESC);
CREATE INDEX quota_ledger_request_idx ON quota_ledger(request_id, id);
CREATE INDEX request_route_candidates_request_order_idx
  ON request_route_candidates(request_id, position);
CREATE INDEX request_route_candidates_target_idx
  ON request_route_candidates(provider_model_target_id, request_id);
PRAGMA legacy_alter_table=OFF;
