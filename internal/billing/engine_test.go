package billing

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"
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
	quote, err := builtinEngine(t).Quote("gpt-5.6-luna", Usage{
		InputTokens: 100_000, CacheReadTokens: 10_000, CacheWriteTokens: 5_000,
		OutputTokens: 20_000, ReasoningTokens: 5_000,
	})
	if err != nil {
		t.Fatalf("Quote() error = %v", err)
	}
	if quote.Cost.Input.MicroUSD != 20_000 || quote.Cost.CacheRead.MicroUSD != 200 ||
		quote.Cost.CacheWrite.MicroUSD != 1_250 || quote.Cost.Output.MicroUSD != 25_000 || quote.Cost.Reasoning.MicroUSD != 6_250 {
		t.Fatalf("unexpected breakdown: %+v", quote.Cost)
	}
	if quote.Cost.Total != 52_700 {
		t.Fatalf("total = %d, want 52700", quote.Cost.Total)
	}
}

func TestQuoteConvertsCNYWithFrozenFX(t *testing.T) {
	quote, err := builtinEngine(t).Quote("deepseek-chat", Usage{
		InputTokens: 1_000_000, CacheReadTokens: 1_000_000, OutputTokens: 1_000_000,
	})
	if err != nil {
		t.Fatalf("Quote() error = %v", err)
	}
	if quote.Cost.Total != 765_889 {
		t.Fatalf("total = %d, want 765889", quote.Cost.Total)
	}
	if quote.Snapshot.FX == nil || quote.Snapshot.FX.UnitsPerUSD != "6.7895" || quote.Snapshot.FX.AsOf != "2026-08-06" {
		t.Fatalf("FX snapshot = %+v", quote.Snapshot.FX)
	}
	if quote.Cost.Input.MicroUSD+quote.Cost.CacheRead.MicroUSD+quote.Cost.CacheWrite.MicroUSD+quote.Cost.CacheWrite1H.MicroUSD+quote.Cost.Output.MicroUSD+quote.Cost.Reasoning.MicroUSD != quote.Cost.Total {
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
	if _, err := engine.Quote("qwen3.8-max", Usage{CacheReadTokens: 1}); !errors.Is(err, ErrCategoryUnpriced) {
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

func TestReservationUsesHighestPromptRate(t *testing.T) {
	reservation, err := builtinEngine(t).Reserve("claude-sonnet-5", Usage{InputTokens: 100_000})
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if reservation.MaximumUsage.CacheWrite1HTokens != 100_000 || reservation.MaximumUsage.InputTokens != 0 {
		t.Fatalf("reservation did not use the worst-case prompt category: %+v", reservation.MaximumUsage)
	}
	if reservation.ReservedMicroUSD != 400_000 {
		t.Fatalf("reserved = %d, want 400000", reservation.ReservedMicroUSD)
	}
}

func TestEffectivePricePeriodSwitchesAtBoundaryAndFreezesReservation(t *testing.T) {
	engine := builtinEngine(t)
	usage := Usage{InputTokens: 100_000, OutputTokens: 10_000}
	beforeTime := time.Date(2026, 8, 31, 23, 59, 59, 0, time.UTC)
	afterTime := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	before, err := engine.QuoteAt("claude-sonnet-5", usage, beforeTime)
	if err != nil {
		t.Fatalf("QuoteAt(before) error = %v", err)
	}
	after, err := engine.QuoteAt("claude-sonnet-5", usage, afterTime)
	if err != nil {
		t.Fatalf("QuoteAt(after) error = %v", err)
	}
	if before.AppliedBand.InputPerMillion != "2.00" || before.AppliedBand.OutputPerMillion != "10.00" ||
		before.Snapshot.EffectiveFrom != "2026-02-03" || before.Snapshot.EffectiveUntil != "2026-09-01" {
		t.Fatalf("before-boundary schedule = %+v", before.Snapshot)
	}
	if after.AppliedBand.InputPerMillion != "3.00" || after.AppliedBand.OutputPerMillion != "15.00" ||
		after.Snapshot.EffectiveFrom != "2026-09-01" || after.Snapshot.EffectiveUntil != "" {
		t.Fatalf("after-boundary schedule = %+v", after.Snapshot)
	}
	if before.Cost.Total != 300_000 || after.Cost.Total != 450_000 {
		t.Fatalf("boundary totals before=%d after=%d", before.Cost.Total, after.Cost.Total)
	}

	reservation, err := engine.ReserveAt("claude-sonnet-5", usage, beforeTime)
	if err != nil {
		t.Fatalf("ReserveAt(before) error = %v", err)
	}
	settlement, err := reservation.Settle(usage)
	if err != nil {
		t.Fatalf("Settle() error = %v", err)
	}
	if settlement.Quote.Snapshot.EffectiveUntil != "2026-09-01" || settlement.ChargedMicroUSD != before.Cost.Total {
		t.Fatalf("reservation did not retain its original price period: %+v", settlement)
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
	if !ok || priceSnapshot["catalog_version"] != "2026-08-06.lkg.3" || priceSnapshot["catalog_sha256"] == "" {
		t.Fatalf("snapshot metadata = %#v", value)
	}
	if _, ok := value["applied_band"]; !ok {
		t.Fatalf("snapshot does not contain applied_band: %#v", value)
	}
}

func TestRoundingOccursOnceAcrossComponents(t *testing.T) {
	quote, err := builtinEngine(t).Quote("gpt-5.4-nano", Usage{
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
	if _, err := engine.Quote("gpt-5.6-sol", Usage{InputTokens: -1}); !errors.Is(err, ErrInvalidUsage) {
		t.Fatalf("negative usage error = %v", err)
	}
	if _, err := engine.Quote("gpt-5.6-sol", Usage{InputTokens: math.MaxInt64, CacheReadTokens: 1}); !errors.Is(err, ErrInvalidUsage) {
		t.Fatalf("prompt overflow error = %v", err)
	}
	if _, err := engine.Quote("gpt-5.5-pro", Usage{OutputTokens: math.MaxInt64}); !errors.Is(err, ErrAmountOverflow) {
		t.Fatalf("amount overflow error = %v", err)
	}
}
