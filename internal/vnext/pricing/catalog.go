package pricing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	NanoUSDPerUSD      int64 = 1_000_000_000
	TokensPerMillion   int64 = 1_000_000
	maxCatalogEntries        = 10_000
	maxDecimalLength         = 128
	maxSourceURLLength       = 4_096

	OfficialSchemaVersion      = 1
	SettlementCurrencyUSD      = "USD"
	FlatTokenPricingBasis      = "flat_tokens_per_million"
	VerificationStatusVerified = "verified"
	CatalogDigestAlgorithm     = "sha256"
)

var (
	decimalPattern    = regexp.MustCompile(`^(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$`)
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	currencyPattern   = regexp.MustCompile(`^[A-Z]{3}$`)
	digestPattern     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type TokenClass string

const (
	TokenInput        TokenClass = "input"
	TokenOutput       TokenClass = "output"
	TokenCacheRead    TokenClass = "cache_read"
	TokenCacheWrite   TokenClass = "cache_write"
	TokenCacheWrite5m TokenClass = "cache_write_5m"
	TokenCacheWrite1h TokenClass = "cache_write_1h"
	TokenReasoning    TokenClass = "reasoning"
)

type Rate struct {
	Class                 TokenClass `json:"class"`
	NativePricePerMillion string     `json:"native_price_per_million,omitempty"`
	NanoUSDPerMillion     int64      `json:"nano_usd_per_million"`
}

// LongContextTier applies to the whole request when the sum of every
// input-side token class crosses ThresholdTokens. ThresholdInclusive selects
// greater-than-or-equal comparison; the zero value preserves the historical
// strictly-greater-than behavior. Keeping the tier optional preserves the
// canonical JSON and digest of historical flat schema-v1 catalogs.
type LongContextTier struct {
	ThresholdTokens    int64  `json:"threshold_tokens"`
	ThresholdInclusive bool   `json:"threshold_inclusive,omitempty"`
	Rates              []Rate `json:"rates"`
}

type Entry struct {
	SKU                string           `json:"sku"`
	Provider           string           `json:"provider"`
	ModelPattern       string           `json:"model_pattern"`
	PricingBasis       string           `json:"pricing_basis,omitempty"`
	VerificationStatus string           `json:"verification_status,omitempty"`
	SourceURL          string           `json:"source_url,omitempty"`
	SourceDigest       string           `json:"source_digest,omitempty"`
	VerifiedAt         time.Time        `json:"verified_at,omitempty"`
	NativeCurrency     string           `json:"native_currency,omitempty"`
	USDPerNativeUnit   string           `json:"usd_per_native_unit,omitempty"`
	Rates              []Rate           `json:"rates"`
	LongContext        *LongContextTier `json:"long_context,omitempty"`
}

func (e Entry) Validate() error {
	if strings.TrimSpace(e.SKU) == "" {
		return errors.New("price SKU is required")
	}
	if strings.TrimSpace(e.Provider) == "" {
		return errors.New("price provider is required")
	}
	if len(e.Provider) > 128 {
		return errors.New("price provider exceeds 128 bytes")
	}
	if strings.TrimSpace(e.ModelPattern) == "" {
		return errors.New("price model pattern is required")
	}
	if len(e.ModelPattern) > 512 {
		return errors.New("price model pattern exceeds 512 bytes")
	}
	if err := validateRateSet(e.Rates); err != nil {
		return err
	}
	if e.LongContext != nil {
		if e.LongContext.ThresholdTokens <= 0 {
			return errors.New("long-context threshold must be positive")
		}
		if err := validateRateSet(e.LongContext.Rates); err != nil {
			return fmt.Errorf("long-context rates: %w", err)
		}
	}
	return nil
}

func validateRateSet(rates []Rate) error {
	if len(rates) == 0 {
		return errors.New("at least one token rate is required")
	}
	if len(rates) > 7 {
		return errors.New("price entry contains too many token rates")
	}
	seen := make(map[TokenClass]struct{}, len(rates))
	for _, rate := range rates {
		if !knownTokenClass(rate.Class) {
			return fmt.Errorf("unknown token class %q", rate.Class)
		}
		if rate.NanoUSDPerMillion < 0 {
			return fmt.Errorf("negative rate for token class %q", rate.Class)
		}
		if _, exists := seen[rate.Class]; exists {
			return fmt.Errorf("duplicate rate for token class %q", rate.Class)
		}
		seen[rate.Class] = struct{}{}
	}
	return nil
}

type Catalog struct {
	SchemaVersion      int       `json:"schema_version,omitempty"`
	Version            string    `json:"version"`
	Digest             string    `json:"digest,omitempty"`
	SettlementCurrency string    `json:"settlement_currency,omitempty"`
	Source             string    `json:"source"`
	SourceDigest       string    `json:"source_digest"`
	FXVersion          string    `json:"fx_version,omitempty"`
	FXSourceURL        string    `json:"fx_source_url,omitempty"`
	FXSourceDigest     string    `json:"fx_source_digest,omitempty"`
	FXVerifiedAt       time.Time `json:"fx_verified_at,omitempty"`
	FetchedAt          time.Time `json:"fetched_at"`
	VerifiedAt         time.Time `json:"verified_at,omitempty"`
	EffectiveAt        time.Time `json:"effective_at"`
	ImportedAt         time.Time `json:"imported_at,omitempty"`
	Entries            []Entry   `json:"entries"`
}

// Validate checks the settlement data used by the in-memory calculator. It is
// intentionally compatible with trusted, in-process test catalogs. External
// administration must call PrepareOfficialCatalog, whose contract is strict.
func (c Catalog) Validate() error {
	if strings.TrimSpace(c.Version) == "" {
		return errors.New("catalog version is required")
	}
	if strings.TrimSpace(c.Source) == "" {
		return errors.New("catalog source is required")
	}
	if len(c.Source) > 256 {
		return errors.New("catalog source exceeds 256 bytes")
	}
	if strings.TrimSpace(c.SourceDigest) == "" {
		return errors.New("catalog source digest is required")
	}
	if c.FetchedAt.IsZero() || c.EffectiveAt.IsZero() {
		return errors.New("catalog timestamps are required")
	}
	if len(c.Entries) == 0 {
		return errors.New("catalog must contain price entries")
	}
	if len(c.Entries) > maxCatalogEntries {
		return fmt.Errorf("catalog exceeds the %d entry limit", maxCatalogEntries)
	}
	seen := make(map[string]struct{}, len(c.Entries))
	for index, entry := range c.Entries {
		if err := entry.Validate(); err != nil {
			return fmt.Errorf("entry %d: %w", index, err)
		}
		key := normalizeSKU(entry.SKU)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate price SKU %q", entry.SKU)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// PrepareOfficialCatalog normalizes and validates an operator-supplied,
// source-backed official price manifest. It computes the USD rates and a
// canonical content digest; no network lookup or implicit exchange rate is
// performed.
func PrepareOfficialCatalog(input Catalog) (Catalog, error) {
	catalog := normalizeCatalog(input)
	if catalog.SchemaVersion == 0 {
		catalog.SchemaVersion = OfficialSchemaVersion
	}
	if catalog.SettlementCurrency == "" {
		catalog.SettlementCurrency = SettlementCurrencyUSD
	}
	for index := range catalog.Entries {
		entry := &catalog.Entries[index]
		if entry.PricingBasis == "" {
			entry.PricingBasis = FlatTokenPricingBasis
		}
		if entry.VerificationStatus == "" {
			entry.VerificationStatus = VerificationStatusVerified
		}
		if entry.SourceDigest == "" {
			entry.SourceDigest = catalog.SourceDigest
		}
		if entry.VerifiedAt.IsZero() {
			entry.VerifiedAt = catalog.VerifiedAt
		}
		if entry.NativeCurrency == SettlementCurrencyUSD && entry.USDPerNativeUnit == "" {
			entry.USDPerNativeUnit = "1"
		}
		if err := prepareRateSet(entry.SKU, "", entry.USDPerNativeUnit, entry.Rates); err != nil {
			return Catalog{}, err
		}
		if entry.LongContext != nil {
			if err := prepareRateSet(entry.SKU, "long-context ", entry.USDPerNativeUnit, entry.LongContext.Rates); err != nil {
				return Catalog{}, err
			}
		}
	}
	if err := validateOfficialCatalog(catalog); err != nil {
		return Catalog{}, err
	}
	digest, err := CatalogDigest(catalog)
	if err != nil {
		return Catalog{}, err
	}
	if catalog.Digest != "" && catalog.Digest != digest {
		return Catalog{}, errors.New("catalog digest does not match canonical content")
	}
	catalog.Digest = digest
	return catalog, nil
}

func prepareRateSet(sku, label, usdPerNativeUnit string, rates []Rate) error {
	for index := range rates {
		rate := &rates[index]
		if strings.TrimSpace(rate.NativePricePerMillion) == "" {
			return fmt.Errorf("entry %q %srate %q: native price is required", sku, label, rate.Class)
		}
		converted, err := ConvertPerMillionToNanoUSD(rate.NativePricePerMillion, usdPerNativeUnit)
		if err != nil {
			return fmt.Errorf("entry %q %srate %q: %w", sku, label, rate.Class, err)
		}
		if rate.NanoUSDPerMillion != 0 && rate.NanoUSDPerMillion != converted {
			return fmt.Errorf("entry %q %srate %q: supplied USD rate does not match the frozen conversion", sku, label, rate.Class)
		}
		rate.NanoUSDPerMillion = converted
	}
	return nil
}

func validateOfficialCatalog(catalog Catalog) error {
	if err := catalog.Validate(); err != nil {
		return err
	}
	if catalog.SchemaVersion != OfficialSchemaVersion {
		return fmt.Errorf("unsupported official price schema version %d", catalog.SchemaVersion)
	}
	if !identifierPattern.MatchString(catalog.Version) {
		return errors.New("catalog version must be a stable identifier")
	}
	if catalog.SettlementCurrency != SettlementCurrencyUSD {
		return errors.New("official downstream settlement currency must be USD")
	}
	if len(catalog.FXVersion) > 128 {
		return errors.New("FX snapshot version exceeds 128 bytes")
	}
	if !digestPattern.MatchString(catalog.SourceDigest) {
		return errors.New("catalog source digest must be a lowercase sha256 digest")
	}
	if catalog.VerifiedAt.IsZero() {
		return errors.New("catalog verification timestamp is required")
	}
	usesFX := false
	for index, entry := range catalog.Entries {
		if !identifierPattern.MatchString(entry.SKU) {
			return fmt.Errorf("entry %d: SKU must be a stable identifier", index)
		}
		if entry.PricingBasis != FlatTokenPricingBasis {
			return fmt.Errorf("entry %q: unsupported pricing basis %q", entry.SKU, entry.PricingBasis)
		}
		if entry.VerificationStatus != VerificationStatusVerified {
			return fmt.Errorf("entry %q: price must be explicitly verified", entry.SKU)
		}
		if err := validateHTTPSURL(entry.SourceURL); err != nil {
			return fmt.Errorf("entry %q source URL: %w", entry.SKU, err)
		}
		if !digestPattern.MatchString(entry.SourceDigest) {
			return fmt.Errorf("entry %q: source digest must be a lowercase sha256 digest", entry.SKU)
		}
		if entry.VerifiedAt.IsZero() {
			return fmt.Errorf("entry %q: verification timestamp is required", entry.SKU)
		}
		if !currencyPattern.MatchString(entry.NativeCurrency) {
			return fmt.Errorf("entry %q: native currency must be an uppercase ISO 4217 code", entry.SKU)
		}
		fx, err := parseDecimal(entry.USDPerNativeUnit)
		if err != nil || fx.Sign() <= 0 {
			return fmt.Errorf("entry %q: USD conversion must be a positive exact decimal", entry.SKU)
		}
		if entry.NativeCurrency == SettlementCurrencyUSD {
			if fx.Cmp(big.NewRat(1, 1)) != 0 {
				return fmt.Errorf("entry %q: USD prices must use a 1:1 conversion", entry.SKU)
			}
		} else {
			usesFX = true
		}
		if err := validateOfficialRateSet(entry.SKU, "", entry.Rates); err != nil {
			return err
		}
		if entry.LongContext != nil {
			if err := validateOfficialRateSet(entry.SKU, "long-context ", entry.LongContext.Rates); err != nil {
				return err
			}
		}
	}
	if usesFX {
		if strings.TrimSpace(catalog.FXVersion) == "" {
			return errors.New("FX snapshot version is required for non-USD prices")
		}
		if err := validateHTTPSURL(catalog.FXSourceURL); err != nil {
			return fmt.Errorf("FX source URL: %w", err)
		}
		if !digestPattern.MatchString(catalog.FXSourceDigest) {
			return errors.New("FX source digest must be a lowercase sha256 digest")
		}
		if catalog.FXVerifiedAt.IsZero() {
			return errors.New("FX verification timestamp is required for non-USD prices")
		}
	}
	return nil
}

func validateOfficialRateSet(sku, label string, rates []Rate) error {
	classes := make(map[TokenClass]struct{}, len(rates))
	for _, rate := range rates {
		if _, err := parseDecimal(rate.NativePricePerMillion); err != nil {
			return fmt.Errorf("entry %q %srate %q: native price must be an exact decimal", sku, label, rate.Class)
		}
		classes[rate.Class] = struct{}{}
	}
	if _, exists := classes[TokenInput]; !exists {
		return fmt.Errorf("entry %q: %sinput price is required", sku, label)
	}
	if _, exists := classes[TokenOutput]; !exists {
		return fmt.Errorf("entry %q: %soutput price is required", sku, label)
	}
	_, genericWrite := classes[TokenCacheWrite]
	_, write5m := classes[TokenCacheWrite5m]
	_, write1h := classes[TokenCacheWrite1h]
	if genericWrite && (write5m || write1h) {
		return fmt.Errorf("entry %q: %sgeneric and TTL-specific cache-write prices are mutually exclusive", sku, label)
	}
	return nil
}

// CatalogDigest returns a deterministic digest over immutable catalog content.
// ImportedAt and Digest are repository metadata and are intentionally omitted.
func CatalogDigest(input Catalog) (string, error) {
	catalog := normalizeCatalog(input)
	catalog.Digest = ""
	catalog.ImportedAt = time.Time{}
	sort.Slice(catalog.Entries, func(i, j int) bool {
		return normalizeSKU(catalog.Entries[i].SKU) < normalizeSKU(catalog.Entries[j].SKU)
	})
	for index := range catalog.Entries {
		catalog.Entries[index].Rates = SortedRates(catalog.Entries[index])
		if catalog.Entries[index].LongContext != nil {
			catalog.Entries[index].LongContext.Rates = sortedRateSet(catalog.Entries[index].LongContext.Rates)
		}
	}
	payload, err := json.Marshal(catalog)
	if err != nil {
		return "", fmt.Errorf("encode canonical catalog: %w", err)
	}
	digest := sha256.Sum256(payload)
	return CatalogDigestAlgorithm + ":" + hex.EncodeToString(digest[:]), nil
}

type Usage map[TokenClass]int64

type Charge struct {
	NanoUSD        int64  `json:"nano_usd"`
	CatalogVersion string `json:"catalog_version"`
	SKU            string `json:"sku"`
}

// CalculateCharge rounds the final request cost to the nearest nano-USD. The
// catalog version and SKU are returned so historical ledger entries remain
// immutable after future price updates.
func CalculateCharge(catalogVersion string, entry Entry, usage Usage) (Charge, error) {
	if strings.TrimSpace(catalogVersion) == "" {
		return Charge{}, errors.New("catalog version is required")
	}
	if err := entry.Validate(); err != nil {
		return Charge{}, err
	}
	if err := ValidateUsage(usage); err != nil {
		return Charge{}, err
	}
	selectedRates := entry.Rates
	if entry.LongContext != nil && exceedsLongContextThreshold(*entry.LongContext, usage) {
		selectedRates = entry.LongContext.Rates
	}
	rates := make(map[TokenClass]int64, len(selectedRates))
	for _, rate := range selectedRates {
		rates[rate.Class] = rate.NanoUSDPerMillion
	}
	total := new(big.Int)
	for class, tokens := range usage {
		rate, exists := rates[class]
		if !exists && tokens > 0 {
			return Charge{}, fmt.Errorf("price SKU %q has no verified rate for %q", entry.SKU, class)
		}
		term := new(big.Int).Mul(big.NewInt(tokens), big.NewInt(rate))
		total.Add(total, term)
	}
	rounded := roundRatioHalfUp(total, big.NewInt(TokensPerMillion))
	if !rounded.IsInt64() {
		return Charge{}, errors.New("calculated charge exceeds int64 nano-USD range")
	}
	return Charge{NanoUSD: rounded.Int64(), CatalogVersion: catalogVersion, SKU: entry.SKU}, nil
}

func exceedsLongContextThreshold(tier LongContextTier, usage Usage) bool {
	total := new(big.Int)
	for _, class := range []TokenClass{
		TokenInput,
		TokenCacheRead,
		TokenCacheWrite,
		TokenCacheWrite5m,
		TokenCacheWrite1h,
	} {
		total.Add(total, big.NewInt(usage[class]))
	}
	comparison := total.Cmp(big.NewInt(tier.ThresholdTokens))
	if tier.ThresholdInclusive {
		return comparison >= 0
	}
	return comparison > 0
}

func ValidateUsage(usage Usage) error {
	for class, tokens := range usage {
		if !knownTokenClass(class) {
			return fmt.Errorf("unknown usage token class %q", class)
		}
		if tokens < 0 {
			return fmt.Errorf("negative token count for %q", class)
		}
	}
	if usage[TokenCacheWrite] > 0 && (usage[TokenCacheWrite5m] > 0 || usage[TokenCacheWrite1h] > 0) {
		return errors.New("generic and TTL-specific cache-write usage are mutually exclusive")
	}
	return nil
}

// ConvertPerMillionToNanoUSD converts an official price in its native
// currency to the normalized USD rate. usdPerNativeUnit must come from a
// versioned FX snapshot and is never looked up during request handling.
func ConvertPerMillionToNanoUSD(nativePrice, usdPerNativeUnit string) (int64, error) {
	price, err := parseDecimal(nativePrice)
	if err != nil {
		return 0, fmt.Errorf("native price: %w", err)
	}
	fx, err := parseDecimal(usdPerNativeUnit)
	if err != nil {
		return 0, fmt.Errorf("FX rate: %w", err)
	}
	if fx.Sign() <= 0 {
		return 0, errors.New("FX rate must be positive")
	}
	value := new(big.Rat).Mul(price, fx)
	value.Mul(value, new(big.Rat).SetInt64(NanoUSDPerUSD))
	rounded := roundRatioHalfUp(value.Num(), value.Denom())
	if !rounded.IsInt64() {
		return 0, errors.New("normalized rate exceeds int64 nano-USD range")
	}
	return rounded.Int64(), nil
}

func SortedRates(entry Entry) []Rate {
	return sortedRateSet(entry.Rates)
}

func sortedRateSet(input []Rate) []Rate {
	rates := append([]Rate(nil), input...)
	sort.Slice(rates, func(i, j int) bool { return rates[i].Class < rates[j].Class })
	return rates
}

func normalizeCatalog(input Catalog) Catalog {
	catalog := cloneCatalog(input)
	catalog.Version = strings.TrimSpace(catalog.Version)
	catalog.Digest = strings.ToLower(strings.TrimSpace(catalog.Digest))
	catalog.SettlementCurrency = strings.ToUpper(strings.TrimSpace(catalog.SettlementCurrency))
	catalog.Source = strings.TrimSpace(catalog.Source)
	catalog.SourceDigest = strings.ToLower(strings.TrimSpace(catalog.SourceDigest))
	catalog.FXVersion = strings.TrimSpace(catalog.FXVersion)
	catalog.FXSourceURL = strings.TrimSpace(catalog.FXSourceURL)
	catalog.FXSourceDigest = strings.ToLower(strings.TrimSpace(catalog.FXSourceDigest))
	catalog.FXVerifiedAt = canonicalTime(catalog.FXVerifiedAt)
	catalog.FetchedAt = canonicalTime(catalog.FetchedAt)
	catalog.VerifiedAt = canonicalTime(catalog.VerifiedAt)
	catalog.EffectiveAt = canonicalTime(catalog.EffectiveAt)
	catalog.ImportedAt = canonicalTime(catalog.ImportedAt)
	for index := range catalog.Entries {
		entry := &catalog.Entries[index]
		entry.SKU = strings.TrimSpace(entry.SKU)
		entry.Provider = strings.TrimSpace(entry.Provider)
		entry.ModelPattern = strings.TrimSpace(entry.ModelPattern)
		entry.PricingBasis = strings.TrimSpace(entry.PricingBasis)
		entry.VerificationStatus = strings.ToLower(strings.TrimSpace(entry.VerificationStatus))
		entry.SourceURL = strings.TrimSpace(entry.SourceURL)
		entry.SourceDigest = strings.ToLower(strings.TrimSpace(entry.SourceDigest))
		entry.VerifiedAt = canonicalTime(entry.VerifiedAt)
		entry.NativeCurrency = strings.ToUpper(strings.TrimSpace(entry.NativeCurrency))
		entry.USDPerNativeUnit = strings.TrimSpace(entry.USDPerNativeUnit)
		for rateIndex := range entry.Rates {
			entry.Rates[rateIndex].NativePricePerMillion = strings.TrimSpace(entry.Rates[rateIndex].NativePricePerMillion)
		}
		if entry.LongContext != nil {
			for rateIndex := range entry.LongContext.Rates {
				entry.LongContext.Rates[rateIndex].NativePricePerMillion = strings.TrimSpace(entry.LongContext.Rates[rateIndex].NativePricePerMillion)
			}
		}
	}
	return catalog
}

func canonicalTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC().Round(0)
}

func validateHTTPSURL(value string) error {
	if len(strings.TrimSpace(value)) > maxSourceURLLength {
		return fmt.Errorf("exceeds %d bytes", maxSourceURLLength)
	}
	parsed, err := url.ParseRequestURI(strings.TrimSpace(value))
	if err != nil || parsed == nil || parsed.Scheme != "https" || parsed.Host == "" {
		return errors.New("must be an absolute HTTPS URL")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return errors.New("must not contain credentials or a fragment")
	}
	return nil
}

func parseDecimal(value string) (*big.Rat, error) {
	value = strings.TrimSpace(value)
	if len(value) > maxDecimalLength {
		return nil, fmt.Errorf("decimal exceeds %d bytes", maxDecimalLength)
	}
	if !decimalPattern.MatchString(value) {
		return nil, fmt.Errorf("expected a non-negative exact decimal, got %q", value)
	}
	result, ok := new(big.Rat).SetString(value)
	if !ok {
		return nil, fmt.Errorf("cannot parse decimal %q", value)
	}
	return result, nil
}

func roundRatioHalfUp(numerator, denominator *big.Int) *big.Int {
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, denominator, remainder)
	if new(big.Int).Lsh(remainder, 1).Cmp(denominator) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	return quotient
}

func knownTokenClass(class TokenClass) bool {
	switch class {
	case TokenInput, TokenOutput, TokenCacheRead, TokenCacheWrite, TokenCacheWrite5m, TokenCacheWrite1h, TokenReasoning:
		return true
	default:
		return false
	}
}
