package billing

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func builtinEngine(t *testing.T) *Engine {
	t.Helper()
	engine, err := NewBuiltin()
	if err != nil {
		t.Fatalf("NewBuiltin() error = %v", err)
	}
	return engine
}

func TestQuoteUsesNonOverlappingTokenCategories(t *testing.T) {
	quote, err := builtinEngine(t).Quote("gpt-5.6", Usage{
		InputTokens: 1_000_000, CacheReadTokens: 100_000,
		OutputTokens: 200_000, ReasoningTokens: 50_000,
	})
	if err != nil {
		t.Fatalf("Quote() error = %v", err)
	}
	if quote.Cost.Input.MicroUSD != 2_500_000 || quote.Cost.CacheRead.MicroUSD != 25_000 ||
		quote.Cost.Output.MicroUSD != 3_000_000 || quote.Cost.Reasoning.MicroUSD != 750_000 {
		t.Fatalf("unexpected breakdown: %+v", quote.Cost)
	}
	if quote.Cost.Total != 6_275_000 {
		t.Fatalf("total = %d, want 6275000", quote.Cost.Total)
	}
}

func TestQuoteConvertsCNYWithFrozenFX(t *testing.T) {
	quote, err := builtinEngine(t).Quote("deepseek-chat", Usage{
		InputTokens: 1_000_000, CacheReadTokens: 1_000_000, OutputTokens: 1_000_000,
	})
	if err != nil {
		t.Fatalf("Quote() error = %v", err)
	}
	if quote.Cost.Total != 765_956 {
		t.Fatalf("total = %d, want 765956", quote.Cost.Total)
	}
	if quote.Snapshot.FX == nil || quote.Snapshot.FX.UnitsPerUSD != "6.7889" || quote.Snapshot.FX.AsOf != "2026-08-05" {
		t.Fatalf("FX snapshot = %+v", quote.Snapshot.FX)
	}
	if quote.Cost.Input.MicroUSD+quote.Cost.CacheRead.MicroUSD+quote.Cost.Output.MicroUSD+quote.Cost.Reasoning.MicroUSD != quote.Cost.Total {
		t.Fatalf("component sum does not equal rounded total: %+v", quote.Cost)
	}
}

func TestQuoteSelectsContextPriceBand(t *testing.T) {
	engine := builtinEngine(t)
	base, err := engine.Quote("gemini-2.5-pro", Usage{InputTokens: 200_000})
	if err != nil {
		t.Fatalf("base Quote() error = %v", err)
	}
	if base.AppliedBand.InputPerMillion != "1.25" {
		t.Fatalf("base rate = %q", base.AppliedBand.InputPerMillion)
	}

	long, err := engine.Quote("gemini-2.5-pro", Usage{InputTokens: 200_001})
	if err != nil {
		t.Fatalf("long Quote() error = %v", err)
	}
	if long.AppliedBand.InputPerMillion != "2.50" {
		t.Fatalf("long-context rate = %q", long.AppliedBand.InputPerMillion)
	}
}

func TestQuoteRejectsUnconfirmedRangeAndCategory(t *testing.T) {
	engine := builtinEngine(t)
	if _, err := engine.Quote("claude-sonnet-5", Usage{InputTokens: 200_001}); !errors.Is(err, ErrOutsidePriceRange) {
		t.Fatalf("long-context error = %v", err)
	}
	if _, err := engine.Quote("gemini-3-flash-preview", Usage{CacheReadTokens: 1}); !errors.Is(err, ErrCategoryUnpriced) {
		t.Fatalf("cache category error = %v", err)
	}
	if _, err := engine.Quote("glm-5", Usage{InputTokens: 1}); !errors.Is(err, ErrModelUnpriced) {
		t.Fatalf("unpriced model error = %v", err)
	}
	if _, err := engine.Quote("not-a-model", Usage{}); !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("unknown model error = %v", err)
	}
}

func TestReservationSettlesAgainstFrozenSchedule(t *testing.T) {
	reservation, err := builtinEngine(t).Reserve("gemini-2.5-pro", Usage{
		InputTokens: 200_001, OutputTokens: 10_000,
	})
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	settlement, err := reservation.Settle(Usage{InputTokens: 100_000, OutputTokens: 1_000})
	if err != nil {
		t.Fatalf("Settle() error = %v", err)
	}
	if settlement.Quote.AppliedBand.InputPerMillion != "1.25" {
		t.Fatalf("settlement used rate %q", settlement.Quote.AppliedBand.InputPerMillion)
	}
	if settlement.ChargedMicroUSD != 135_000 {
		t.Fatalf("charged = %d, want 135000", settlement.ChargedMicroUSD)
	}
	if settlement.ReleaseMicroUSD == 0 || settlement.AdditionalMicroUSD != 0 || settlement.DeltaMicroUSD >= 0 {
		t.Fatalf("unexpected settlement delta: %+v", settlement)
	}

	over, err := reservation.Settle(Usage{InputTokens: 300_000, OutputTokens: 20_000})
	if err != nil {
		t.Fatalf("overage Settle() error = %v", err)
	}
	if over.AdditionalMicroUSD == 0 || over.ReleaseMicroUSD != 0 || over.DeltaMicroUSD <= 0 {
		t.Fatalf("expected additional charge: %+v", over)
	}
}

func TestSnapshotJSONContainsAuditInputs(t *testing.T) {
	quote, err := builtinEngine(t).Quote("kimi-k2.6", Usage{InputTokens: 123, OutputTokens: 45})
	if err != nil {
		t.Fatalf("Quote() error = %v", err)
	}
	raw, err := quote.SnapshotJSON()
	if err != nil {
		t.Fatalf("SnapshotJSON() error = %v", err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("snapshot is not JSON: %v", err)
	}
	priceSnapshot, ok := value["price_snapshot"].(map[string]any)
	if !ok || priceSnapshot["catalog_version"] != "2026-08-05.lkg.1" || priceSnapshot["catalog_sha256"] == "" {
		t.Fatalf("snapshot metadata = %#v", value)
	}
	if _, ok := value["applied_band"]; !ok {
		t.Fatalf("snapshot does not contain applied_band: %#v", value)
	}
}

func TestRoundingOccursOnceAcrossComponents(t *testing.T) {
	quote, err := builtinEngine(t).Quote("gpt-5.6-nano", Usage{
		InputTokens: 1, CacheReadTokens: 1, OutputTokens: 1,
	})
	if err != nil {
		t.Fatalf("Quote() error = %v", err)
	}
	if quote.Cost.Total != 1 {
		t.Fatalf("total = %d, want one micro-USD", quote.Cost.Total)
	}
	if quote.Cost.Input.MicroUSD+quote.Cost.CacheRead.MicroUSD+quote.Cost.Output.MicroUSD != quote.Cost.Total {
		t.Fatalf("component allocation = %+v", quote.Cost)
	}
}

func TestInvalidUsageAndAmountOverflow(t *testing.T) {
	engine := builtinEngine(t)
	if _, err := engine.Quote("gpt-5.6", Usage{InputTokens: -1}); !errors.Is(err, ErrInvalidUsage) {
		t.Fatalf("negative usage error = %v", err)
	}
	if _, err := engine.Quote("gpt-5.6", Usage{InputTokens: math.MaxInt64, CacheReadTokens: 1}); !errors.Is(err, ErrInvalidUsage) {
		t.Fatalf("prompt overflow error = %v", err)
	}
	if _, err := engine.Quote("gpt-5.6-pro", Usage{OutputTokens: math.MaxInt64}); !errors.Is(err, ErrAmountOverflow) {
		t.Fatalf("amount overflow error = %v", err)
	}
}
