package pricing

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

const (
	BuiltinOfficialCatalogVersion = "official-usd-2026-08-07-v4"
	builtinOfficialCatalogSource  = "JieShan bundled official API pricing snapshot"
	openAILongContextThreshold    = int64(272_000)
	xAILongContextThreshold       = int64(200_000)

	openAIOfficialPricingURL    = "https://developers.openai.com/api/docs/pricing"
	anthropicOfficialPricingURL = "https://platform.claude.com/docs/en/about-claude/pricing"
	geminiOfficialPricingURL    = "https://ai.google.dev/gemini-api/docs/pricing"
	deepSeekOfficialPricingURL  = "https://api-docs.deepseek.com/quick_start/pricing/"
	xAIOfficialPricingURL       = "https://docs.x.ai/developers/pricing"
)

var builtinOfficialVerifiedAt = time.Date(2026, time.August, 7, 0, 0, 0, 0, time.UTC)

// These canonical evidence records are intentionally stored beside the
// catalog. Their SHA-256 digests prove exactly which official price facts were
// reviewed without requiring network access during startup.
const openAIPricingEvidence = `source=https://developers.openai.com/api/docs/pricing
verified_at=2026-08-06
currency=USD
basis=standard API text-token prices per 1M tokens
long_context_rule=input+cached_input+cache_write tokens >272000 applies the long-context rates to the whole request
gpt-5.6-sol=input:5.00,cached_input:0.50,cache_write:6.25,output:30.00;long=input:10.00,cached_input:1.00,cache_write:12.50,output:45.00
gpt-5.6-terra=input:2.00,cached_input:0.20,cache_write:2.50,output:12.00;long=input:4.00,cached_input:0.40,cache_write:5.00,output:18.00
gpt-5.6-luna=input:0.20,cached_input:0.02,cache_write:0.25,output:1.20;long=input:0.40,cached_input:0.04,cache_write:0.50,output:1.80
gpt-5.5=input:5.00,cached_input:0.50,output:30.00;long=input:10.00,cached_input:1.00,output:45.00
gpt-5.5-pro=input:30.00,output:180.00;long=input:60.00,output:270.00
gpt-5.4=input:2.50,cached_input:0.25,output:15.00;long=input:5.00,cached_input:0.50,output:22.50
gpt-5.4-pro=input:30.00,output:180.00;long=input:60.00,output:270.00
gpt-5.4-mini=input:0.75,cached_input:0.075,output:4.50
gpt-5.4-nano=input:0.20,cached_input:0.02,output:1.25
gpt-5.3-codex=input:1.75,cached_input:0.175,output:14.00
gpt-5.2=input:1.75,cached_input:0.175,output:14.00
gpt-5.2-codex=input:1.75,cached_input:0.175,output:14.00
gpt-5.1=input:1.25,cached_input:0.125,output:10.00
gpt-5.1-codex=input:1.25,cached_input:0.125,output:10.00
gpt-5.1-codex-mini=input:0.25,cached_input:0.025,output:2.00
gpt-5=input:1.25,cached_input:0.125,output:10.00
gpt-5-mini=input:0.25,cached_input:0.025,output:2.00
gpt-5-nano=input:0.05,cached_input:0.005,output:0.40
gpt-4.1=input:2.00,cached_input:0.50,output:8.00
gpt-4.1-mini=input:0.40,cached_input:0.10,output:1.60
gpt-4.1-nano=input:0.10,cached_input:0.025,output:0.40
gpt-4o=input:2.50,cached_input:1.25,output:10.00
gpt-4o-mini=input:0.15,cached_input:0.075,output:0.60`

const anthropicPricingEvidence = `source=https://platform.claude.com/docs/en/about-claude/pricing
verified_at=2026-08-06
currency=USD
basis=standard API token prices per 1M tokens
claude-fable-5=input:10.00,cache_write_5m:12.50,cache_write_1h:20.00,cache_read:1.00,output:50.00
claude-opus-5=input:5.00,cache_write_5m:6.25,cache_write_1h:10.00,cache_read:0.50,output:25.00
claude-opus-4-8=input:5.00,cache_write_5m:6.25,cache_write_1h:10.00,cache_read:0.50,output:25.00
claude-opus-4-7=input:5.00,cache_write_5m:6.25,cache_write_1h:10.00,cache_read:0.50,output:25.00
claude-opus-4-6=input:5.00,cache_write_5m:6.25,cache_write_1h:10.00,cache_read:0.50,output:25.00
claude-sonnet-4-6=input:3.00,cache_write_5m:3.75,cache_write_1h:6.00,cache_read:0.30,output:15.00
claude-opus-4-5-20251101=input:5.00,cache_write_5m:6.25,cache_write_1h:10.00,cache_read:0.50,output:25.00
claude-haiku-4-5-20251001=input:1.00,cache_write_5m:1.25,cache_write_1h:2.00,cache_read:0.10,output:5.00
excluded=temporary Claude Sonnet 5 introductory pricing and long-context premium models are not included`

const geminiPricingEvidence = `source=https://ai.google.dev/gemini-api/docs/pricing
verified_at=2026-08-06
currency=USD
basis=standard paid-tier token prices per 1M tokens
gemini-3.6-flash=input:1.50,cache_read:0.15,output_including_thinking:7.50
gemini-3.5-flash=input:1.50,cache_read:0.15,output_including_thinking:9.00
gemini-3.5-flash-lite=input:0.30,cache_read:0.03,output_including_thinking:2.50
excluded=context-tiered or modality-dependent Gemini prices are not included`

const deepSeekPricingEvidence = `source=https://api-docs.deepseek.com/quick_start/pricing/
verified_at=2026-08-06
currency=USD
basis=official API token prices per 1M tokens
deepseek-v4-flash=input_cache_miss:0.14,input_cache_hit:0.0028,output:0.28
deepseek-v4-pro=input_cache_miss:0.435,input_cache_hit:0.003625,output:0.87
notice=official page states that a future price increase is planned but not yet specified`

const xAIPricingEvidence = `source=https://docs.x.ai/developers/pricing
verified_at=2026-08-07
currency=USD
basis=API text-token prices per 1M tokens
long_context_rule=prompt tokens >=200000 applies higher-context rates to the whole request
grok-4.5=input:2.00,cached_input:0.30,output:6.00,reasoning:6.00;long=input:4.00,cached_input:0.60,output:12.00,reasoning:12.00`

// BuiltinOfficialUSDCatalog returns a fresh copy of the immutable catalog
// bundled with this binary. A new version must be created for every factual
// price change; callers must never mutate an already-imported version.
func BuiltinOfficialUSDCatalog() Catalog {
	openAIDigest := evidenceDigest(openAIPricingEvidence)
	anthropicDigest := evidenceDigest(anthropicPricingEvidence)
	geminiDigest := evidenceDigest(geminiPricingEvidence)
	deepSeekDigest := evidenceDigest(deepSeekPricingEvidence)
	xAIDigest := evidenceDigest(xAIPricingEvidence)
	entries := []Entry{
		openAITieredEntry("gpt-5.6-sol", "5.00", "0.50", "6.25", "30.00", "10.00", "1.00", "12.50", "45.00", openAIDigest),
		openAITieredEntry("gpt-5.6-terra", "2.00", "0.20", "2.50", "12.00", "4.00", "0.40", "5.00", "18.00", openAIDigest),
		openAITieredEntry("gpt-5.6-luna", "0.20", "0.02", "0.25", "1.20", "0.40", "0.04", "0.50", "1.80", openAIDigest),
		openAITieredEntry("gpt-5.5", "5.00", "0.50", "", "30.00", "10.00", "1.00", "", "45.00", openAIDigest),
		openAITieredEntry("gpt-5.5-pro", "30.00", "", "", "180.00", "60.00", "", "", "270.00", openAIDigest),
		openAITieredEntry("gpt-5.4", "2.50", "0.25", "", "15.00", "5.00", "0.50", "", "22.50", openAIDigest),
		openAITieredEntry("gpt-5.4-pro", "30.00", "", "", "180.00", "60.00", "", "", "270.00", openAIDigest),
		openAIEntry("gpt-5.4-mini", "0.75", "0.075", "4.50", true, openAIDigest),
		openAIEntry("gpt-5.4-nano", "0.20", "0.02", "1.25", true, openAIDigest),
		openAIEntry("gpt-5.3-codex", "1.75", "0.175", "14.00", true, openAIDigest),
		openAIEntry("gpt-5.2", "1.75", "0.175", "14.00", true, openAIDigest),
		openAIEntry("gpt-5.2-codex", "1.75", "0.175", "14.00", true, openAIDigest),
		openAIEntry("gpt-5.1", "1.25", "0.125", "10.00", true, openAIDigest),
		openAIEntry("gpt-5.1-codex", "1.25", "0.125", "10.00", true, openAIDigest),
		openAIEntry("gpt-5.1-codex-mini", "0.25", "0.025", "2.00", true, openAIDigest),
		openAIEntry("gpt-5", "1.25", "0.125", "10.00", true, openAIDigest),
		openAIEntry("gpt-5-mini", "0.25", "0.025", "2.00", true, openAIDigest),
		openAIEntry("gpt-5-nano", "0.05", "0.005", "0.40", true, openAIDigest),
		openAIEntry("gpt-4.1", "2.00", "0.50", "8.00", false, openAIDigest),
		openAIEntry("gpt-4.1-mini", "0.40", "0.10", "1.60", false, openAIDigest),
		openAIEntry("gpt-4.1-nano", "0.10", "0.025", "0.40", false, openAIDigest),
		openAIEntry("gpt-4o", "2.50", "1.25", "10.00", false, openAIDigest),
		openAIEntry("gpt-4o-mini", "0.15", "0.075", "0.60", false, openAIDigest),
		anthropicEntry("claude-fable-5", "10.00", "12.50", "20.00", "1.00", "50.00", anthropicDigest),
		anthropicEntry("claude-opus-5", "5.00", "6.25", "10.00", "0.50", "25.00", anthropicDigest),
		anthropicEntry("claude-opus-4-8", "5.00", "6.25", "10.00", "0.50", "25.00", anthropicDigest),
		anthropicEntry("claude-opus-4-7", "5.00", "6.25", "10.00", "0.50", "25.00", anthropicDigest),
		anthropicEntry("claude-opus-4-6", "5.00", "6.25", "10.00", "0.50", "25.00", anthropicDigest),
		anthropicEntry("claude-sonnet-4-6", "3.00", "3.75", "6.00", "0.30", "15.00", anthropicDigest),
		anthropicEntry("claude-opus-4-5-20251101", "5.00", "6.25", "10.00", "0.50", "25.00", anthropicDigest),
		anthropicEntry("claude-haiku-4-5-20251001", "1.00", "1.25", "2.00", "0.10", "5.00", anthropicDigest),
		geminiEntry("gemini-3.6-flash", "1.50", "0.15", "7.50", geminiDigest),
		geminiEntry("gemini-3.5-flash", "1.50", "0.15", "9.00", geminiDigest),
		geminiEntry("gemini-3.5-flash-lite", "0.30", "0.03", "2.50", geminiDigest),
		deepSeekEntry("deepseek-v4-flash", "0.14", "0.0028", "0.28", deepSeekDigest),
		deepSeekEntry("deepseek-v4-pro", "0.435", "0.003625", "0.87", deepSeekDigest),
		xAIEntry("grok-4.5", "2.00", "0.30", "6.00", "4.00", "0.60", "12.00", xAIDigest),
	}
	return Catalog{
		SchemaVersion:      OfficialSchemaVersion,
		Version:            BuiltinOfficialCatalogVersion,
		SettlementCurrency: SettlementCurrencyUSD,
		Source:             builtinOfficialCatalogSource,
		SourceDigest: evidenceDigest(strings.Join([]string{
			openAIPricingEvidence, anthropicPricingEvidence, geminiPricingEvidence, deepSeekPricingEvidence, xAIPricingEvidence,
		}, "\n---\n")),
		FetchedAt:   builtinOfficialVerifiedAt,
		VerifiedAt:  builtinOfficialVerifiedAt,
		EffectiveAt: builtinOfficialVerifiedAt,
		Entries:     entries,
	}
}

func openAIEntry(sku, input, cacheRead, output string, reasoning bool, sourceDigest string) Entry {
	return builtinUSDEntry(sku, "openai", openAIOfficialPricingURL, sourceDigest, openAIRates(input, cacheRead, "", output, reasoning))
}

func openAITieredEntry(sku, input, cacheRead, cacheWrite, output, longInput, longCacheRead, longCacheWrite, longOutput, sourceDigest string) Entry {
	entry := builtinUSDEntry(sku, "openai", openAIOfficialPricingURL, sourceDigest, openAIRates(input, cacheRead, cacheWrite, output, true))
	entry.LongContext = &LongContextTier{
		ThresholdTokens: openAILongContextThreshold,
		Rates:           openAIRates(longInput, longCacheRead, longCacheWrite, longOutput, true),
	}
	return entry
}

func openAIRates(input, cacheRead, cacheWrite, output string, reasoning bool) []Rate {
	rates := []Rate{builtinRate(TokenInput, input)}
	if cacheRead != "" {
		rates = append(rates, builtinRate(TokenCacheRead, cacheRead))
	}
	if cacheWrite != "" {
		rates = append(rates, builtinRate(TokenCacheWrite, cacheWrite))
	}
	rates = append(rates, builtinRate(TokenOutput, output))
	if reasoning {
		rates = append(rates, builtinRate(TokenReasoning, output))
	}
	return rates
}

func anthropicEntry(sku, input, cacheWrite5m, cacheWrite1h, cacheRead, output, sourceDigest string) Entry {
	return builtinUSDEntry(sku, "anthropic", anthropicOfficialPricingURL, sourceDigest, []Rate{
		builtinRate(TokenInput, input),
		builtinRate(TokenCacheWrite5m, cacheWrite5m),
		builtinRate(TokenCacheWrite1h, cacheWrite1h),
		builtinRate(TokenCacheRead, cacheRead),
		builtinRate(TokenOutput, output),
		builtinRate(TokenReasoning, output),
	})
}

func geminiEntry(sku, input, cacheRead, output, sourceDigest string) Entry {
	return builtinUSDEntry(sku, "google", geminiOfficialPricingURL, sourceDigest, []Rate{
		builtinRate(TokenInput, input),
		builtinRate(TokenCacheRead, cacheRead),
		builtinRate(TokenOutput, output),
		builtinRate(TokenReasoning, output),
	})
}

func deepSeekEntry(sku, input, cacheRead, output, sourceDigest string) Entry {
	return builtinUSDEntry(sku, "deepseek", deepSeekOfficialPricingURL, sourceDigest, []Rate{
		builtinRate(TokenInput, input),
		builtinRate(TokenCacheRead, cacheRead),
		builtinRate(TokenOutput, output),
		builtinRate(TokenReasoning, output),
	})
}

func xAIEntry(sku, input, cacheRead, output, longInput, longCacheRead, longOutput, sourceDigest string) Entry {
	entry := builtinUSDEntry(sku, "xai", xAIOfficialPricingURL, sourceDigest, openAIRates(input, cacheRead, "", output, true))
	entry.LongContext = &LongContextTier{
		ThresholdTokens:    xAILongContextThreshold,
		ThresholdInclusive: true,
		Rates:              openAIRates(longInput, longCacheRead, "", longOutput, true),
	}
	return entry
}

func builtinUSDEntry(sku, provider, sourceURL, sourceDigest string, rates []Rate) Entry {
	return Entry{
		SKU:                sku,
		Provider:           provider,
		ModelPattern:       sku,
		PricingBasis:       FlatTokenPricingBasis,
		VerificationStatus: VerificationStatusVerified,
		SourceURL:          sourceURL,
		SourceDigest:       sourceDigest,
		VerifiedAt:         builtinOfficialVerifiedAt,
		NativeCurrency:     SettlementCurrencyUSD,
		USDPerNativeUnit:   "1",
		Rates:              rates,
	}
}

func builtinRate(class TokenClass, price string) Rate {
	return Rate{Class: class, NativePricePerMillion: price}
}

func evidenceDigest(value string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return CatalogDigestAlgorithm + ":" + hex.EncodeToString(digest[:])
}
