package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/LuTianTian001/JieShan/internal/vnext/pricing"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol"
)

const defaultMaxOutputTokens int64 = 4096

type ReservationInput struct {
	Protocol               protocol.Protocol
	Surface                protocol.Surface
	Payload                []byte
	DefaultMaxOutputTokens int64
}

type ReservationPlan struct {
	MaximumUsage         pricing.Usage
	ReasoningEffort      string
	ThinkingBudgetTokens *int64
}

// ReservationPlanner provides a replaceable upper-bound estimate for quota
// admission. It is intentionally separate from final provider usage, which is
// always used for settlement.
type ReservationPlanner interface {
	PlanReservation(context.Context, ReservationInput) (ReservationPlan, error)
}

// ConservativeJSONReservationPlanner uses the request byte length as a
// conservative input-token ceiling and reads explicit output/thinking limits.
// It is an admission estimate, not an exact tokenizer.
type ConservativeJSONReservationPlanner struct{}

func NewConservativeJSONReservationPlanner() *ConservativeJSONReservationPlanner {
	return &ConservativeJSONReservationPlanner{}
}

func (*ConservativeJSONReservationPlanner) PlanReservation(_ context.Context, input ReservationInput) (ReservationPlan, error) {
	if len(input.Payload) == 0 || input.DefaultMaxOutputTokens <= 0 {
		return ReservationPlan{}, fmt.Errorf("%w: reservation payload and default output limit are required", ErrInvalidRequest)
	}
	decoder := json.NewDecoder(bytes.NewReader(input.Payload))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil || payload == nil {
		return ReservationPlan{}, fmt.Errorf("%w: request payload must be one JSON object", ErrInvalidRequest)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ReservationPlan{}, fmt.Errorf("%w: request payload must contain exactly one JSON object", ErrInvalidRequest)
	}

	maxOutput := input.DefaultMaxOutputTokens
	explicitOutput, found, err := maximumIntegerAtPaths(payload,
		[]string{"max_completion_tokens"},
		[]string{"max_output_tokens"},
		[]string{"max_tokens"},
		[]string{"generationConfig", "maxOutputTokens"},
	)
	if err != nil {
		return ReservationPlan{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if found {
		if explicitOutput <= 0 {
			return ReservationPlan{}, fmt.Errorf("%w: maximum output tokens must be positive", ErrInvalidRequest)
		}
		maxOutput = explicitOutput
	}

	reasoningEffort, err := reasoningEffortFromPayload(payload)
	if err != nil {
		return ReservationPlan{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	thinkingBudget, dynamicThinking, err := thinkingBudgetFromPayload(payload)
	if err != nil {
		return ReservationPlan{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if dynamicThinking && reasoningEffort == "" {
		reasoningEffort = "dynamic"
	}

	maximum := pricing.Usage{
		pricing.TokenInput:  int64(len(input.Payload)),
		pricing.TokenOutput: maxOutput,
	}
	if input.Protocol == protocol.Anthropic || input.Surface == protocol.AnthropicMessages {
		cache5m, cache1h, err := anthropicCacheReservation(payload)
		if err != nil {
			return ReservationPlan{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
		}
		if cache5m {
			maximum[pricing.TokenCacheWrite5m] = int64(len(input.Payload))
		}
		if cache1h {
			maximum[pricing.TokenCacheWrite1h] = int64(len(input.Payload))
		}
	}
	if thinkingBudget != nil {
		maximum[pricing.TokenReasoning] = *thinkingBudget
	} else if dynamicThinking || reasoningEffort != "" {
		maximum[pricing.TokenReasoning] = maxOutput
	}
	return ReservationPlan{
		MaximumUsage:         maximum,
		ReasoningEffort:      reasoningEffort,
		ThinkingBudgetTokens: cloneInt64Pointer(thinkingBudget),
	}, nil
}

func validateReservationPlan(plan ReservationPlan) error {
	if len(plan.MaximumUsage) == 0 {
		return errors.New("maximum usage is required")
	}
	positive := false
	for class, tokens := range plan.MaximumUsage {
		switch class {
		case pricing.TokenInput, pricing.TokenOutput, pricing.TokenCacheRead, pricing.TokenCacheWrite,
			pricing.TokenCacheWrite5m, pricing.TokenCacheWrite1h, pricing.TokenReasoning:
		default:
			return fmt.Errorf("unknown token class %q", class)
		}
		if tokens < 0 {
			return fmt.Errorf("negative maximum for %q", class)
		}
		positive = positive || tokens > 0
	}
	if !positive {
		return errors.New("maximum usage must contain a positive token bound")
	}
	if len(strings.TrimSpace(plan.ReasoningEffort)) > 64 {
		return errors.New("reasoning effort exceeds its maximum length")
	}
	if plan.ThinkingBudgetTokens != nil && *plan.ThinkingBudgetTokens < 0 {
		return errors.New("thinking budget cannot be negative")
	}
	return nil
}

func clonePricingUsage(value pricing.Usage) pricing.Usage {
	copy := make(pricing.Usage, len(value))
	for class, tokens := range value {
		copy[class] = tokens
	}
	return copy
}

func maximumIntegerAtPaths(payload map[string]any, paths ...[]string) (int64, bool, error) {
	var maximum int64
	found := false
	for _, path := range paths {
		value, exists := nestedValue(payload, path)
		if !exists || value == nil {
			continue
		}
		parsed, err := exactInteger(value)
		if err != nil {
			return 0, false, fmt.Errorf("%s must be an integer", strings.Join(path, "."))
		}
		if !found || parsed > maximum {
			maximum = parsed
		}
		found = true
	}
	return maximum, found, nil
}

func thinkingBudgetFromPayload(payload map[string]any) (*int64, bool, error) {
	value, found, err := maximumIntegerAtPaths(payload,
		[]string{"thinking", "budget_tokens"},
		[]string{"reasoning", "max_tokens"},
		[]string{"generationConfig", "thinkingConfig", "thinkingBudget"},
	)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, thinkingEnabled(payload), nil
	}
	if value == -1 {
		return nil, true, nil
	}
	if value < 0 {
		return nil, false, errors.New("thinking budget cannot be negative except for dynamic -1")
	}
	return &value, value > 0 || thinkingEnabled(payload), nil
}

func reasoningEffortFromPayload(payload map[string]any) (string, error) {
	paths := [][]string{{"reasoning_effort"}, {"reasoning", "effort"}}
	for _, path := range paths {
		value, exists := nestedValue(payload, path)
		if !exists || value == nil {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("%s must be a string", strings.Join(path, "."))
		}
		text = strings.ToLower(strings.TrimSpace(text))
		if text == "" || len(text) > 64 {
			return "", fmt.Errorf("%s is invalid", strings.Join(path, "."))
		}
		return text, nil
	}
	if value, exists := nestedValue(payload, []string{"thinking", "type"}); exists && value != nil {
		text, ok := value.(string)
		if !ok {
			return "", errors.New("thinking.type must be a string")
		}
		text = strings.ToLower(strings.TrimSpace(text))
		if text == "enabled" {
			return text, nil
		}
	}
	return "", nil
}

func thinkingEnabled(payload map[string]any) bool {
	if value, exists := nestedValue(payload, []string{"thinking", "type"}); exists {
		text, _ := value.(string)
		if strings.EqualFold(strings.TrimSpace(text), "enabled") {
			return true
		}
	}
	if value, exists := nestedValue(payload, []string{"generationConfig", "thinkingConfig", "includeThoughts"}); exists {
		enabled, _ := value.(bool)
		return enabled
	}
	return false
}

func anthropicCacheReservation(payload map[string]any) (bool, bool, error) {
	return findAnthropicCacheControl(payload)
}

func findAnthropicCacheControl(value any) (bool, bool, error) {
	switch typed := value.(type) {
	case map[string]any:
		cache5m := false
		cache1h := false
		if raw, exists := typed["cache_control"]; exists {
			control, ok := raw.(map[string]any)
			if !ok || control == nil {
				return false, false, errors.New("cache_control must be an object")
			}
			if rawType, exists := control["type"]; exists {
				cacheType, ok := rawType.(string)
				if !ok || !strings.EqualFold(strings.TrimSpace(cacheType), "ephemeral") {
					return false, false, errors.New("cache_control.type must be ephemeral")
				}
			}
			ttl := "5m"
			if rawTTL, exists := control["ttl"]; exists {
				text, ok := rawTTL.(string)
				if !ok {
					return false, false, errors.New("cache_control.ttl must be a string")
				}
				ttl = strings.ToLower(strings.TrimSpace(text))
			}
			switch ttl {
			case "5m":
				cache5m = true
			case "1h":
				cache1h = true
			default:
				return false, false, errors.New("cache_control.ttl must be 5m or 1h")
			}
		}
		for key, child := range typed {
			if key == "cache_control" {
				continue
			}
			child5m, child1h, err := findAnthropicCacheControl(child)
			if err != nil {
				return false, false, err
			}
			cache5m = cache5m || child5m
			cache1h = cache1h || child1h
		}
		return cache5m, cache1h, nil
	case []any:
		cache5m := false
		cache1h := false
		for _, child := range typed {
			child5m, child1h, err := findAnthropicCacheControl(child)
			if err != nil {
				return false, false, err
			}
			cache5m = cache5m || child5m
			cache1h = cache1h || child1h
		}
		return cache5m, cache1h, nil
	default:
		return false, false, nil
	}
}

func nestedValue(payload map[string]any, path []string) (any, bool) {
	var current any = payload
	for _, part := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func exactInteger(value any) (int64, error) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, errors.New("not a JSON number")
	}
	return number.Int64()
}

var _ ReservationPlanner = (*ConservativeJSONReservationPlanner)(nil)
