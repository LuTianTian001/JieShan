// Package billing prices canonical token usage against immutable official-price
// snapshots. It performs no database writes; callers apply reservation and
// settlement amounts in their own short transaction.
package billing

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"time"
)

type MicroUSD int64

type Usage struct {
	// InputTokens excludes cached input and cache writes. OutputTokens excludes reasoning tokens.
	// Adapters must canonicalize provider usage into these non-overlapping fields.
	InputTokens        int64 `json:"input_tokens"`
	CacheReadTokens    int64 `json:"cache_read_tokens"`
	CacheWriteTokens   int64 `json:"cache_write_tokens"`
	CacheWrite1HTokens int64 `json:"cache_write_1h_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	ReasoningTokens    int64 `json:"reasoning_tokens"`
}

func (u Usage) PromptTokens() (int64, error) {
	if err := u.Validate(); err != nil {
		return 0, err
	}
	if u.InputTokens > int64(^uint64(0)>>1)-u.CacheReadTokens {
		return 0, &PricingError{Kind: ErrInvalidUsage, Reason: "prompt token count overflows int64"}
	}
	prompt := u.InputTokens + u.CacheReadTokens
	if prompt > int64(^uint64(0)>>1)-u.CacheWriteTokens {
		return 0, &PricingError{Kind: ErrInvalidUsage, Reason: "prompt token count overflows int64"}
	}
	prompt += u.CacheWriteTokens
	if prompt > int64(^uint64(0)>>1)-u.CacheWrite1HTokens {
		return 0, &PricingError{Kind: ErrInvalidUsage, Reason: "prompt token count overflows int64"}
	}
	return prompt + u.CacheWrite1HTokens, nil
}

func (u Usage) Validate() error {
	if u.InputTokens < 0 || u.CacheReadTokens < 0 || u.CacheWriteTokens < 0 || u.CacheWrite1HTokens < 0 || u.OutputTokens < 0 || u.ReasoningTokens < 0 {
		return &PricingError{Kind: ErrInvalidUsage, Reason: "token counts cannot be negative"}
	}
	return nil
}

type ComponentCost struct {
	Tokens         int64    `json:"tokens"`
	RatePerMillion string   `json:"rate_per_million"`
	SourceCurrency string   `json:"source_currency"`
	MicroUSD       MicroUSD `json:"micro_usd"`
}

type CostBreakdown struct {
	Input        ComponentCost `json:"input"`
	CacheRead    ComponentCost `json:"cache_read"`
	CacheWrite   ComponentCost `json:"cache_write"`
	CacheWrite1H ComponentCost `json:"cache_write_1h"`
	Output       ComponentCost `json:"output"`
	Reasoning    ComponentCost `json:"reasoning"`
	Total        MicroUSD      `json:"total_micro_usd"`
}

type PriceSnapshot struct {
	CatalogVersion     string      `json:"catalog_version"`
	CatalogDigest      string      `json:"catalog_sha256"`
	CatalogPublishedAt string      `json:"catalog_published_at"`
	CatalogCheckedAt   string      `json:"catalog_checked_at"`
	Provider           string      `json:"provider"`
	RequestedModel     string      `json:"requested_model"`
	PriceName          string      `json:"price_name"`
	Currency           string      `json:"currency"`
	FX                 *FXSnapshot `json:"fx,omitempty"`
	Bands              []RateBand  `json:"bands"`
	SourceURL          string      `json:"source_url"`
	SourceCheckedAt    string      `json:"source_checked_at"`
	EffectiveFrom      string      `json:"effective_from,omitempty"`
	EffectiveUntil     string      `json:"effective_until,omitempty"`
	Notes              string      `json:"notes,omitempty"`
}

type Quote struct {
	Usage       Usage         `json:"usage"`
	Cost        CostBreakdown `json:"cost"`
	AppliedBand RateBand      `json:"applied_band"`
	Snapshot    PriceSnapshot `json:"price_snapshot"`
}

func (q Quote) SnapshotJSON() ([]byte, error) {
	return json.Marshal(struct {
		PriceSnapshot PriceSnapshot `json:"price_snapshot"`
		AppliedBand   RateBand      `json:"applied_band"`
	}{PriceSnapshot: q.Snapshot, AppliedBand: q.AppliedBand})
}

type Engine struct {
	catalog *Catalog
}

func New(catalog *Catalog) (*Engine, error) {
	if catalog == nil {
		return nil, invalidCatalog("catalog is nil")
	}
	if catalog.index == nil {
		if err := catalog.validate(); err != nil {
			return nil, err
		}
	}
	return &Engine{catalog: catalog}, nil
}

func NewBuiltin() (*Engine, error) {
	catalog, err := BuiltinCatalog()
	if err != nil {
		return nil, err
	}
	return New(catalog)
}

func (e *Engine) Catalog() *Catalog {
	return e.catalog
}

func (e *Engine) Quote(model string, usage Usage) (Quote, error) {
	return e.QuoteAt(model, usage, time.Now().UTC())
}

func (e *Engine) QuoteAt(model string, usage Usage, at time.Time) (Quote, error) {
	if e == nil || e.catalog == nil {
		return Quote{}, invalidCatalog("engine has no catalog")
	}
	price, ok := e.catalog.Lookup(model)
	if !ok {
		return Quote{}, &PricingError{Kind: ErrModelNotFound, Model: model}
	}
	if !price.Priced {
		return Quote{}, &PricingError{Kind: ErrModelUnpriced, Model: model, Reason: price.UnpricedReason}
	}
	snapshot, err := e.snapshot(model, price, at)
	if err != nil {
		return Quote{}, err
	}
	return snapshot.Quote(usage)
}

func (e *Engine) snapshot(requestedModel string, price ModelPrice, at time.Time) (PriceSnapshot, error) {
	var fx *FXSnapshot
	if price.Currency != "USD" {
		value, ok := e.catalog.FX[price.Currency]
		if !ok {
			return PriceSnapshot{}, invalidCatalog("missing FX snapshot for %s", price.Currency)
		}
		copy := value
		fx = &copy
	}
	selectedBands, effectiveFrom, effectiveUntil, err := selectEffectiveBands(price, at)
	if err != nil {
		return PriceSnapshot{}, err
	}
	bands := make([]RateBand, len(selectedBands))
	for i, band := range selectedBands {
		bands[i] = band
		if band.MaxInputTokens != nil {
			limit := *band.MaxInputTokens
			bands[i].MaxInputTokens = &limit
		}
	}
	return PriceSnapshot{
		CatalogVersion: e.catalog.Version, CatalogDigest: e.catalog.DigestSHA256(),
		CatalogPublishedAt: e.catalog.PublishedAt, CatalogCheckedAt: e.catalog.CheckedAt,
		Provider: price.Provider, RequestedModel: requestedModel, PriceName: price.Name,
		Currency: price.Currency, FX: fx, Bands: bands, SourceURL: price.SourceURL,
		SourceCheckedAt: price.SourceCheckedAt, EffectiveFrom: effectiveFrom,
		EffectiveUntil: effectiveUntil, Notes: price.Notes,
	}, nil
}

func selectEffectiveBands(price ModelPrice, at time.Time) ([]RateBand, string, string, error) {
	if len(price.PricePeriods) == 0 {
		return price.Bands, "", "", nil
	}
	at = at.UTC()
	for _, period := range price.PricePeriods {
		from, _ := time.Parse("2006-01-02", period.EffectiveFrom)
		if at.Before(from) {
			continue
		}
		if period.EffectiveUntil != "" {
			until, _ := time.Parse("2006-01-02", period.EffectiveUntil)
			if !at.Before(until) {
				continue
			}
		}
		return period.Bands, period.EffectiveFrom, period.EffectiveUntil, nil
	}
	return nil, "", "", &PricingError{Kind: ErrOutsidePriceRange, Model: price.Name,
		Reason: fmt.Sprintf("no official price period is active at %s UTC", at.Format(time.RFC3339))}
}

func (s PriceSnapshot) Quote(usage Usage) (Quote, error) {
	promptTokens, err := usage.PromptTokens()
	if err != nil {
		return Quote{}, err
	}
	band, ok := selectBand(s.Bands, promptTokens)
	if !ok {
		return Quote{}, &PricingError{
			Kind: ErrOutsidePriceRange, Model: s.RequestedModel,
			Reason: fmt.Sprintf("%d prompt tokens exceed the last confirmed price band", promptTokens),
		}
	}
	components := []componentWork{
		{name: "input", tokens: usage.InputTokens, rate: band.InputPerMillion},
		{name: "cache_read", tokens: usage.CacheReadTokens, rate: band.CachePerMillion},
		{name: "cache_write", tokens: usage.CacheWriteTokens, rate: band.CacheWritePerMillion},
		{name: "cache_write_1h", tokens: usage.CacheWrite1HTokens, rate: band.CacheWrite1HPerMillion},
		{name: "output", tokens: usage.OutputTokens, rate: band.OutputPerMillion},
		{name: "reasoning", tokens: usage.ReasoningTokens, rate: band.ReasoningPerMillion},
	}
	fx, err := s.fxRate()
	if err != nil {
		return Quote{}, err
	}
	for i := range components {
		component := &components[i]
		if component.tokens == 0 {
			component.raw = new(big.Rat)
			continue
		}
		if component.rate == "" {
			return Quote{}, &PricingError{Kind: ErrCategoryUnpriced, Model: s.RequestedModel, Category: component.name}
		}
		rate, err := nonNegativeDecimal(component.rate)
		if err != nil {
			return Quote{}, invalidCatalog("snapshot %s rate: %v", component.name, err)
		}
		// A price of X currency units per one million tokens is also X
		// micro-currency units per token. Divide by units-per-USD for micro-USD.
		component.raw = new(big.Rat).Mul(rate, new(big.Rat).SetInt64(component.tokens))
		component.raw.Quo(component.raw, fx)
	}
	amounts, total, err := roundComponents(components)
	if err != nil {
		return Quote{}, err
	}
	breakdown := CostBreakdown{
		Input:        componentCost(components[0], amounts[0], s.Currency),
		CacheRead:    componentCost(components[1], amounts[1], s.Currency),
		CacheWrite:   componentCost(components[2], amounts[2], s.Currency),
		CacheWrite1H: componentCost(components[3], amounts[3], s.Currency),
		Output:       componentCost(components[4], amounts[4], s.Currency),
		Reasoning:    componentCost(components[5], amounts[5], s.Currency),
		Total:        total,
	}
	return Quote{Usage: usage, Cost: breakdown, AppliedBand: band, Snapshot: s}, nil
}

func (s PriceSnapshot) fxRate() (*big.Rat, error) {
	if s.Currency == "USD" {
		return big.NewRat(1, 1), nil
	}
	if s.FX == nil {
		return nil, invalidCatalog("snapshot is missing FX for %s", s.Currency)
	}
	rate, err := positiveDecimal(s.FX.UnitsPerUSD)
	if err != nil {
		return nil, invalidCatalog("snapshot FX for %s: %v", s.Currency, err)
	}
	return rate, nil
}

type Reservation struct {
	MaximumUsage     Usage         `json:"maximum_usage"`
	ReservedMicroUSD MicroUSD      `json:"reserved_micro_usd"`
	Snapshot         PriceSnapshot `json:"price_snapshot"`
}

func (e *Engine) Reserve(model string, maximum Usage) (Reservation, error) {
	return e.ReserveAt(model, maximum, time.Now().UTC())
}

func (e *Engine) ReserveAt(model string, maximum Usage, at time.Time) (Reservation, error) {
	price, ok := e.catalog.Lookup(model)
	if !ok {
		return Reservation{}, &PricingError{Kind: ErrModelNotFound, Model: model}
	}
	if !price.Priced {
		return Reservation{}, &PricingError{Kind: ErrModelUnpriced, Model: model, Reason: price.UnpricedReason}
	}
	snapshot, err := e.snapshot(model, price, at)
	if err != nil {
		return Reservation{}, err
	}
	maximum, err = conservativeMaximumUsage(snapshot, maximum)
	if err != nil {
		return Reservation{}, err
	}
	quote, err := snapshot.Quote(maximum)
	if err != nil {
		return Reservation{}, err
	}
	return Reservation{
		MaximumUsage: maximum, ReservedMicroUSD: quote.Cost.Total, Snapshot: quote.Snapshot,
	}, nil
}

func conservativeMaximumUsage(snapshot PriceSnapshot, maximum Usage) (Usage, error) {
	prompt, err := maximum.PromptTokens()
	if err != nil {
		return Usage{}, err
	}
	band, ok := selectBand(snapshot.Bands, prompt)
	if !ok {
		return Usage{}, &PricingError{Kind: ErrOutsidePriceRange, Model: snapshot.RequestedModel}
	}
	type promptRate struct {
		rate string
		set  func(*Usage, int64)
	}
	rates := []promptRate{
		{band.InputPerMillion, func(u *Usage, n int64) { u.InputTokens = n }},
		{band.CachePerMillion, func(u *Usage, n int64) { u.CacheReadTokens = n }},
		{band.CacheWritePerMillion, func(u *Usage, n int64) { u.CacheWriteTokens = n }},
		{band.CacheWrite1HPerMillion, func(u *Usage, n int64) { u.CacheWrite1HTokens = n }},
	}
	best := -1
	var bestRate *big.Rat
	for i, candidate := range rates {
		if candidate.rate == "" {
			continue
		}
		rate, parseErr := nonNegativeDecimal(candidate.rate)
		if parseErr != nil {
			return Usage{}, invalidCatalog("reservation prompt rate: %v", parseErr)
		}
		if bestRate == nil || rate.Cmp(bestRate) > 0 {
			best, bestRate = i, rate
		}
	}
	if best < 0 {
		return Usage{}, &PricingError{Kind: ErrCategoryUnpriced, Model: snapshot.RequestedModel, Category: "input"}
	}
	result := Usage{OutputTokens: maximum.OutputTokens, ReasoningTokens: maximum.ReasoningTokens}
	rates[best].set(&result, prompt)
	return result, nil
}

type Settlement struct {
	ReservedMicroUSD   MicroUSD `json:"reserved_micro_usd"`
	ChargedMicroUSD    MicroUSD `json:"charged_micro_usd"`
	ReleaseMicroUSD    MicroUSD `json:"release_micro_usd"`
	AdditionalMicroUSD MicroUSD `json:"additional_micro_usd"`
	DeltaMicroUSD      int64    `json:"delta_micro_usd"`
	Quote              Quote    `json:"quote"`
}

func (r Reservation) Settle(actual Usage) (Settlement, error) {
	quote, err := r.Snapshot.Quote(actual)
	if err != nil {
		return Settlement{}, err
	}
	settlement := Settlement{
		ReservedMicroUSD: r.ReservedMicroUSD,
		ChargedMicroUSD:  quote.Cost.Total,
		DeltaMicroUSD:    int64(quote.Cost.Total) - int64(r.ReservedMicroUSD),
		Quote:            quote,
	}
	if settlement.DeltaMicroUSD < 0 {
		settlement.ReleaseMicroUSD = MicroUSD(-settlement.DeltaMicroUSD)
	} else {
		settlement.AdditionalMicroUSD = MicroUSD(settlement.DeltaMicroUSD)
	}
	return settlement, nil
}

func selectBand(bands []RateBand, promptTokens int64) (RateBand, bool) {
	for _, band := range bands {
		if band.MaxInputTokens == nil || promptTokens <= *band.MaxInputTokens {
			return band, true
		}
	}
	return RateBand{}, false
}

type componentWork struct {
	name   string
	tokens int64
	rate   string
	raw    *big.Rat
}

func componentCost(work componentWork, amount MicroUSD, currency string) ComponentCost {
	return ComponentCost{Tokens: work.tokens, RatePerMillion: work.rate, SourceCurrency: currency, MicroUSD: amount}
}

func roundComponents(components []componentWork) ([]MicroUSD, MicroUSD, error) {
	totalRaw := new(big.Rat)
	floors := make([]*big.Int, len(components))
	type remainder struct {
		index int
		value *big.Rat
	}
	remainders := make([]remainder, len(components))
	for i, component := range components {
		if component.raw == nil || component.raw.Sign() < 0 {
			return nil, 0, invalidCatalog("invalid raw %s cost", component.name)
		}
		totalRaw.Add(totalRaw, component.raw)
		floor := new(big.Int).Quo(component.raw.Num(), component.raw.Denom())
		floors[i] = floor
		remainders[i] = remainder{
			index: i,
			value: new(big.Rat).Sub(component.raw, new(big.Rat).SetInt(floor)),
		}
	}
	target := roundHalfUp(totalRaw)
	floorSum := new(big.Int)
	for _, floor := range floors {
		floorSum.Add(floorSum, floor)
	}
	remaining := new(big.Int).Sub(target, floorSum)
	if !remaining.IsInt64() || remaining.Int64() < 0 || remaining.Int64() > int64(len(components)) {
		return nil, 0, fmt.Errorf("%w: invalid rounding remainder", ErrAmountOverflow)
	}
	sort.SliceStable(remainders, func(i, j int) bool {
		return remainders[i].value.Cmp(remainders[j].value) > 0
	})
	for i := int64(0); i < remaining.Int64(); i++ {
		floors[remainders[i].index].Add(floors[remainders[i].index], big.NewInt(1))
	}
	if !target.IsInt64() {
		return nil, 0, ErrAmountOverflow
	}
	amounts := make([]MicroUSD, len(components))
	for i, value := range floors {
		if !value.IsInt64() {
			return nil, 0, ErrAmountOverflow
		}
		amounts[i] = MicroUSD(value.Int64())
	}
	return amounts, MicroUSD(target.Int64()), nil
}

func roundHalfUp(value *big.Rat) *big.Int {
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(value.Num(), value.Denom(), remainder)
	if new(big.Int).Lsh(remainder, 1).Cmp(value.Denom()) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	return quotient
}
