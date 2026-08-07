-- One revisioned settings record is the durable source of truth for routing,
-- active probes, request watchdogs, and request-log retention.
CREATE TABLE runtime_settings (
  singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
  failure_threshold INTEGER NOT NULL CHECK (failure_threshold BETWEEN 2 AND 20),
  failure_window_ms INTEGER NOT NULL CHECK (failure_window_ms BETWEEN 30000 AND 86400000),
  cooldown_ms INTEGER NOT NULL CHECK (cooldown_ms BETWEEN 30000 AND 86400000),
  probe_interval_ms INTEGER NOT NULL CHECK (probe_interval_ms BETWEEN 60000 AND 86400000),
  first_output_timeout_ms INTEGER NOT NULL CHECK (first_output_timeout_ms BETWEEN 1000 AND 600000),
  stream_idle_timeout_ms INTEGER NOT NULL CHECK (stream_idle_timeout_ms BETWEEN 1000 AND 600000),
  request_timeout_ms INTEGER NOT NULL CHECK (request_timeout_ms BETWEEN 1000 AND 1800000),
  max_attempts INTEGER NOT NULL CHECK (max_attempts BETWEEN 1 AND 20),
  log_retention_days INTEGER NOT NULL CHECK (log_retention_days BETWEEN 1 AND 365),
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  updated_at INTEGER NOT NULL DEFAULT 0 CHECK (updated_at >= 0),
  CHECK (request_timeout_ms >= first_output_timeout_ms),
  CHECK (request_timeout_ms >= stream_idle_timeout_ms)
);

INSERT INTO runtime_settings(
  singleton_id,failure_threshold,failure_window_ms,cooldown_ms,probe_interval_ms,
  first_output_timeout_ms,stream_idle_timeout_ms,request_timeout_ms,max_attempts,
  log_retention_days,revision,updated_at
) VALUES (1,2,300000,300000,300000,15000,60000,300000,4,30,1,0);
