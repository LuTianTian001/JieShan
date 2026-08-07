package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/pricing"
)

func TestOfficialPriceCatalogLifecycleIsImmutableAndRestartSafe(t *testing.T) {
	ctx := context.Background()
	storage := openTestStoreAt(t, filepath.Join(t.TempDir(), "pricing.db"))
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	clock := now
	service, err := pricing.NewService(storage, pricing.WithClock(func() time.Time { return clock }))
	if err != nil {
		t.Fatal(err)
	}

	firstInput := storeOfficialCatalog("prices-1", "2", now)
	preview, err := service.Preview(ctx, firstInput)
	if err != nil {
		t.Fatal(err)
	}
	if preview.State.Revision != 0 || preview.State.ActiveVersion != "" ||
		preview.Diff.Summary.AddedEntries != 1 || !preview.CanActivate {
		t.Fatalf("first preview = %+v", preview)
	}
	imported, err := service.Import(ctx, firstInput, preview.Candidate.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if !imported.Imported || imported.Catalog.ImportedAt != now {
		t.Fatalf("import = %+v", imported)
	}
	clock = now.Add(time.Hour)
	idempotent, err := service.Import(ctx, firstInput, preview.Candidate.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if idempotent.Imported {
		t.Fatal("identical immutable catalog was inserted twice")
	}
	if idempotent.Catalog.ImportedAt != now {
		t.Fatalf("idempotent import did not return stored import time: %v", idempotent.Catalog.ImportedAt)
	}
	clock = now
	loaded, err := storage.GetPriceCatalog(ctx, "prices-1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Digest != preview.Candidate.Digest || len(loaded.Entries) != 1 || len(loaded.Entries[0].Rates) != 2 {
		t.Fatalf("loaded catalog = %+v", loaded)
	}
	if _, err := storage.DB.ExecContext(ctx, `UPDATE price_catalogs SET source_name='tampered' WHERE version='prices-1'`); err == nil {
		t.Fatal("sealed catalog row was mutable")
	}
	if _, err := storage.DB.ExecContext(ctx, `
INSERT INTO price_catalog_entries(
 catalog_version,sku,provider,model_pattern,pricing_basis,verification_status,
 source_url,source_digest,verified_at,native_currency,usd_per_native_unit,position
) VALUES ('prices-1','late','p','late','flat_tokens_per_million','verified',
 'https://provider.example/pricing',?,?,'USD','1',99)`, storePriceDigest("c"), now.UnixMilli()); err == nil {
		t.Fatal("entry was appended to a sealed catalog")
	}

	state, err := service.Activate(ctx, "prices-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveVersion != "prices-1" || state.Revision != 1 {
		t.Fatalf("state = %+v", state)
	}

	secondInput := storeOfficialCatalog("prices-2", "4", now)
	secondPreview, err := service.Preview(ctx, secondInput)
	if err != nil {
		t.Fatal(err)
	}
	if secondPreview.Diff.Summary.ChangedEntries != 1 || secondPreview.Diff.Summary.AddedEntries != 0 {
		t.Fatalf("second diff = %+v", secondPreview.Diff)
	}
	if _, err := service.Import(ctx, secondInput, secondPreview.Candidate.Digest); err != nil {
		t.Fatal(err)
	}
	state, err = service.Activate(ctx, "prices-2", 1)
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveVersion != "prices-2" || state.Revision != 2 {
		t.Fatalf("state = %+v", state)
	}
	if _, err := service.Activate(ctx, "prices-1", 1); !errors.Is(err, pricing.ErrCatalogStateConflict) {
		t.Fatalf("stale activation error = %v", err)
	}

	book, err := service.BuildBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	quote, err := book.Quote("model-a", pricing.Usage{pricing.TokenInput: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if quote.CatalogVersion != "prices-2" || quote.ReservationNanoUSD != 4_000_000_000 {
		t.Fatalf("active quote = %+v", quote)
	}
	historical, err := book.Charge("prices-1", "model-a", pricing.Usage{pricing.TokenInput: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if historical.NanoUSD != 2_000_000_000 {
		t.Fatalf("historical charge = %+v", historical)
	}

	conflict := storeOfficialCatalog("prices-1", "9", now)
	conflictPreview, err := service.Preview(ctx, conflict)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Import(ctx, conflict, conflictPreview.Candidate.Digest); !errors.Is(err, pricing.ErrCatalogVersionConflict) {
		t.Fatalf("version reuse error = %v", err)
	}
}

func TestFutureOfficialPriceCatalogCanImportButCannotActivate(t *testing.T) {
	ctx := context.Background()
	storage := openTestStoreAt(t, filepath.Join(t.TempDir(), "future-pricing.db"))
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	service, err := pricing.NewService(storage, pricing.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	catalog := storeOfficialCatalog("future-prices", "3", now)
	catalog.EffectiveAt = now.Add(24 * time.Hour)
	preview, err := service.Preview(ctx, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if preview.CanActivate {
		t.Fatal("future catalog reported activatable")
	}
	if _, err := service.Import(ctx, catalog, preview.Candidate.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Activate(ctx, catalog.Version, 0); !errors.Is(err, pricing.ErrCatalogNotEffective) {
		t.Fatalf("activation error = %v", err)
	}
}

func TestTieredOfficialPriceCatalogIsImmutableAndRestartSafe(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tiered-pricing.db")
	storage := openTestStoreAt(t, path)
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	service, err := pricing.NewService(storage, pricing.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	catalog := storeOfficialCatalog("tiered-prices", "2", now)
	catalog.Entries[0].LongContext = &pricing.LongContextTier{ThresholdTokens: 272_000, ThresholdInclusive: true, Rates: []pricing.Rate{
		{Class: pricing.TokenInput, NativePricePerMillion: "4"},
		{Class: pricing.TokenOutput, NativePricePerMillion: "12"},
	}}
	preview, err := service.Preview(ctx, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Import(ctx, catalog, preview.Candidate.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Activate(ctx, catalog.Version, 0); err != nil {
		t.Fatal(err)
	}
	loaded, err := storage.GetPriceCatalog(ctx, catalog.Version)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Entries[0].LongContext == nil || loaded.Entries[0].LongContext.ThresholdTokens != 272_000 ||
		!loaded.Entries[0].LongContext.ThresholdInclusive ||
		len(loaded.Entries[0].LongContext.Rates) != 2 ||
		loaded.Entries[0].LongContext.Rates[0].NanoUSDPerMillion != 4_000_000_000 {
		t.Fatalf("loaded tiered catalog = %+v", loaded)
	}
	if _, err := storage.DB.ExecContext(ctx, `
INSERT INTO price_catalog_long_context_rates(
 catalog_version,sku,token_class,native_price_per_million,nano_usd_per_million,position
) VALUES ('tiered-prices','model-a','cache_read','1',1000000000,9)`); err == nil {
		t.Fatal("long-context rate was appended to a sealed catalog")
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted, err := pricing.NewService(reopened, pricing.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	book, err := restarted.BuildBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	short, err := book.Charge(catalog.Version, "model-a", pricing.Usage{pricing.TokenInput: 271_999})
	if err != nil {
		t.Fatal(err)
	}
	long, err := book.Charge(catalog.Version, "model-a", pricing.Usage{pricing.TokenInput: 272_000})
	if err != nil {
		t.Fatal(err)
	}
	if short.NanoUSD != 543_998_000 || long.NanoUSD != 1_088_000_000 {
		t.Fatalf("restart tiered charges = %d/%d", short.NanoUSD, long.NanoUSD)
	}
}

func TestTieredPriceMigrationPreservesHistoricalFlatCatalog(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "flat-v10.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (
version INTEGER PRIMARY KEY,
name TEXT NOT NULL,
applied_at INTEGER NOT NULL
)`); err != nil {
		t.Fatal(err)
	}
	for _, item := range migrations[:10] {
		if _, err := db.ExecContext(ctx, item.sql); err != nil {
			t.Fatalf("apply historical migration %d: %v", item.version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,applied_at) VALUES (?,?,?)`, item.version, item.name, 0); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)
	flat, err := pricing.PrepareOfficialCatalog(storeOfficialCatalog("historical-flat", "2", now))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO price_catalogs(
 version,schema_version,digest,settlement_currency,source_name,source_digest,
 fetched_at,verified_at,effective_at,imported_at,sealed
) VALUES (?,?,?,?,?,?,?,?,?,?,0)`,
		flat.Version, flat.SchemaVersion, flat.Digest, flat.SettlementCurrency, flat.Source, flat.SourceDigest,
		flat.FetchedAt.UnixMilli(), flat.VerifiedAt.UnixMilli(), flat.EffectiveAt.UnixMilli(), now.UnixMilli(),
	); err != nil {
		t.Fatal(err)
	}
	entry := flat.Entries[0]
	if _, err := db.ExecContext(ctx, `
INSERT INTO price_catalog_entries(
 catalog_version,sku,provider,model_pattern,pricing_basis,verification_status,
 source_url,source_digest,verified_at,native_currency,usd_per_native_unit,position
) VALUES (?,?,?,?,?,?,?,?,?,?,?,0)`,
		flat.Version, entry.SKU, entry.Provider, entry.ModelPattern, entry.PricingBasis,
		entry.VerificationStatus, entry.SourceURL, entry.SourceDigest, entry.VerifiedAt.UnixMilli(),
		entry.NativeCurrency, entry.USDPerNativeUnit,
	); err != nil {
		t.Fatal(err)
	}
	for position, rate := range entry.Rates {
		if _, err := db.ExecContext(ctx, `
INSERT INTO price_catalog_rates(
 catalog_version,sku,token_class,native_price_per_million,nano_usd_per_million,position
) VALUES (?,?,?,?,?,?)`, flat.Version, entry.SKU, string(rate.Class), rate.NativePricePerMillion, rate.NanoUSDPerMillion, position); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE price_catalogs SET sealed=1 WHERE version=?`, flat.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE price_catalog_state SET active_version=?,revision=1,updated_at=? WHERE singleton_id=1`, flat.Version, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	loaded, err := upgraded.GetPriceCatalog(ctx, flat.Version)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SchemaVersion != pricing.OfficialSchemaVersion || loaded.Digest != flat.Digest ||
		len(loaded.Entries) != 1 || loaded.Entries[0].LongContext != nil {
		t.Fatalf("upgraded historical catalog = %+v", loaded)
	}
	prepared, err := pricing.PrepareOfficialCatalog(loaded)
	if err != nil || prepared.Digest != flat.Digest {
		t.Fatalf("historical digest after migration = %q, %v", prepared.Digest, err)
	}
	var version int
	if err := upgraded.DB.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 21 {
		t.Fatalf("upgraded migration version = %d, %v", version, err)
	}
}

func TestRuntimePriceServiceAppliesActivationWithoutRestart(t *testing.T) {
	ctx := context.Background()
	storage := openTestStoreAt(t, filepath.Join(t.TempDir(), "runtime-pricing.db"))
	now := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)
	setup, err := pricing.NewService(storage, pricing.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	first := storeOfficialCatalog("runtime-prices-1", "2", now)
	preview, err := setup.Preview(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Import(ctx, first, preview.Candidate.Digest); err != nil {
		t.Fatal(err)
	}
	service, err := pricing.NewRuntimeService(ctx, storage, pricing.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	items, inactiveState, err := service.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Version != first.Version || inactiveState.ActiveVersion != "" {
		t.Fatalf("custom inactive catalog was overridden by bootstrap: %#v, %+v", items, inactiveState)
	}
	if _, err := service.Quote("model-a", pricing.Usage{pricing.TokenInput: 1}); !errors.Is(err, pricing.ErrNoActiveCatalog) {
		t.Fatalf("empty runtime quote error = %v", err)
	}
	if _, err := service.Activate(ctx, first.Version, 0); err != nil {
		t.Fatal(err)
	}
	quote, err := service.Quote("model-a", pricing.Usage{pricing.TokenInput: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if quote.CatalogVersion != first.Version || quote.ReservationNanoUSD != 2_000_000_000 {
		t.Fatalf("first runtime quote = %+v", quote)
	}

	second := storeOfficialCatalog("runtime-prices-2", "4", now)
	preview, err = service.Preview(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Import(ctx, second, preview.Candidate.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Activate(ctx, second.Version, 1); err != nil {
		t.Fatal(err)
	}
	quote, err = service.Quote("model-a", pricing.Usage{pricing.TokenInput: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if quote.CatalogVersion != second.Version || quote.ReservationNanoUSD != 4_000_000_000 {
		t.Fatalf("second runtime quote = %+v", quote)
	}
	historical, err := service.Charge(first.Version, "model-a", pricing.Usage{pricing.TokenInput: 1_000_000})
	if err != nil || historical.NanoUSD != 2_000_000_000 {
		t.Fatalf("historical runtime charge = %+v, %v", historical, err)
	}
}

func TestRuntimePriceServiceBootstrapsBuiltinCatalogExactlyOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "builtin-pricing.db")
	now := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)
	storage := openTestStoreAt(t, path)
	service, err := pricing.NewRuntimeService(ctx, storage, pricing.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	state, err := service.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveVersion != pricing.BuiltinOfficialCatalogVersion || state.Revision != 1 {
		t.Fatalf("bootstrap state = %+v", state)
	}
	quote, err := service.Quote("gpt-5.4-mini", pricing.Usage{pricing.TokenInput: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if quote.CatalogVersion != pricing.BuiltinOfficialCatalogVersion || quote.ReservationNanoUSD != 1_500_000_000 {
		t.Fatalf("built-in quote = %+v", quote)
	}
	if _, err := service.Quote("unknown-model", pricing.Usage{pricing.TokenInput: 1}); !errors.Is(err, pricing.ErrPriceUnavailable) {
		t.Fatalf("unknown price error = %v", err)
	}

	service, err = pricing.NewRuntimeService(ctx, storage, pricing.WithClock(func() time.Time { return now.Add(time.Hour) }))
	if err != nil {
		t.Fatal(err)
	}
	items, restartedState, err := service.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || restartedState.Revision != 1 || restartedState.ActiveVersion != pricing.BuiltinOfficialCatalogVersion {
		t.Fatalf("restart catalog state = %#v, %+v", items, restartedState)
	}
}

func TestRuntimePriceServiceRecoversBuiltinImportBeforeActivation(t *testing.T) {
	ctx := context.Background()
	storage := openTestStoreAt(t, filepath.Join(t.TempDir(), "builtin-recovery.db"))
	now := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)
	setup, err := pricing.NewService(storage, pricing.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	catalog := pricing.BuiltinOfficialUSDCatalog()
	preview, err := setup.Preview(ctx, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Import(ctx, catalog, preview.Candidate.Digest); err != nil {
		t.Fatal(err)
	}

	service, err := pricing.NewRuntimeService(ctx, storage, pricing.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	state, err := service.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveVersion != pricing.BuiltinOfficialCatalogVersion || state.Revision != 1 {
		t.Fatalf("recovered state = %+v", state)
	}
}

func storeOfficialCatalog(version, inputPrice string, now time.Time) pricing.Catalog {
	return pricing.Catalog{
		Version: version, Source: "operator-verified official pages", SourceDigest: storePriceDigest("a"),
		FetchedAt: now, VerifiedAt: now, EffectiveAt: now,
		Entries: []pricing.Entry{{
			SKU: "model-a", Provider: "provider-a", ModelPattern: "model-a",
			SourceURL: "https://provider.example/pricing", NativeCurrency: "USD", USDPerNativeUnit: "1",
			Rates: []pricing.Rate{
				{Class: pricing.TokenInput, NativePricePerMillion: inputPrice},
				{Class: pricing.TokenOutput, NativePricePerMillion: "6"},
			},
		}},
	}
}

func storePriceDigest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}
