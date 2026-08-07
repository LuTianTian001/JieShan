-- Upgrade only the bootstrap policy. Any administrator save advances the
-- settings revision, so explicitly configured timeouts remain untouched.
UPDATE runtime_settings
SET first_output_timeout_ms = 15000
WHERE singleton_id = 1
  AND revision = 1
  AND first_output_timeout_ms = 30000;
