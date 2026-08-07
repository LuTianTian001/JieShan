package pricing

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCatalogRejectsDuplicateSKUs(t *testing.T) {
	entry := Entry{
		SKU:          "model-a",
		Provider:     "provider-a",
		ModelPattern: "model-a",
		Rates:        []Rate{{Class: TokenInput, NanoUSDPerMillion: 1}},
	}
	catalog := Catalog{
		Version:      "2026-08-06",
		Source:       "official-provider-pages",
		SourceDigest: "sha256:test",
		FetchedAt:    time.Now(),
		EffectiveAt:  time.Now(),
		Entries:      []Entry{entry, entry},
	}
	if err := catalog.Validate(); err == nil {
		t.Fatal("expected duplicate SKUs to fail validation")
	}
}

func TestCalculateChargeUsesEveryTokenClass(t *testing.T) {
	entry := Entry{
		SKU:          "model-a",
		Provider:     "provider-a",
		ModelPattern: "model-a",
		Rates: []Rate{
			{Class: TokenInput, NanoUSDPerMillion: 1_000_000_000},
			{Class: TokenOutput, NanoUSDPerMillion: 2_000_000_000},
			{Class: TokenReasoning, NanoUSDPerMillion: 3_000_000_000},
		},
	}
	charge, err := CalculateCharge("catalog-v1", entry, Usage{
		TokenInput:     1_000_000,
		TokenOutput:    500_000,
		TokenReasoning: 250_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if charge.NanoUSD != 2_750_000_000 {
		t.Fatalf("unexpected charge %d", charge.NanoUSD)
	}
	if charge.CatalogVersion != "catalog-v1" || charge.SKU != "model-a" {
		t.Fatalf("charge lost immutable pricing identity: %#v", charge)
	}
}

func TestCalculateChargeRejectsUnpricedUsage(t *testing.T) {
	entry := Entry{
		SKU:          "model-a",
		Provider:     "provider-a",
		ModelPattern: "model-a",
		Rates:        []Rate{{Class: TokenInput, NanoUSDPerMillion: 1}},
	}
	if _, err := CalculateCharge("catalog-v1", entry, Usage{TokenOutput: 1}); err == nil {
		t.Fatal("expected missing output rate to fail")
	}
}

func TestCalculateChargeSwitchesTheWholeRequestAboveLongContextThreshold(t *testing.T) {
	entry := Entry{
		SKU: "tiered", Provider: "provider-a", ModelPattern: "tiered",
		Rates: []Rate{
			{Class: TokenInput, NanoUSDPerMillion: 1_000_000_000},
			{Class: TokenCacheRead, NanoUSDPerMillion: 2_000_000_000},
			{Class: TokenCacheWrite, NanoUSDPerMillion: 3_000_000_000},
			{Class: TokenOutput, NanoUSDPerMillion: 4_000_000_000},
			{Class: TokenReasoning, NanoUSDPerMillion: 4_000_000_000},
		},
		LongContext: &LongContextTier{ThresholdTokens: 272_000, Rates: []Rate{
			{Class: TokenInput, NanoUSDPerMillion: 10_000_000_000},
			{Class: TokenCacheRead, NanoUSDPerMillion: 20_000_000_000},
			{Class: TokenCacheWrite, NanoUSDPerMillion: 30_000_000_000},
			{Class: TokenOutput, NanoUSDPerMillion: 40_000_000_000},
			{Class: TokenReasoning, NanoUSDPerMillion: 40_000_000_000},
		}},
	}
	atThreshold, err := CalculateCharge("tiered-v1", entry, Usage{
		TokenInput: 100_000, TokenCacheRead: 100_000, TokenCacheWrite: 72_000,
		TokenOutput: 10_000, TokenReasoning: 5_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if atThreshold.NanoUSD != 576_000_000 {
		t.Fatalf("charge at threshold = %d, want flat-tier 576000000", atThreshold.NanoUSD)
	}
	aboveThreshold, err := CalculateCharge("tiered-v1", entry, Usage{
		TokenInput: 100_000, TokenCacheRead: 100_000, TokenCacheWrite: 72_001,
		TokenOutput: 10_000, TokenReasoning: 5_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if aboveThreshold.NanoUSD != 5_760_030_000 {
		t.Fatalf("charge above threshold = %d, want whole-request long-tier 5760030000", aboveThreshold.NanoUSD)
	}
}

func TestCalculateChargeCountsTTLCacheWritesTowardLongContextThreshold(t *testing.T) {
	entry := Entry{
		SKU: "ttl-tiered", Provider: "provider-a", ModelPattern: "ttl-tiered",
		Rates: []Rate{
			{Class: TokenInput, NanoUSDPerMillion: 1_000_000_000},
			{Class: TokenOutput, NanoUSDPerMillion: 1_000_000_000},
			{Class: TokenCacheWrite5m, NanoUSDPerMillion: 1_000_000_000},
			{Class: TokenCacheWrite1h, NanoUSDPerMillion: 1_000_000_000},
		},
		LongContext: &LongContextTier{ThresholdTokens: 272_000, Rates: []Rate{
			{Class: TokenInput, NanoUSDPerMillion: 10_000_000_000},
			{Class: TokenOutput, NanoUSDPerMillion: 10_000_000_000},
			{Class: TokenCacheWrite5m, NanoUSDPerMillion: 10_000_000_000},
			{Class: TokenCacheWrite1h, NanoUSDPerMillion: 10_000_000_000},
		}},
	}
	atThreshold, err := CalculateCharge("tiered-v1", entry, Usage{TokenInput: 271_999, TokenCacheWrite5m: 1})
	if err != nil {
		t.Fatal(err)
	}
	aboveThreshold, err := CalculateCharge("tiered-v1", entry, Usage{
		TokenInput: 271_999, TokenCacheWrite5m: 1, TokenCacheWrite1h: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if atThreshold.NanoUSD != 272_000_000 || aboveThreshold.NanoUSD != 2_720_010_000 {
		t.Fatalf("TTL threshold charges = %d/%d", atThreshold.NanoUSD, aboveThreshold.NanoUSD)
	}
}

func TestCalculateChargeDoesNotFallBackToFlatRateInsideLongTier(t *testing.T) {
	entry := Entry{
		SKU: "strict-tier", Provider: "provider-a", ModelPattern: "strict-tier",
		Rates: []Rate{
			{Class: TokenInput, NanoUSDPerMillion: 1},
			{Class: TokenCacheRead, NanoUSDPerMillion: 1},
			{Class: TokenOutput, NanoUSDPerMillion: 1},
		},
		LongContext: &LongContextTier{ThresholdTokens: 10, Rates: []Rate{
			{Class: TokenInput, NanoUSDPerMillion: 2},
			{Class: TokenOutput, NanoUSDPerMillion: 2},
		}},
	}
	if _, err := CalculateCharge("tiered-v1", entry, Usage{TokenInput: 10, TokenCacheRead: 1}); err == nil {
		t.Fatal("missing verified long-context cache rate unexpectedly fell back to the flat tier")
	}
}

func TestConvertNativePriceUsesVersionedFXValue(t *testing.T) {
	// 10 CNY per million at 0.14 USD/CNY = 1.4 USD per million.
	rate, err := ConvertPerMillionToNanoUSD("10", "0.14")
	if err != nil {
		t.Fatal(err)
	}
	if rate != 1_400_000_000 {
		t.Fatalf("unexpected normalized rate %d", rate)
	}
}

func TestFractionalNanoUSDRoundsHalfUp(t *testing.T) {
	entry := Entry{
		SKU:          "tiny",
		Provider:     "provider-a",
		ModelPattern: "tiny",
		Rates:        []Rate{{Class: TokenInput, NanoUSDPerMillion: 1}},
	}
	below, err := CalculateCharge("v1", entry, Usage{TokenInput: 499_999})
	if err != nil {
		t.Fatal(err)
	}
	atHalf, err := CalculateCharge("v1", entry, Usage{TokenInput: 500_000})
	if err != nil {
		t.Fatal(err)
	}
	if below.NanoUSD != 0 || atHalf.NanoUSD != 1 {
		t.Fatalf("unexpected half-up rounding: below=%d atHalf=%d", below.NanoUSD, atHalf.NanoUSD)
	}
}

func TestPrepareOfficialCatalogFreezesSourceConversionAndDigest(t *testing.T) {
	now := time.Date(2026, time.August, 6, 10, 0, 0, 0, time.UTC)
	catalog := officialCatalogFixture("official-2026-08-06", now)
	catalog.Entries[0].NativeCurrency = "CNY"
	catalog.Entries[0].USDPerNativeUnit = "0.14"
	catalog.Entries[0].Rates = []Rate{
		{Class: TokenOutput, NativePricePerMillion: "20"},
		{Class: TokenInput, NativePricePerMillion: "10"},
	}
	catalog.FXVersion = "fx-2026-08-06"
	catalog.FXSourceURL = "https://central-bank.example/rates/2026-08-06"
	catalog.FXSourceDigest = shaDigest("b")
	catalog.FXVerifiedAt = now
	prepared, err := PrepareOfficialCatalog(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.SettlementCurrency != SettlementCurrencyUSD || prepared.SchemaVersion != OfficialSchemaVersion {
		t.Fatalf("catalog contract = %+v", prepared)
	}
	if prepared.Entries[0].Rates[0].NanoUSDPerMillion != 2_800_000_000 ||
		prepared.Entries[0].Rates[1].NanoUSDPerMillion != 1_400_000_000 {
		t.Fatalf("converted rates = %+v", prepared.Entries[0].Rates)
	}
	if !digestPattern.MatchString(prepared.Digest) {
		t.Fatalf("digest = %q", prepared.Digest)
	}

	reordered := prepared
	reordered.ImportedAt = now.Add(time.Hour)
	reordered.Entries[0].Rates[0], reordered.Entries[0].Rates[1] = reordered.Entries[0].Rates[1], reordered.Entries[0].Rates[0]
	digest, err := CatalogDigest(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if digest != prepared.Digest {
		t.Fatalf("canonical digest changed with order/repository metadata: %s != %s", digest, prepared.Digest)
	}
}

func TestPrepareOfficialCatalogFreezesAndCanonicalizesLongContextRates(t *testing.T) {
	now := time.Date(2026, time.August, 6, 10, 0, 0, 0, time.UTC)
	catalog := officialCatalogFixture("tiered-official", now)
	catalog.Entries[0].NativeCurrency = "CNY"
	catalog.Entries[0].USDPerNativeUnit = "0.14"
	catalog.Entries[0].LongContext = &LongContextTier{ThresholdTokens: 272_000, Rates: []Rate{
		{Class: TokenOutput, NativePricePerMillion: "40"},
		{Class: TokenInput, NativePricePerMillion: "20"},
	}}
	catalog.FXVersion = "fx-2026-08-06"
	catalog.FXSourceURL = "https://central-bank.example/rates/2026-08-06"
	catalog.FXSourceDigest = shaDigest("b")
	catalog.FXVerifiedAt = now
	prepared, err := PrepareOfficialCatalog(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Entries[0].LongContext.Rates[0].NanoUSDPerMillion != 0 {
		t.Fatal("preparing a catalog mutated caller-owned long-context rates")
	}
	if got := prepared.Entries[0].LongContext.Rates; got[0].NanoUSDPerMillion != 5_600_000_000 || got[1].NanoUSDPerMillion != 2_800_000_000 {
		t.Fatalf("converted long-context rates = %+v", got)
	}
	reordered := prepared
	reordered.Entries[0].LongContext.Rates[0], reordered.Entries[0].LongContext.Rates[1] =
		reordered.Entries[0].LongContext.Rates[1], reordered.Entries[0].LongContext.Rates[0]
	digest, err := CatalogDigest(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if digest != prepared.Digest {
		t.Fatalf("long-context rate ordering changed digest: %s != %s", digest, prepared.Digest)
	}
	changedThreshold := cloneCatalog(prepared)
	changedThreshold.Entries[0].LongContext.ThresholdTokens++
	digest, err = CatalogDigest(changedThreshold)
	if err != nil {
		t.Fatal(err)
	}
	if digest == prepared.Digest {
		t.Fatal("long-context threshold was omitted from the canonical digest")
	}
	changedInclusive := cloneCatalog(prepared)
	changedInclusive.Entries[0].LongContext.ThresholdInclusive = true
	digest, err = CatalogDigest(changedInclusive)
	if err != nil {
		t.Fatal(err)
	}
	if digest == prepared.Digest {
		t.Fatal("long-context threshold comparison was omitted from the canonical digest")
	}
}

func TestSchemaV1FlatCatalogEncodingAndDigestRemainStable(t *testing.T) {
	now := time.Date(2026, time.August, 6, 10, 0, 0, 0, time.UTC)
	prepared, err := PrepareOfficialCatalog(officialCatalogFixture("legacy-flat-v1", now))
	if err != nil {
		t.Fatal(err)
	}
	const legacyDigest = "sha256:a8ad80a4e5e9996117a4d9f315f9f01b5aa132f322687fed3626a33b048f0010"
	if prepared.Digest != legacyDigest {
		t.Fatalf("legacy flat digest = %q, want %q", prepared.Digest, legacyDigest)
	}
	payload, err := json.Marshal(prepared)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "long_context") {
		t.Fatalf("flat schema-v1 catalog gained a long-context field: %s", payload)
	}
}

func TestOfficialCatalogRejectsUnverifiedAndOverlappingPrices(t *testing.T) {
	now := time.Date(2026, time.August, 6, 10, 0, 0, 0, time.UTC)
	unverified := officialCatalogFixture("unverified", now)
	unverified.Entries[0].VerificationStatus = "estimated"
	if _, err := PrepareOfficialCatalog(unverified); err == nil {
		t.Fatal("unverified official price unexpectedly accepted")
	}

	overlap := officialCatalogFixture("overlap", now)
	overlap.Entries[0].Rates = append(overlap.Entries[0].Rates,
		Rate{Class: TokenCacheWrite, NativePricePerMillion: "1"},
		Rate{Class: TokenCacheWrite5m, NativePricePerMillion: "1.25"},
	)
	if _, err := PrepareOfficialCatalog(overlap); err == nil {
		t.Fatal("overlapping generic and TTL cache prices unexpectedly accepted")
	}
	if err := ValidateUsage(Usage{TokenCacheWrite: 1, TokenCacheWrite5m: 1}); err == nil {
		t.Fatal("overlapping cache usage unexpectedly accepted")
	}

	invalidTier := officialCatalogFixture("invalid-long-tier", now)
	invalidTier.Entries[0].LongContext = &LongContextTier{ThresholdTokens: 272_000, Rates: []Rate{
		{Class: TokenInput, NativePricePerMillion: "4"},
	}}
	if _, err := PrepareOfficialCatalog(invalidTier); err == nil {
		t.Fatal("long-context tier without an output price unexpectedly accepted")
	}
	invalidTier.Entries[0].LongContext.ThresholdTokens = 0
	if _, err := PrepareOfficialCatalog(invalidTier); err == nil {
		t.Fatal("non-positive long-context threshold unexpectedly accepted")
	}
}

func TestOfficialCatalogRejectsNonUSDWithoutFrozenFXEvidence(t *testing.T) {
	now := time.Date(2026, time.August, 6, 10, 0, 0, 0, time.UTC)
	catalog := officialCatalogFixture("missing-fx", now)
	catalog.Entries[0].NativeCurrency = "CNY"
	catalog.Entries[0].USDPerNativeUnit = "0.14"
	if _, err := PrepareOfficialCatalog(catalog); err == nil {
		t.Fatal("non-USD price without FX evidence unexpectedly accepted")
	}
}

func officialCatalogFixture(version string, now time.Time) Catalog {
	return Catalog{
		Version: version, Source: "operator-verified official pages", SourceDigest: shaDigest("a"),
		FetchedAt: now, VerifiedAt: now, EffectiveAt: now,
		Entries: []Entry{{
			SKU: "model-a", Provider: "provider-a", ModelPattern: "model-a",
			SourceURL: "https://provider.example/pricing", VerifiedAt: now,
			NativeCurrency: "USD", USDPerNativeUnit: "1",
			Rates: []Rate{
				{Class: TokenInput, NativePricePerMillion: "2"},
				{Class: TokenOutput, NativePricePerMillion: "6"},
			},
		}},
	}
}

func shaDigest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}
