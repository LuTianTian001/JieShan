-- Additive long-context pricing keeps every historical flat catalog row and
-- digest untouched. A tier applies only when its entry owns a positive input
-- threshold, and its rates remain immutable once the parent catalog is sealed.
ALTER TABLE price_catalog_entries
  ADD COLUMN long_context_threshold_tokens INTEGER
  CHECK (long_context_threshold_tokens IS NULL OR long_context_threshold_tokens > 0);

CREATE TABLE price_catalog_long_context_rates (
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
CREATE INDEX price_catalog_long_context_rates_order_idx
  ON price_catalog_long_context_rates(catalog_version, position, sku, token_class);

CREATE TRIGGER price_catalog_long_context_rates_threshold_insert
BEFORE INSERT ON price_catalog_long_context_rates
WHEN (
  SELECT long_context_threshold_tokens
  FROM price_catalog_entries
  WHERE catalog_version=NEW.catalog_version AND sku=NEW.sku
) IS NULL
BEGIN
  SELECT RAISE(ABORT, 'long-context rate requires an entry threshold');
END;
CREATE TRIGGER price_catalog_long_context_rates_sealed_insert
BEFORE INSERT ON price_catalog_long_context_rates
WHEN (SELECT sealed FROM price_catalogs WHERE version=NEW.catalog_version) <> 0
BEGIN
  SELECT RAISE(ABORT, 'sealed price catalog is immutable');
END;
CREATE TRIGGER price_catalog_long_context_rates_immutable_update
BEFORE UPDATE ON price_catalog_long_context_rates
BEGIN
  SELECT RAISE(ABORT, 'long-context price catalog rate is immutable');
END;
CREATE TRIGGER price_catalog_long_context_rates_immutable_delete
BEFORE DELETE ON price_catalog_long_context_rates
BEGIN
  SELECT RAISE(ABORT, 'long-context price catalog rate is immutable');
END;
