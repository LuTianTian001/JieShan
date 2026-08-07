package pricing

import (
	"errors"
	"testing"
	"time"
)

func TestBookFreezesCatalogForReservationAndHistoricalSettlement(t *testing.T) {
	first := testCatalog("prices-1", "digest-1", 2_000_000_000)
	book, err := NewBook(first)
	if err != nil {
		t.Fatal(err)
	}
	quote, err := book.Quote("MODEL-A", Usage{TokenInput: 1_000, TokenOutput: 2_000})
	if err != nil {
		t.Fatal(err)
	}
	if quote.CatalogVersion != "prices-1" || quote.SKU != "model-a" || quote.ReservationNanoUSD != 6_000_000 {
		t.Fatalf("quote = %+v", quote)
	}

	second := testCatalog("prices-2", "digest-2", 4_000_000_000)
	if err := book.Install(second, true); err != nil {
		t.Fatal(err)
	}
	newQuote, err := book.Quote("model-a", Usage{TokenInput: 1_000, TokenOutput: 2_000})
	if err != nil {
		t.Fatal(err)
	}
	if newQuote.CatalogVersion != "prices-2" || newQuote.ReservationNanoUSD != 12_000_000 {
		t.Fatalf("new quote = %+v", newQuote)
	}
	historical, err := book.Charge(quote.CatalogVersion, quote.SKU, Usage{TokenInput: 1_000, TokenOutput: 1_000})
	if err != nil {
		t.Fatal(err)
	}
	if historical.CatalogVersion != "prices-1" || historical.NanoUSD != 4_000_000 {
		t.Fatalf("historical charge = %+v", historical)
	}
}

func TestBookRejectsVersionReuseWithDifferentImmutableSource(t *testing.T) {
	book, err := NewBook(testCatalog("prices-1", "digest-1", 1_000_000_000))
	if err != nil {
		t.Fatal(err)
	}
	if err := book.Install(testCatalog("prices-1", "different", 1_000_000_000), false); err == nil {
		t.Fatal("reused catalog version with different source digest")
	}
}

func TestBookRejectsVersionReuseWhenRatesChangeButSourceLabelDoesNot(t *testing.T) {
	first := testCatalog("prices-1", "digest-1", 1_000_000_000)
	book, err := NewBook(first)
	if err != nil {
		t.Fatal(err)
	}
	changed := testCatalog("prices-1", "digest-1", 2_000_000_000)
	if err := book.Install(changed, false); !errors.Is(err, ErrCatalogVersionConflict) {
		t.Fatalf("error = %v, want immutable version conflict", err)
	}
}

func TestInstallingInactiveCatalogDoesNotInventAnActiveVersion(t *testing.T) {
	book := NewEmptyBook()
	if err := book.Install(testCatalog("prices-1", "digest-1", 1_000_000_000), false); err != nil {
		t.Fatal(err)
	}
	if book.CurrentVersion() != "" {
		t.Fatalf("current version = %q", book.CurrentVersion())
	}
	if _, err := book.Quote("model-a", Usage{TokenInput: 1}); !errors.Is(err, ErrNoActiveCatalog) {
		t.Fatalf("quote error = %v", err)
	}
}

func TestBookDeepClonesLongContextRates(t *testing.T) {
	catalog := testCatalog("tiered-prices", "digest-tiered", 1_000_000_000)
	catalog.Entries[0].LongContext = &LongContextTier{
		ThresholdTokens: 272_000,
		Rates: []Rate{
			{Class: TokenInput, NanoUSDPerMillion: 2_000_000_000},
			{Class: TokenOutput, NanoUSDPerMillion: 2_000_000_000},
		},
	}
	book, err := NewBook(catalog)
	if err != nil {
		t.Fatal(err)
	}
	first := book.Catalogs()
	first[0].Entries[0].LongContext.ThresholdTokens = 1
	first[0].Entries[0].LongContext.Rates[0].NanoUSDPerMillion = 99
	second := book.Catalogs()
	longContext := second[0].Entries[0].LongContext
	if longContext == nil || longContext.ThresholdTokens != 272_000 || longContext.Rates[0].NanoUSDPerMillion != 2_000_000_000 {
		t.Fatalf("book snapshot was mutated through returned catalog: %+v", longContext)
	}
}

func testCatalog(version, digest string, rate int64) Catalog {
	now := time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)
	return Catalog{
		Version: version, Source: "official", SourceDigest: digest, FXVersion: "usd",
		FetchedAt: now, EffectiveAt: now,
		Entries: []Entry{{
			SKU: "model-a", Provider: "provider", ModelPattern: "model-a",
			Rates: []Rate{{Class: TokenInput, NanoUSDPerMillion: rate}, {Class: TokenOutput, NanoUSDPerMillion: rate}},
		}},
	}
}
