-- Migration 0013 originally created hourly quota buckets without materializing
-- reservations owned by requests that were already running. Reconcile every
-- existing bucket to the durable running-request ledger, then insert buckets
-- that are missing. Migrations run before the server accepts traffic, so the
-- request log is the complete reservation source of truth at this point.
UPDATE downstream_key_hourly_usage AS hourly
SET reserved_nano_usd = COALESCE((
      SELECT SUM(request.reservation_nano_usd)
      FROM request_logs AS request
      WHERE request.downstream_key_id=hourly.downstream_key_id
        AND request.status='running'
        AND request.started_at-(request.started_at%3600000)=hourly.window_started_at
    ),0);

INSERT INTO downstream_key_hourly_usage(
  downstream_key_id,window_started_at,used_nano_usd,reserved_nano_usd,updated_at
)
SELECT
  request.downstream_key_id,
  request.started_at-(request.started_at%3600000),
  0,
  SUM(request.reservation_nano_usd),
  MAX(request.started_at)
FROM request_logs AS request
WHERE request.status='running'
  AND NOT EXISTS (
    SELECT 1
    FROM downstream_key_hourly_usage AS hourly
    WHERE hourly.downstream_key_id=request.downstream_key_id
      AND hourly.window_started_at=request.started_at-(request.started_at%3600000)
  )
GROUP BY request.downstream_key_id,request.started_at-(request.started_at%3600000);
