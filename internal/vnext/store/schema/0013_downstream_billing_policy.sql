-- Downstream billing is expressed only in official nano-USD. Per-key
-- multipliers are stored as exact basis points (1.00x = 10000) so admission
-- and settlement never depend on floating-point arithmetic.
ALTER TABLE downstream_keys ADD COLUMN hourly_quota_nano_usd INTEGER
  CHECK (hourly_quota_nano_usd IS NULL OR hourly_quota_nano_usd >= 0);
ALTER TABLE downstream_keys ADD COLUMN billing_multiplier_bps INTEGER NOT NULL DEFAULT 10000
  CHECK (billing_multiplier_bps BETWEEN 0 AND 10000000);

-- RPM is no longer a product policy. Existing databases are normalized to an
-- unlimited value while the legacy column remains for migration compatibility.
UPDATE downstream_keys SET rpm_limit=0 WHERE rpm_limit<>0;

-- One row per key and UTC hour keeps reservations correct when a streaming
-- request crosses an hour boundary. Old settled buckets are pruned with other
-- operational history; buckets with active reservations are retained.
CREATE TABLE downstream_key_hourly_usage (
  downstream_key_id INTEGER NOT NULL REFERENCES downstream_keys(id) ON DELETE CASCADE,
  window_started_at INTEGER NOT NULL CHECK (window_started_at >= 0),
  used_nano_usd INTEGER NOT NULL DEFAULT 0 CHECK (used_nano_usd >= 0),
  reserved_nano_usd INTEGER NOT NULL DEFAULT 0 CHECK (reserved_nano_usd >= 0),
  updated_at INTEGER NOT NULL CHECK (updated_at >= window_started_at),
  PRIMARY KEY(downstream_key_id, window_started_at)
) WITHOUT ROWID;
CREATE INDEX downstream_key_hourly_usage_cleanup_idx
  ON downstream_key_hourly_usage(window_started_at, reserved_nano_usd, downstream_key_id);

-- Requests left running by the previous process still own their legacy global
-- reservation. Materialize the matching UTC-hour buckets so startup recovery
-- can release those reservations atomically after this migration.
INSERT INTO downstream_key_hourly_usage(
  downstream_key_id,window_started_at,used_nano_usd,reserved_nano_usd,updated_at
)
SELECT
  downstream_key_id,
  started_at-(started_at%3600000),
  0,
  SUM(reservation_nano_usd),
  MAX(started_at)
FROM request_logs
WHERE status='running'
GROUP BY downstream_key_id,started_at-(started_at%3600000);

-- The multiplier is frozen at admission so editing a key affects only future
-- requests and every historical charge remains reproducible.
ALTER TABLE request_logs ADD COLUMN billing_multiplier_bps INTEGER NOT NULL DEFAULT 10000
  CHECK (billing_multiplier_bps BETWEEN 0 AND 10000000);
