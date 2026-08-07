package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/pricing"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol"
)

func TestConservativeJSONReservationPlannerUsesPayloadBytesAndConfiguredDefault(t *testing.T) {
	payload := []byte(`{"model":"public-model","messages":[{"role":"user","content":"hello"}]}`)
	plan, err := NewConservativeJSONReservationPlanner().PlanReservation(context.Background(), ReservationInput{
		Protocol: protocol.OpenAI, Surface: protocol.OpenAIChatCompletions,
		Payload: payload, DefaultMaxOutputTokens: 777,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.MaximumUsage[pricing.TokenInput] != int64(len(payload)) || plan.MaximumUsage[pricing.TokenOutput] != 777 {
		t.Fatalf("plan = %+v", plan)
	}
	if _, exists := plan.MaximumUsage[pricing.TokenReasoning]; exists || plan.ReasoningEffort != "" || plan.ThinkingBudgetTokens != nil {
		t.Fatalf("unexpected reasoning reservation = %+v", plan)
	}
}

func TestConservativeJSONReservationPlannerReadsExplicitOutputReasoningAndThinkingLimits(t *testing.T) {
	tests := []struct {
		name          string
		payload       string
		wantOutput    int64
		wantReasoning int64
		wantEffort    string
		wantThinking  *int64
	}{
		{
			name:       "OpenAI chat uses the largest explicit compatibility limit",
			payload:    `{"max_tokens":100,"max_completion_tokens":120,"reasoning_effort":"HIGH"}`,
			wantOutput: 120, wantReasoning: 120, wantEffort: "high",
		},
		{
			name:       "OpenAI Responses nested reasoning budget",
			payload:    `{"max_output_tokens":200,"reasoning":{"effort":"medium","max_tokens":80}}`,
			wantOutput: 200, wantReasoning: 80, wantEffort: "medium", wantThinking: int64TestPointer(80),
		},
		{
			name:       "Anthropic thinking budget",
			payload:    `{"max_tokens":300,"thinking":{"type":"enabled","budget_tokens":100}}`,
			wantOutput: 300, wantReasoning: 100, wantEffort: "enabled", wantThinking: int64TestPointer(100),
		},
		{
			name:       "Gemini dynamic thinking",
			payload:    `{"generationConfig":{"maxOutputTokens":400,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":-1}}}`,
			wantOutput: 400, wantReasoning: 400, wantEffort: "dynamic",
		},
	}
	planner := NewConservativeJSONReservationPlanner()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := planner.PlanReservation(context.Background(), ReservationInput{
				Protocol: protocol.OpenAI, Surface: protocol.OpenAIChatCompletions,
				Payload: []byte(test.payload), DefaultMaxOutputTokens: 4096,
			})
			if err != nil {
				t.Fatal(err)
			}
			if plan.MaximumUsage[pricing.TokenOutput] != test.wantOutput ||
				plan.MaximumUsage[pricing.TokenReasoning] != test.wantReasoning || plan.ReasoningEffort != test.wantEffort ||
				!equalTestInt64Pointer(plan.ThinkingBudgetTokens, test.wantThinking) {
				t.Fatalf("plan = %+v", plan)
			}
		})
	}
}

func TestConservativeJSONReservationPlannerRejectsUnboundedOrMalformedLimits(t *testing.T) {
	tests := []string{
		`{"max_tokens":0}`,
		`{"max_tokens":1.5}`,
		`{"max_tokens":"10"}`,
		`{"thinking":{"budget_tokens":-2}}`,
		`{"reasoning_effort":3}`,
		`{"model":"a"} {"model":"b"}`,
		`[]`,
	}
	planner := NewConservativeJSONReservationPlanner()
	for _, payload := range tests {
		_, err := planner.PlanReservation(context.Background(), ReservationInput{
			Protocol: protocol.OpenAI, Surface: protocol.OpenAIChatCompletions,
			Payload: []byte(payload), DefaultMaxOutputTokens: 100,
		})
		if !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("payload %s error = %v", payload, err)
		}
	}
	if _, err := planner.PlanReservation(context.Background(), ReservationInput{
		Protocol: protocol.OpenAI, Surface: protocol.OpenAIChatCompletions,
		Payload: []byte(`{"model":"a"}`), DefaultMaxOutputTokens: 0,
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("zero default output error = %v", err)
	}
}

func TestAnthropicCacheControlReservesTheTTLRateAndBoundsActualCharge(t *testing.T) {
	tests := []struct {
		name      string
		control   string
		wantClass pricing.TokenClass
	}{
		{name: "omitted TTL defaults to 5m", control: `{"type":"ephemeral"}`, wantClass: pricing.TokenCacheWrite5m},
		{name: "explicit 1h TTL", control: `{"type":"ephemeral","ttl":"1h"}`, wantClass: pricing.TokenCacheWrite1h},
	}
	now := time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)
	book, err := pricing.NewBook(pricing.Catalog{
		Version: "cache-prices", Source: "official", SourceDigest: "cache-digest", FXVersion: "usd",
		FetchedAt: now, EffectiveAt: now,
		Entries: []pricing.Entry{{
			SKU: "claude-test", Provider: "anthropic", ModelPattern: "claude-test",
			Rates: []pricing.Rate{
				{Class: pricing.TokenInput, NanoUSDPerMillion: 1_000_000_000},
				{Class: pricing.TokenOutput, NanoUSDPerMillion: 2_000_000_000},
				{Class: pricing.TokenCacheWrite5m, NanoUSDPerMillion: 1_250_000_000},
				{Class: pricing.TokenCacheWrite1h, NanoUSDPerMillion: 2_000_000_000},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := []byte(`{"model":"claude-test","max_tokens":50,"messages":[{"role":"user","content":[{"type":"text","text":"hello","cache_control":` + test.control + `}]}]}`)
			plan, err := NewConservativeJSONReservationPlanner().PlanReservation(context.Background(), ReservationInput{
				Protocol: protocol.Anthropic, Surface: protocol.AnthropicMessages,
				Payload: payload, DefaultMaxOutputTokens: 4096,
			})
			if err != nil {
				t.Fatal(err)
			}
			if plan.MaximumUsage[test.wantClass] != int64(len(payload)) {
				t.Fatalf("cache reservation = %+v, payload bytes=%d", plan.MaximumUsage, len(payload))
			}
			otherClass := pricing.TokenCacheWrite5m
			if test.wantClass == pricing.TokenCacheWrite5m {
				otherClass = pricing.TokenCacheWrite1h
			}
			if _, exists := plan.MaximumUsage[otherClass]; exists {
				t.Fatalf("unexpected second TTL reservation = %+v", plan.MaximumUsage)
			}
			quote, err := book.Quote("claude-test", plan.MaximumUsage)
			if err != nil {
				t.Fatal(err)
			}
			actualUsage := pricing.Usage{pricing.TokenInput: 20, pricing.TokenOutput: 10, test.wantClass: 30}
			charge, err := book.Charge(quote.CatalogVersion, quote.SKU, actualUsage)
			if err != nil {
				t.Fatal(err)
			}
			if charge.NanoUSD > quote.ReservationNanoUSD {
				t.Fatalf("actual charge %d exceeded reservation %d", charge.NanoUSD, quote.ReservationNanoUSD)
			}
		})
	}
}

func TestAnthropicCacheControlRejectsUnsupportedTTL(t *testing.T) {
	_, err := NewConservativeJSONReservationPlanner().PlanReservation(context.Background(), ReservationInput{
		Protocol: protocol.Anthropic, Surface: protocol.AnthropicMessages,
		Payload:                []byte(`{"max_tokens":10,"messages":[{"cache_control":{"type":"ephemeral","ttl":"2h"}}]}`),
		DefaultMaxOutputTokens: 100,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error = %v", err)
	}
}

func int64TestPointer(value int64) *int64 { return &value }

func equalTestInt64Pointer(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
