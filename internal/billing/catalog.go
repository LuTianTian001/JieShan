package billing

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"sort"
	"strings"
	"time"
)

const catalogSchemaVersion = 1

//go:embed builtin_catalog.json
var builtinCatalogJSON []byte

type Source struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	CheckedAt string `json:"checked_at"`
}

type FXSnapshot struct {
	UnitsPerUSD string `json:"units_per_usd"`
	AsOf        string `json:"as_of"`
	SourceURL   string `json:"source_url"`
}

type RateBand struct {
	MaxInputTokens      *int64 `json:"max_input_tokens,omitempty"`
	InputPerMillion     string `json:"input_per_million,omitempty"`
	CachePerMillion     string `json:"cache_per_million,omitempty"`
	OutputPerMillion    string `json:"output_per_million,omitempty"`
	ReasoningPerMillion string `json:"reasoning_per_million,omitempty"`
}

type ModelPrice struct {
	Provider        string     `json:"provider"`
	Name            string     `json:"name"`
	Aliases         []string   `json:"aliases,omitempty"`
	Currency        string     `json:"currency"`
	Priced          bool       `json:"priced"`
	UnpricedReason  string     `json:"unpriced_reason,omitempty"`
	Bands           []RateBand `json:"bands,omitempty"`
	SourceURL       string     `json:"source_url"`
	SourceCheckedAt string     `json:"source_checked_at"`
	Notes           string     `json:"notes,omitempty"`
}

type Catalog struct {
	SchemaVersion int                   `json:"schema_version"`
	Version       string                `json:"version"`
	PublishedAt   string                `json:"published_at"`
	CheckedAt     string                `json:"checked_at"`
	BaseCurrency  string                `json:"base_currency"`
	FX            map[string]FXSnapshot `json:"fx"`
	Sources       []Source              `json:"sources"`
	Models        []ModelPrice          `json:"models"`

	digest string
	index  map[string]int
}

func BuiltinCatalog() (*Catalog, error) {
	return ParseCatalog(builtinCatalogJSON)
}

func BuiltinCatalogJSON() []byte {
	return append([]byte(nil), builtinCatalogJSON...)
}

func ParseCatalog(data []byte) (*Catalog, error) {
	var catalog Catalog
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("%w: decode JSON: %v", ErrInvalidCatalog, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing JSON data", ErrInvalidCatalog)
	}
	if err := catalog.validate(); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	catalog.digest = hex.EncodeToString(sum[:])
	return &catalog, nil
}

func (c *Catalog) DigestSHA256() string {
	if c == nil {
		return ""
	}
	return c.digest
}

func (c *Catalog) Lookup(model string) (ModelPrice, bool) {
	if c == nil {
		return ModelPrice{}, false
	}
	index, ok := c.index[normalizeModel(model)]
	if !ok {
		return ModelPrice{}, false
	}
	return c.Models[index], true
}

func (c *Catalog) LookupProvider(provider, model string) (ModelPrice, bool) {
	price, ok := c.Lookup(model)
	if !ok || !strings.EqualFold(strings.TrimSpace(provider), price.Provider) {
		return ModelPrice{}, false
	}
	return price, true
}

func (c *Catalog) validate() error {
	if c.SchemaVersion != catalogSchemaVersion {
		return invalidCatalog("unsupported schema_version %d", c.SchemaVersion)
	}
	if strings.TrimSpace(c.Version) == "" {
		return invalidCatalog("version is required")
	}
	if err := validateDate("published_at", c.PublishedAt); err != nil {
		return err
	}
	if err := validateDate("checked_at", c.CheckedAt); err != nil {
		return err
	}
	if c.BaseCurrency != "USD" {
		return invalidCatalog("base_currency must be USD")
	}
	for currency, snapshot := range c.FX {
		if currency == "USD" {
			return invalidCatalog("USD must not have an FX entry")
		}
		if _, err := positiveDecimal(snapshot.UnitsPerUSD); err != nil {
			return invalidCatalog("FX %s units_per_usd: %v", currency, err)
		}
		if err := validateDate("FX "+currency+" as_of", snapshot.AsOf); err != nil {
			return err
		}
		if err := validateHTTPS(snapshot.SourceURL); err != nil {
			return invalidCatalog("FX %s source_url: %v", currency, err)
		}
	}
	for i, source := range c.Sources {
		if strings.TrimSpace(source.Name) == "" {
			return invalidCatalog("sources[%d].name is required", i)
		}
		if err := validateHTTPS(source.URL); err != nil {
			return invalidCatalog("sources[%d].url: %v", i, err)
		}
		if err := validateDate(fmt.Sprintf("sources[%d].checked_at", i), source.CheckedAt); err != nil {
			return err
		}
	}
	if len(c.Models) == 0 {
		return invalidCatalog("at least one model is required")
	}
	c.index = make(map[string]int, len(c.Models)*2)
	for i := range c.Models {
		model := &c.Models[i]
		if err := validateModel(model); err != nil {
			return invalidCatalog("models[%d]: %v", i, err)
		}
		if model.Priced && model.Currency != "USD" {
			if _, ok := c.FX[model.Currency]; !ok {
				return invalidCatalog("models[%d] is missing FX for %s", i, model.Currency)
			}
		}
		names := append([]string{model.Name}, model.Aliases...)
		for _, name := range names {
			key := normalizeModel(name)
			if key == "" {
				return invalidCatalog("models[%d] contains an empty alias", i)
			}
			if existing, ok := c.index[key]; ok && existing != i {
				return invalidCatalog("model alias %q is duplicated", name)
			}
			c.index[key] = i
		}
	}
	return nil
}

func validateModel(model *ModelPrice) error {
	model.Provider = strings.ToLower(strings.TrimSpace(model.Provider))
	model.Name = strings.TrimSpace(model.Name)
	model.Currency = strings.ToUpper(strings.TrimSpace(model.Currency))
	if model.Provider == "" || model.Name == "" {
		return fmt.Errorf("provider and name are required")
	}
	if model.Currency == "" {
		return fmt.Errorf("currency is required")
	}
	if err := validateHTTPS(model.SourceURL); err != nil {
		return fmt.Errorf("source_url: %w", err)
	}
	if err := validateDate("source_checked_at", model.SourceCheckedAt); err != nil {
		return err
	}
	if !model.Priced {
		if strings.TrimSpace(model.UnpricedReason) == "" {
			return fmt.Errorf("unpriced_reason is required for unpriced models")
		}
		if len(model.Bands) != 0 {
			return fmt.Errorf("unpriced models cannot contain rate bands")
		}
		return nil
	}
	if len(model.Bands) == 0 {
		return fmt.Errorf("priced model has no rate bands")
	}
	var previous int64
	for i := range model.Bands {
		band := &model.Bands[i]
		if i < len(model.Bands)-1 && band.MaxInputTokens == nil {
			return fmt.Errorf("only the final rate band may omit max_input_tokens")
		}
		if band.MaxInputTokens != nil {
			if *band.MaxInputTokens <= previous {
				return fmt.Errorf("rate band limits must be strictly increasing")
			}
			previous = *band.MaxInputTokens
		}
		if band.InputPerMillion == "" || band.OutputPerMillion == "" {
			return fmt.Errorf("rate band %d requires input and output prices", i)
		}
		for name, value := range map[string]string{
			"input": band.InputPerMillion, "cache": band.CachePerMillion,
			"output": band.OutputPerMillion, "reasoning": band.ReasoningPerMillion,
		} {
			if value == "" {
				continue
			}
			if _, err := nonNegativeDecimal(value); err != nil {
				return fmt.Errorf("rate band %d %s price: %v", i, name, err)
			}
		}
	}
	return nil
}

func validateDate(field, value string) error {
	if _, err := time.Parse("2006-01-02", value); err != nil {
		return invalidCatalog("%s must be YYYY-MM-DD", field)
	}
	return nil
}

func validateHTTPS(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("must be an absolute HTTPS URL")
	}
	return nil
}

func nonNegativeDecimal(value string) (*big.Rat, error) {
	rate, ok := new(big.Rat).SetString(value)
	if !ok || rate.Sign() < 0 {
		return nil, fmt.Errorf("must be a non-negative decimal")
	}
	return rate, nil
}

func positiveDecimal(value string) (*big.Rat, error) {
	rate, err := nonNegativeDecimal(value)
	if err != nil || rate.Sign() == 0 {
		return nil, fmt.Errorf("must be a positive decimal")
	}
	return rate, nil
}

func normalizeModel(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

func invalidCatalog(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidCatalog, fmt.Sprintf(format, args...))
}

func sortedFXKeys(values map[string]FXSnapshot) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
