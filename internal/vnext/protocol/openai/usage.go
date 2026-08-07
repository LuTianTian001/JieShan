package openai

import (
	"context"
	"errors"
	"fmt"
	"strings"

	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
)

func (adapter *ChatCompletionsAdapter) ExtractUsage(_ context.Context, input vnextprotocol.UsageInput) (vnextprotocol.Usage, error) {
	for index := len(input.Events) - 1; index >= 0; index-- {
		event := input.Events[index]
		if event.Terminal || len(event.Body) == 0 {
			continue
		}
		if len(event.Body) > maxEventBytes {
			return vnextprotocol.Usage{}, errEventTooLarge
		}
		data, ok := sseDataFromEvent(event.Body)
		if !ok || strings.TrimSpace(string(data)) == "[DONE]" {
			continue
		}
		usage, found, err := usageFromJSON(data)
		if err != nil {
			return vnextprotocol.Usage{}, err
		}
		if found {
			return usage, nil
		}
	}
	if len(input.Body) > 0 {
		if len(input.Body) > maxBodyBytes {
			return vnextprotocol.Usage{}, errBodyTooLarge
		}
		usage, found, err := usageFromJSON(input.Body)
		if err != nil {
			return vnextprotocol.Usage{}, err
		}
		if found {
			return usage, nil
		}
	}
	return vnextprotocol.Usage{}, errors.New("OpenAI response did not contain usage")
}

type usageEnvelope struct {
	Usage *usagePayload `json:"usage"`
}

type usagePayload struct {
	PromptTokens     *int64 `json:"prompt_tokens"`
	InputTokens      *int64 `json:"input_tokens"`
	CompletionTokens *int64 `json:"completion_tokens"`
	OutputTokens     *int64 `json:"output_tokens"`
	TotalTokens      *int64 `json:"total_tokens"`

	PromptDetails     *inputTokenDetails  `json:"prompt_tokens_details"`
	InputDetails      *inputTokenDetails  `json:"input_tokens_details"`
	CompletionDetails *outputTokenDetails `json:"completion_tokens_details"`
	OutputDetails     *outputTokenDetails `json:"output_tokens_details"`
}

type inputTokenDetails struct {
	CachedTokens        *int64 `json:"cached_tokens"`
	CacheWriteTokens    *int64 `json:"cache_write_tokens"`
	CacheCreationTokens *int64 `json:"cache_creation_tokens"`
}

type outputTokenDetails struct {
	ReasoningTokens *int64 `json:"reasoning_tokens"`
}

func usageFromJSON(raw []byte) (vnextprotocol.Usage, bool, error) {
	var envelope usageEnvelope
	if err := decodeSingleJSON(raw, &envelope); err != nil {
		return vnextprotocol.Usage{}, false, errors.New("OpenAI usage envelope is malformed")
	}
	if envelope.Usage == nil {
		return vnextprotocol.Usage{}, false, nil
	}
	usage, err := normalizeUsage(envelope.Usage)
	if err != nil {
		return vnextprotocol.Usage{}, true, err
	}
	return usage, true, nil
}

func normalizeUsage(raw *usagePayload) (vnextprotocol.Usage, error) {
	inputTotal, err := consistentTokenValue("input total", raw.PromptTokens, raw.InputTokens)
	if err != nil {
		return vnextprotocol.Usage{}, err
	}
	outputTotal, err := consistentTokenValue("output total", raw.CompletionTokens, raw.OutputTokens)
	if err != nil {
		return vnextprotocol.Usage{}, err
	}
	if inputTotal == nil || outputTotal == nil {
		return vnextprotocol.Usage{}, errors.New("OpenAI usage is missing input or output token totals")
	}
	if *inputTotal < 0 || *outputTotal < 0 {
		return vnextprotocol.Usage{}, errors.New("OpenAI usage contains negative token totals")
	}

	cacheRead, err := consistentTokenValue("cache read", detailValue(raw.PromptDetails, func(value *inputTokenDetails) *int64 { return value.CachedTokens }), detailValue(raw.InputDetails, func(value *inputTokenDetails) *int64 { return value.CachedTokens }))
	if err != nil {
		return vnextprotocol.Usage{}, err
	}
	promptCacheWrite, err := cacheWriteDetailValue(raw.PromptDetails)
	if err != nil {
		return vnextprotocol.Usage{}, err
	}
	inputCacheWrite, err := cacheWriteDetailValue(raw.InputDetails)
	if err != nil {
		return vnextprotocol.Usage{}, err
	}
	cacheWrite, err := consistentTokenValue(
		"cache write",
		promptCacheWrite,
		inputCacheWrite,
	)
	if err != nil {
		return vnextprotocol.Usage{}, err
	}
	reasoning, err := consistentTokenValue("reasoning", outputDetailValue(raw.CompletionDetails), outputDetailValue(raw.OutputDetails))
	if err != nil {
		return vnextprotocol.Usage{}, err
	}
	cacheReadValue, cacheWriteValue, reasoningValue := tokenOrZero(cacheRead), tokenOrZero(cacheWrite), tokenOrZero(reasoning)
	if cacheReadValue < 0 || cacheWriteValue < 0 || reasoningValue < 0 {
		return vnextprotocol.Usage{}, errors.New("OpenAI usage contains negative token details")
	}
	if cacheReadValue+cacheWriteValue > *inputTotal {
		return vnextprotocol.Usage{}, errors.New("OpenAI cache token details exceed the input token total")
	}
	if reasoningValue > *outputTotal {
		return vnextprotocol.Usage{}, errors.New("OpenAI reasoning tokens exceed the output token total")
	}
	if raw.TotalTokens != nil {
		if *raw.TotalTokens < 0 {
			return vnextprotocol.Usage{}, errors.New("OpenAI usage contains a negative total token count")
		}
		if *raw.TotalTokens != *inputTotal+*outputTotal {
			return vnextprotocol.Usage{}, errors.New("OpenAI usage total does not match input plus output")
		}
	}

	input := *inputTotal - cacheReadValue - cacheWriteValue
	output := *outputTotal - reasoningValue
	return vnextprotocol.Usage{
		InputTokens:      int64Pointer(input),
		OutputTokens:     int64Pointer(output),
		CacheReadTokens:  int64Pointer(cacheReadValue),
		CacheWriteTokens: int64Pointer(cacheWriteValue),
		ReasoningTokens:  int64Pointer(reasoningValue),
	}, nil
}

func consistentTokenValue(label string, values ...*int64) (*int64, error) {
	var selected *int64
	for _, value := range values {
		if value == nil {
			continue
		}
		if selected != nil && *selected != *value {
			return nil, fmt.Errorf("OpenAI usage has conflicting %s values", label)
		}
		selected = value
	}
	return selected, nil
}

func detailValue(details *inputTokenDetails, selectValue func(*inputTokenDetails) *int64) *int64 {
	if details == nil {
		return nil
	}
	return selectValue(details)
}

func outputDetailValue(details *outputTokenDetails) *int64 {
	if details == nil {
		return nil
	}
	return details.ReasoningTokens
}

func cacheWriteDetailValue(details *inputTokenDetails) (*int64, error) {
	if details == nil {
		return nil, nil
	}
	return consistentTokenValue("cache write", details.CacheWriteTokens, details.CacheCreationTokens)
}

func tokenOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func int64Pointer(value int64) *int64 {
	copy := value
	return &copy
}
