-- Historical catalogs used a strict greater-than long-context threshold.
-- Preserve that zero-value behavior while allowing newer catalogs to declare
-- a greater-than-or-equal boundary as part of their canonical content.
ALTER TABLE price_catalog_entries
  ADD COLUMN long_context_threshold_inclusive INTEGER NOT NULL DEFAULT 0
  CHECK (long_context_threshold_inclusive IN (0,1));
