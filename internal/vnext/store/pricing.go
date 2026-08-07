package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/pricing"
)

func (s *Store) ListPriceCatalogs(ctx context.Context) ([]pricing.CatalogSummary, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT c.version,c.digest,c.settlement_currency,c.source_name,c.source_digest,
       COUNT(e.sku),c.effective_at,c.verified_at,c.imported_at,
       CASE WHEN state.active_version=c.version THEN 1 ELSE 0 END
FROM price_catalogs c
LEFT JOIN price_catalog_entries e ON e.catalog_version=c.version
CROSS JOIN price_catalog_state state
WHERE c.sealed=1 AND state.singleton_id=1
GROUP BY c.version,c.digest,c.settlement_currency,c.source_name,c.source_digest,
         c.effective_at,c.verified_at,c.imported_at,state.active_version
ORDER BY c.effective_at DESC,c.imported_at DESC,c.version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]pricing.CatalogSummary, 0)
	for rows.Next() {
		var item pricing.CatalogSummary
		var effectiveAt, verifiedAt, importedAt int64
		var active int
		if err := rows.Scan(
			&item.Version, &item.Digest, &item.SettlementCurrency, &item.Source, &item.SourceDigest,
			&item.EntryCount, &effectiveAt, &verifiedAt, &importedAt, &active,
		); err != nil {
			return nil, err
		}
		item.EffectiveAt = timeFromMS(effectiveAt)
		item.VerifiedAt = timeFromMS(verifiedAt)
		item.ImportedAt = timeFromMS(importedAt)
		item.Active = active == 1
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) GetPriceCatalog(ctx context.Context, version string) (pricing.Catalog, error) {
	version = strings.TrimSpace(version)
	var catalog pricing.Catalog
	var fxVersion, fxSourceURL, fxSourceDigest sql.NullString
	var fxVerifiedAt sql.NullInt64
	var fetchedAt, verifiedAt, effectiveAt, importedAt int64
	err := s.DB.QueryRowContext(ctx, `
SELECT schema_version,version,digest,settlement_currency,source_name,source_digest,
       fx_version,fx_source_url,fx_source_digest,fx_verified_at,
       fetched_at,verified_at,effective_at,imported_at
FROM price_catalogs WHERE version=? AND sealed=1`, version).Scan(
		&catalog.SchemaVersion, &catalog.Version, &catalog.Digest, &catalog.SettlementCurrency,
		&catalog.Source, &catalog.SourceDigest, &fxVersion, &fxSourceURL, &fxSourceDigest,
		&fxVerifiedAt, &fetchedAt, &verifiedAt, &effectiveAt, &importedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return pricing.Catalog{}, fmt.Errorf("%w: version %q", pricing.ErrCatalogNotFound, version)
	}
	if err != nil {
		return pricing.Catalog{}, err
	}
	catalog.FXVersion = fxVersion.String
	catalog.FXSourceURL = fxSourceURL.String
	catalog.FXSourceDigest = fxSourceDigest.String
	if fxVerifiedAt.Valid {
		catalog.FXVerifiedAt = timeFromMS(fxVerifiedAt.Int64)
	}
	catalog.FetchedAt = timeFromMS(fetchedAt)
	catalog.VerifiedAt = timeFromMS(verifiedAt)
	catalog.EffectiveAt = timeFromMS(effectiveAt)
	catalog.ImportedAt = timeFromMS(importedAt)

	rows, err := s.DB.QueryContext(ctx, `
SELECT sku,provider,model_pattern,pricing_basis,verification_status,source_url,
       source_digest,verified_at,native_currency,usd_per_native_unit,
       long_context_threshold_tokens,long_context_threshold_inclusive
FROM price_catalog_entries WHERE catalog_version=? ORDER BY position,sku`, version)
	if err != nil {
		return pricing.Catalog{}, err
	}
	for rows.Next() {
		var entry pricing.Entry
		var entryVerifiedAt int64
		var longContextThreshold sql.NullInt64
		var longContextThresholdInclusive int
		if err := rows.Scan(
			&entry.SKU, &entry.Provider, &entry.ModelPattern, &entry.PricingBasis,
			&entry.VerificationStatus, &entry.SourceURL, &entry.SourceDigest,
			&entryVerifiedAt, &entry.NativeCurrency, &entry.USDPerNativeUnit,
			&longContextThreshold, &longContextThresholdInclusive,
		); err != nil {
			_ = rows.Close()
			return pricing.Catalog{}, err
		}
		entry.VerifiedAt = timeFromMS(entryVerifiedAt)
		if longContextThreshold.Valid {
			entry.LongContext = &pricing.LongContextTier{
				ThresholdTokens:    longContextThreshold.Int64,
				ThresholdInclusive: longContextThresholdInclusive == 1,
			}
		}
		catalog.Entries = append(catalog.Entries, entry)
	}
	if err := rows.Close(); err != nil {
		return pricing.Catalog{}, err
	}
	if err := rows.Err(); err != nil {
		return pricing.Catalog{}, err
	}
	entryIndex := make(map[string]int, len(catalog.Entries))
	for index, entry := range catalog.Entries {
		entryIndex[strings.ToLower(entry.SKU)] = index
	}
	rateRows, err := s.DB.QueryContext(ctx, `
SELECT sku,token_class,native_price_per_million,nano_usd_per_million
FROM price_catalog_rates WHERE catalog_version=? ORDER BY position,sku,token_class`, version)
	if err != nil {
		return pricing.Catalog{}, err
	}
	for rateRows.Next() {
		var sku string
		var rate pricing.Rate
		if err := rateRows.Scan(&sku, &rate.Class, &rate.NativePricePerMillion, &rate.NanoUSDPerMillion); err != nil {
			_ = rateRows.Close()
			return pricing.Catalog{}, err
		}
		index, exists := entryIndex[strings.ToLower(sku)]
		if !exists {
			_ = rateRows.Close()
			return pricing.Catalog{}, errors.New("price rate references a missing catalog entry")
		}
		catalog.Entries[index].Rates = append(catalog.Entries[index].Rates, rate)
	}
	if err := rateRows.Err(); err != nil {
		_ = rateRows.Close()
		return pricing.Catalog{}, err
	}
	if err := rateRows.Close(); err != nil {
		return pricing.Catalog{}, err
	}
	longRateRows, err := s.DB.QueryContext(ctx, `
SELECT sku,token_class,native_price_per_million,nano_usd_per_million
FROM price_catalog_long_context_rates
WHERE catalog_version=? ORDER BY position,sku,token_class`, version)
	if err != nil {
		return pricing.Catalog{}, err
	}
	for longRateRows.Next() {
		var sku string
		var rate pricing.Rate
		if err := longRateRows.Scan(&sku, &rate.Class, &rate.NativePricePerMillion, &rate.NanoUSDPerMillion); err != nil {
			_ = longRateRows.Close()
			return pricing.Catalog{}, err
		}
		index, exists := entryIndex[strings.ToLower(sku)]
		if !exists || catalog.Entries[index].LongContext == nil {
			_ = longRateRows.Close()
			return pricing.Catalog{}, errors.New("long-context price rate references an entry without a threshold")
		}
		catalog.Entries[index].LongContext.Rates = append(catalog.Entries[index].LongContext.Rates, rate)
	}
	if err := longRateRows.Err(); err != nil {
		_ = longRateRows.Close()
		return pricing.Catalog{}, err
	}
	if err := longRateRows.Close(); err != nil {
		return pricing.Catalog{}, err
	}
	return catalog, nil
}

func (s *Store) GetPriceCatalogState(ctx context.Context) (pricing.CatalogState, error) {
	var state pricing.CatalogState
	var active sql.NullString
	var updatedAt int64
	if err := s.DB.QueryRowContext(ctx, `
SELECT active_version,revision,updated_at FROM price_catalog_state WHERE singleton_id=1`).Scan(
		&active, &state.Revision, &updatedAt,
	); err != nil {
		return pricing.CatalogState{}, err
	}
	state.ActiveVersion = active.String
	state.UpdatedAt = timeFromMS(updatedAt)
	return state, nil
}

func (s *Store) ImportPriceCatalog(ctx context.Context, input pricing.Catalog) (pricing.RepositoryImportResult, error) {
	catalog, err := pricing.PrepareOfficialCatalog(input)
	if err != nil {
		return pricing.RepositoryImportResult{}, err
	}
	if input.Digest != "" && input.Digest != catalog.Digest {
		return pricing.RepositoryImportResult{}, errors.New("price catalog digest changed during import")
	}
	if catalog.ImportedAt.IsZero() {
		catalog.ImportedAt = timeFromMS(NowMS())
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return pricing.RepositoryImportResult{}, err
	}
	defer tx.Rollback()
	var existingDigest string
	var existingSealed int
	err = tx.QueryRowContext(ctx, `SELECT digest,sealed FROM price_catalogs WHERE version=?`, catalog.Version).Scan(&existingDigest, &existingSealed)
	if err == nil {
		if existingDigest != catalog.Digest || existingSealed != 1 {
			return pricing.RepositoryImportResult{}, pricing.ErrCatalogVersionConflict
		}
		return pricing.RepositoryImportResult{Imported: false}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return pricing.RepositoryImportResult{}, err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO price_catalogs(
  version,schema_version,digest,settlement_currency,source_name,source_digest,
  fx_version,fx_source_url,fx_source_digest,fx_verified_at,
  fetched_at,verified_at,effective_at,imported_at,sealed
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,0)`,
		catalog.Version, catalog.SchemaVersion, catalog.Digest, catalog.SettlementCurrency,
		catalog.Source, catalog.SourceDigest, nullableString(catalog.FXVersion), nullableString(catalog.FXSourceURL),
		nullableString(catalog.FXSourceDigest), nullableTimeMS(catalog.FXVerifiedAt),
		catalog.FetchedAt.UnixMilli(), catalog.VerifiedAt.UnixMilli(), catalog.EffectiveAt.UnixMilli(),
		catalog.ImportedAt.UnixMilli(),
	)
	if err != nil {
		return pricing.RepositoryImportResult{}, fmt.Errorf("insert price catalog: %w", err)
	}
	for entryPosition, entry := range catalog.Entries {
		_, err = tx.ExecContext(ctx, `
INSERT INTO price_catalog_entries(
  catalog_version,sku,provider,model_pattern,pricing_basis,verification_status,
  source_url,source_digest,verified_at,native_currency,usd_per_native_unit,
  long_context_threshold_tokens,long_context_threshold_inclusive,position
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			catalog.Version, entry.SKU, entry.Provider, entry.ModelPattern, entry.PricingBasis,
			entry.VerificationStatus, entry.SourceURL, entry.SourceDigest, entry.VerifiedAt.UnixMilli(),
			entry.NativeCurrency, entry.USDPerNativeUnit, longContextThreshold(entry.LongContext),
			longContextThresholdInclusive(entry.LongContext), entryPosition,
		)
		if err != nil {
			return pricing.RepositoryImportResult{}, fmt.Errorf("insert price entry %q: %w", entry.SKU, err)
		}
		for ratePosition, rate := range entry.Rates {
			_, err = tx.ExecContext(ctx, `
INSERT INTO price_catalog_rates(
  catalog_version,sku,token_class,native_price_per_million,nano_usd_per_million,position
) VALUES (?,?,?,?,?,?)`,
				catalog.Version, entry.SKU, string(rate.Class), rate.NativePricePerMillion,
				rate.NanoUSDPerMillion, ratePosition,
			)
			if err != nil {
				return pricing.RepositoryImportResult{}, fmt.Errorf("insert price rate %q/%q: %w", entry.SKU, rate.Class, err)
			}
		}
		if entry.LongContext != nil {
			for ratePosition, rate := range entry.LongContext.Rates {
				_, err = tx.ExecContext(ctx, `
INSERT INTO price_catalog_long_context_rates(
  catalog_version,sku,token_class,native_price_per_million,nano_usd_per_million,position
) VALUES (?,?,?,?,?,?)`,
					catalog.Version, entry.SKU, string(rate.Class), rate.NativePricePerMillion,
					rate.NanoUSDPerMillion, ratePosition,
				)
				if err != nil {
					return pricing.RepositoryImportResult{}, fmt.Errorf("insert long-context price rate %q/%q: %w", entry.SKU, rate.Class, err)
				}
			}
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE price_catalogs SET sealed=1 WHERE version=? AND sealed=0`, catalog.Version)
	if err != nil {
		return pricing.RepositoryImportResult{}, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return pricing.RepositoryImportResult{}, errors.New("price catalog could not be sealed")
	}
	if err := tx.Commit(); err != nil {
		return pricing.RepositoryImportResult{}, err
	}
	return pricing.RepositoryImportResult{Imported: true}, nil
}

func (s *Store) ActivatePriceCatalog(ctx context.Context, version string, expectedRevision int64) (pricing.CatalogState, error) {
	version = strings.TrimSpace(version)
	if expectedRevision < 0 {
		return pricing.CatalogState{}, errors.New("price catalog state revision cannot be negative")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return pricing.CatalogState{}, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM price_catalogs WHERE version=? AND sealed=1`, version).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return pricing.CatalogState{}, fmt.Errorf("%w: version %q", pricing.ErrCatalogNotFound, version)
	} else if err != nil {
		return pricing.CatalogState{}, err
	}
	var active sql.NullString
	var revision, updatedAt int64
	if err := tx.QueryRowContext(ctx, `
SELECT active_version,revision,updated_at FROM price_catalog_state WHERE singleton_id=1`).Scan(
		&active, &revision, &updatedAt,
	); err != nil {
		return pricing.CatalogState{}, err
	}
	if revision != expectedRevision {
		return pricing.CatalogState{}, pricing.ErrCatalogStateConflict
	}
	if active.String == version {
		return pricing.CatalogState{ActiveVersion: version, Revision: revision, UpdatedAt: timeFromMS(updatedAt)}, nil
	}
	now := NowMS()
	result, err := tx.ExecContext(ctx, `
UPDATE price_catalog_state
SET active_version=?,revision=revision+1,updated_at=?
WHERE singleton_id=1 AND revision=?`, version, now, expectedRevision)
	if err != nil {
		return pricing.CatalogState{}, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return pricing.CatalogState{}, pricing.ErrCatalogStateConflict
	}
	if err := tx.Commit(); err != nil {
		return pricing.CatalogState{}, err
	}
	return pricing.CatalogState{ActiveVersion: version, Revision: revision + 1, UpdatedAt: timeFromMS(now)}, nil
}

func nullableTimeMS(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().UnixMilli()
}

func longContextThreshold(tier *pricing.LongContextTier) any {
	if tier == nil {
		return nil
	}
	return tier.ThresholdTokens
}

func longContextThresholdInclusive(tier *pricing.LongContextTier) int {
	if tier != nil && tier.ThresholdInclusive {
		return 1
	}
	return 0
}

func timeFromMS(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}

var _ pricing.Repository = (*Store)(nil)
