package pricing

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBuiltinOfficialCatalogIsDeterministicAndSourceBacked(t *testing.T) {
	first, err := PrepareOfficialCatalog(BuiltinOfficialUSDCatalog())
	if err != nil {
		t.Fatal(err)
	}
	second, err := PrepareOfficialCatalog(BuiltinOfficialUSDCatalog())
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != BuiltinOfficialCatalogVersion || first.Digest != second.Digest {
		t.Fatalf("built-in catalog identity is not deterministic: %q/%q and %q/%q", first.Version, first.Digest, second.Version, second.Digest)
	}
	const expectedDigest = "sha256:8724268b46b824d17d6c33e0ddb441b9fe49a9eac766f1c2a1e3b2b2863d146f"
	if first.Digest != expectedDigest {
		t.Fatalf("built-in catalog changed without a version bump: got %q, want %q", first.Digest, expectedDigest)
	}
	if len(first.Entries) != 36 {
		t.Fatalf("built-in entry count = %d, want 36", len(first.Entries))
	}
	if first.SourceDigest != evidenceDigest(stringsJoinEvidence()) {
		t.Fatalf("built-in source digest = %q", first.SourceDigest)
	}
	for _, entry := range first.Entries {
		if entry.VerifiedAt != builtinOfficialVerifiedAt || entry.NativeCurrency != "USD" || entry.USDPerNativeUnit != "1" {
			t.Fatalf("entry %q lost fixed verification evidence: %+v", entry.SKU, entry)
		}
		switch entry.Provider {
		case "openai":
			if entry.SourceURL != openAIOfficialPricingURL {
				t.Fatalf("OpenAI source URL = %q", entry.SourceURL)
			}
		case "anthropic":
			if entry.SourceURL != anthropicOfficialPricingURL {
				t.Fatalf("Anthropic source URL = %q", entry.SourceURL)
			}
		case "google":
			if entry.SourceURL != geminiOfficialPricingURL {
				t.Fatalf("Gemini source URL = %q", entry.SourceURL)
			}
		case "deepseek":
			if entry.SourceURL != deepSeekOfficialPricingURL {
				t.Fatalf("DeepSeek source URL = %q", entry.SourceURL)
			}
		default:
			t.Fatalf("unexpected built-in provider %q", entry.Provider)
		}
	}
}

func TestBuiltinOfficialCatalogChargesKnownModelsAndRejectsUnknown(t *testing.T) {
	catalog, err := PrepareOfficialCatalog(BuiltinOfficialUSDCatalog())
	if err != nil {
		t.Fatal(err)
	}
	book, err := NewBook(catalog)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		sku   string
		usage Usage
		want  int64
	}{
		{sku: "gpt-5.4-mini", usage: Usage{TokenInput: 1_000_000, TokenCacheRead: 1_000_000, TokenOutput: 1_000_000, TokenReasoning: 1_000_000}, want: 9_825_000_000},
		{sku: "claude-sonnet-4-6", usage: Usage{TokenInput: 1_000_000, TokenCacheWrite5m: 1_000_000, TokenCacheRead: 1_000_000, TokenOutput: 1_000_000}, want: 22_050_000_000},
		{sku: "gemini-3.6-flash", usage: Usage{TokenInput: 1_000_000, TokenCacheRead: 1_000_000, TokenReasoning: 1_000_000}, want: 9_150_000_000},
		{sku: "deepseek-v4-flash", usage: Usage{TokenInput: 1_000_000, TokenCacheRead: 1_000_000, TokenOutput: 1_000_000}, want: 422_800_000},
	}
	for _, test := range tests {
		charge, err := book.Charge(catalog.Version, test.sku, test.usage)
		if err != nil {
			t.Fatalf("Charge(%q) error = %v", test.sku, err)
		}
		if charge.NanoUSD != test.want {
			t.Fatalf("Charge(%q) = %d, want %d", test.sku, charge.NanoUSD, test.want)
		}
	}
	short, err := book.Quote("gpt-5.6-sol", Usage{TokenInput: 272_000})
	if err != nil || short.ReservationNanoUSD != 1_360_000_000 {
		t.Fatalf("gpt-5.6-sol short quote = %+v, %v", short, err)
	}
	long, err := book.Quote("gpt-5.6-sol", Usage{TokenInput: 272_001, TokenOutput: 1, TokenReasoning: 1})
	if err != nil || long.ReservationNanoUSD != 2_720_100_000 {
		t.Fatalf("gpt-5.6-sol long quote = %+v, %v", long, err)
	}
	cacheWrite, err := book.Quote("gpt-5.6-sol", Usage{TokenCacheWrite: 272_001})
	if err != nil || cacheWrite.ReservationNanoUSD != 3_400_012_500 {
		t.Fatalf("gpt-5.6-sol cache-write quote = %+v, %v", cacheWrite, err)
	}
	if _, err := book.Quote("gpt-5.5-pro", Usage{TokenCacheRead: 1}); !errors.Is(err, ErrPriceUnavailable) {
		t.Fatalf("unpublished gpt-5.5-pro cache price error = %v", err)
	}
	if _, err := book.Quote("unknown-model", Usage{TokenInput: 1}); !errors.Is(err, ErrPriceUnavailable) {
		t.Fatalf("unknown model error = %v", err)
	}
}

func TestBuiltinOpenAIFlagshipStandardAndLongContextRates(t *testing.T) {
	catalog, err := PrepareOfficialCatalog(BuiltinOfficialUSDCatalog())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		sku   string
		short map[TokenClass]string
		long  map[TokenClass]string
	}{
		{
			sku:   "gpt-5.6-sol",
			short: map[TokenClass]string{TokenInput: "5.00", TokenCacheRead: "0.50", TokenCacheWrite: "6.25", TokenOutput: "30.00", TokenReasoning: "30.00"},
			long:  map[TokenClass]string{TokenInput: "10.00", TokenCacheRead: "1.00", TokenCacheWrite: "12.50", TokenOutput: "45.00", TokenReasoning: "45.00"},
		},
		{
			sku:   "gpt-5.6-terra",
			short: map[TokenClass]string{TokenInput: "2.00", TokenCacheRead: "0.20", TokenCacheWrite: "2.50", TokenOutput: "12.00", TokenReasoning: "12.00"},
			long:  map[TokenClass]string{TokenInput: "4.00", TokenCacheRead: "0.40", TokenCacheWrite: "5.00", TokenOutput: "18.00", TokenReasoning: "18.00"},
		},
		{
			sku:   "gpt-5.6-luna",
			short: map[TokenClass]string{TokenInput: "0.20", TokenCacheRead: "0.02", TokenCacheWrite: "0.25", TokenOutput: "1.20", TokenReasoning: "1.20"},
			long:  map[TokenClass]string{TokenInput: "0.40", TokenCacheRead: "0.04", TokenCacheWrite: "0.50", TokenOutput: "1.80", TokenReasoning: "1.80"},
		},
		{
			sku:   "gpt-5.5",
			short: map[TokenClass]string{TokenInput: "5.00", TokenCacheRead: "0.50", TokenOutput: "30.00", TokenReasoning: "30.00"},
			long:  map[TokenClass]string{TokenInput: "10.00", TokenCacheRead: "1.00", TokenOutput: "45.00", TokenReasoning: "45.00"},
		},
		{
			sku:   "gpt-5.5-pro",
			short: map[TokenClass]string{TokenInput: "30.00", TokenOutput: "180.00", TokenReasoning: "180.00"},
			long:  map[TokenClass]string{TokenInput: "60.00", TokenOutput: "270.00", TokenReasoning: "270.00"},
		},
		{
			sku:   "gpt-5.4",
			short: map[TokenClass]string{TokenInput: "2.50", TokenCacheRead: "0.25", TokenOutput: "15.00", TokenReasoning: "15.00"},
			long:  map[TokenClass]string{TokenInput: "5.00", TokenCacheRead: "0.50", TokenOutput: "22.50", TokenReasoning: "22.50"},
		},
		{
			sku:   "gpt-5.4-pro",
			short: map[TokenClass]string{TokenInput: "30.00", TokenOutput: "180.00", TokenReasoning: "180.00"},
			long:  map[TokenClass]string{TokenInput: "60.00", TokenOutput: "270.00", TokenReasoning: "270.00"},
		},
	}
	entries := make(map[string]Entry, len(catalog.Entries))
	for _, entry := range catalog.Entries {
		entries[entry.SKU] = entry
	}
	for _, test := range tests {
		entry, ok := entries[test.sku]
		if !ok || entry.LongContext == nil || entry.LongContext.ThresholdTokens != openAILongContextThreshold {
			t.Fatalf("tiered entry %q = %+v", test.sku, entry)
		}
		assertNativeRates(t, test.sku+" short", entry.Rates, test.short)
		assertNativeRates(t, test.sku+" long", entry.LongContext.Rates, test.long)
	}
}

func assertNativeRates(t *testing.T, label string, rates []Rate, expected map[TokenClass]string) {
	t.Helper()
	actual := make(map[TokenClass]string, len(rates))
	for _, rate := range rates {
		actual[rate.Class] = rate.NativePricePerMillion
	}
	if len(actual) != len(expected) {
		t.Fatalf("%s rate count = %d, want %d: %+v", label, len(actual), len(expected), actual)
	}
	for class, want := range expected {
		if actual[class] != want {
			t.Fatalf("%s %s price = %q, want %q", label, class, actual[class], want)
		}
	}
}

func TestBuiltinBootstrapDoesNotClaimAConcurrentOperatorActivation(t *testing.T) {
	repository := &operatorWinsRepository{}
	service, err := NewService(repository, WithClock(func() time.Time {
		return time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.EnsureBuiltinOfficialCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Imported || result.Activated || result.State.ActiveVersion != "operator-catalog" || result.State.Revision != 1 {
		t.Fatalf("bootstrap conflict result = %+v", result)
	}
}

func TestBuiltinBootstrapUpgradesOnlyAnOlderBundledCatalog(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	olderInput := BuiltinOfficialUSDCatalog()
	olderInput.Version = "official-usd-2026-08-06-v2"
	older, err := PrepareOfficialCatalog(olderInput)
	if err != nil {
		t.Fatal(err)
	}
	older.ImportedAt = now.Add(-time.Hour)
	repository := newMemoryCatalogRepository(older, CatalogState{
		ActiveVersion: older.Version, Revision: 1, UpdatedAt: now.Add(-time.Hour),
	})
	service, err := NewService(repository, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.EnsureBuiltinOfficialCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Imported || !result.Activated || result.State.ActiveVersion != BuiltinOfficialCatalogVersion || result.State.Revision != 2 {
		t.Fatalf("upgrade result = %+v", result)
	}

	operatorInput := BuiltinOfficialUSDCatalog()
	operatorInput.Version = "operator-catalog"
	operatorInput.Source = "Operator maintained official prices"
	operator, err := PrepareOfficialCatalog(operatorInput)
	if err != nil {
		t.Fatal(err)
	}
	operatorRepository := newMemoryCatalogRepository(operator, CatalogState{
		ActiveVersion: operator.Version, Revision: 7, UpdatedAt: now,
	})
	operatorService, err := NewService(operatorRepository, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	operatorResult, err := operatorService.EnsureBuiltinOfficialCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if operatorResult.Imported || operatorResult.Activated || operatorResult.State.ActiveVersion != operator.Version || len(operatorRepository.catalogs) != 1 {
		t.Fatalf("operator catalog was replaced: result=%+v catalogs=%d", operatorResult, len(operatorRepository.catalogs))
	}
}

func stringsJoinEvidence() string {
	return openAIPricingEvidence + "\n---\n" + anthropicPricingEvidence + "\n---\n" + geminiPricingEvidence + "\n---\n" + deepSeekPricingEvidence
}

type operatorWinsRepository struct {
	catalog Catalog
	state   CatalogState
}

type memoryCatalogRepository struct {
	catalogs map[string]Catalog
	state    CatalogState
}

func newMemoryCatalogRepository(catalog Catalog, state CatalogState) *memoryCatalogRepository {
	return &memoryCatalogRepository{catalogs: map[string]Catalog{catalog.Version: catalog}, state: state}
}

func (repository *memoryCatalogRepository) ListPriceCatalogs(context.Context) ([]CatalogSummary, error) {
	items := make([]CatalogSummary, 0, len(repository.catalogs))
	for _, catalog := range repository.catalogs {
		items = append(items, CatalogSummary{
			Version: catalog.Version, Digest: catalog.Digest, SettlementCurrency: catalog.SettlementCurrency,
			Source: catalog.Source, SourceDigest: catalog.SourceDigest, EntryCount: len(catalog.Entries),
			EffectiveAt: catalog.EffectiveAt, VerifiedAt: catalog.VerifiedAt, ImportedAt: catalog.ImportedAt,
			Active: repository.state.ActiveVersion == catalog.Version,
		})
	}
	return items, nil
}

func (repository *memoryCatalogRepository) GetPriceCatalog(_ context.Context, version string) (Catalog, error) {
	catalog, ok := repository.catalogs[version]
	if !ok {
		return Catalog{}, ErrCatalogNotFound
	}
	return catalog, nil
}

func (repository *memoryCatalogRepository) GetPriceCatalogState(context.Context) (CatalogState, error) {
	return repository.state, nil
}

func (repository *memoryCatalogRepository) ImportPriceCatalog(_ context.Context, catalog Catalog) (RepositoryImportResult, error) {
	if existing, ok := repository.catalogs[catalog.Version]; ok {
		if existing.Digest != catalog.Digest {
			return RepositoryImportResult{}, ErrCatalogVersionConflict
		}
		return RepositoryImportResult{}, nil
	}
	repository.catalogs[catalog.Version] = catalog
	return RepositoryImportResult{Imported: true}, nil
}

func (repository *memoryCatalogRepository) ActivatePriceCatalog(_ context.Context, version string, expectedRevision int64) (CatalogState, error) {
	if repository.state.Revision != expectedRevision {
		return CatalogState{}, ErrCatalogStateConflict
	}
	if _, ok := repository.catalogs[version]; !ok {
		return CatalogState{}, ErrCatalogNotFound
	}
	repository.state.ActiveVersion = version
	repository.state.Revision++
	repository.state.UpdatedAt = time.Date(2026, time.August, 6, 12, 0, 1, 0, time.UTC)
	return repository.state, nil
}

func (*operatorWinsRepository) ListPriceCatalogs(context.Context) ([]CatalogSummary, error) {
	return nil, nil
}

func (repository *operatorWinsRepository) GetPriceCatalog(_ context.Context, version string) (Catalog, error) {
	if repository.catalog.Version != version {
		return Catalog{}, ErrCatalogNotFound
	}
	return repository.catalog, nil
}

func (repository *operatorWinsRepository) GetPriceCatalogState(context.Context) (CatalogState, error) {
	return repository.state, nil
}

func (repository *operatorWinsRepository) ImportPriceCatalog(_ context.Context, catalog Catalog) (RepositoryImportResult, error) {
	repository.catalog = catalog
	return RepositoryImportResult{Imported: true}, nil
}

func (repository *operatorWinsRepository) ActivatePriceCatalog(context.Context, string, int64) (CatalogState, error) {
	repository.state = CatalogState{
		ActiveVersion: "operator-catalog",
		Revision:      1,
		UpdatedAt:     time.Date(2026, time.August, 6, 12, 0, 1, 0, time.UTC),
	}
	return CatalogState{}, ErrCatalogStateConflict
}
