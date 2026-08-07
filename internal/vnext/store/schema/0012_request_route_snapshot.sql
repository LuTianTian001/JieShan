-- Freeze the effective route revision and ordered candidate identities at
-- admission time. Zero is reserved for requests created before this snapshot
-- existed; new writes are validated by the Store and always use a positive
-- route revision. Snapshot rows deliberately avoid foreign keys to mutable
-- inventory resources so later edits or deletions cannot rewrite history.
ALTER TABLE request_logs ADD COLUMN route_revision INTEGER NOT NULL DEFAULT 0
  CHECK (route_revision >= 0);
ALTER TABLE request_logs ADD COLUMN metering_status TEXT NOT NULL DEFAULT 'pending'
  CHECK (metering_status IN ('pending','metered','unavailable','not_applicable'));
ALTER TABLE request_logs ADD COLUMN metering_error_code TEXT;
ALTER TABLE request_attempts ADD COLUMN response_model TEXT;

-- V11 had no explicit metering lifecycle. Preserve what can be proven from
-- settled usage or cost fields, and label successful rows without usage as
-- unavailable instead of silently presenting them as pending or zero-cost.
UPDATE request_logs SET
  metering_status=CASE
    WHEN status='running' THEN 'pending'
    WHEN input_tokens IS NOT NULL
      OR output_tokens IS NOT NULL
      OR cache_read_tokens IS NOT NULL
      OR cache_write_tokens IS NOT NULL
      OR cache_write_5m_tokens IS NOT NULL
      OR cache_write_1h_tokens IS NOT NULL
      OR reasoning_tokens IS NOT NULL
      OR official_cost_nano_usd > 0
      OR charged_nano_usd > 0 THEN 'metered'
    WHEN status='success' THEN 'unavailable'
    ELSE 'not_applicable'
  END,
  metering_error_code=CASE
    WHEN status='success'
      AND input_tokens IS NULL
      AND output_tokens IS NULL
      AND cache_read_tokens IS NULL
      AND cache_write_tokens IS NULL
      AND cache_write_5m_tokens IS NULL
      AND cache_write_1h_tokens IS NULL
      AND reasoning_tokens IS NULL
      AND official_cost_nano_usd = 0
      AND charged_nano_usd = 0 THEN 'legacy_usage_unavailable'
    ELSE NULL
  END;

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
CREATE INDEX request_route_candidates_request_order_idx
  ON request_route_candidates(request_id, position);
CREATE INDEX request_route_candidates_target_idx
  ON request_route_candidates(provider_model_target_id, request_id);
