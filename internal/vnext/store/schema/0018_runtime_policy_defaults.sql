-- Upgrade only revision-1 bootstrap values. An administrator save advances
-- the settings revision, so explicit panel policy remains untouched.
UPDATE model_monitor_settings
SET interval_ms = 900000
WHERE interval_ms = 300000
  AND EXISTS (
    SELECT 1 FROM runtime_settings
    WHERE singleton_id = 1
      AND revision = 1
      AND probe_interval_ms = 300000
  );

UPDATE runtime_settings
SET cooldown_ms = CASE WHEN cooldown_ms = 300000 THEN 900000 ELSE cooldown_ms END,
    probe_interval_ms = CASE WHEN probe_interval_ms = 300000 THEN 900000 ELSE probe_interval_ms END
WHERE singleton_id = 1
  AND revision = 1;
