package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
)

type anthropicUsagePayload struct {
	InputTokens              *int64 `json:"input_tokens"`
	OutputTokens             *int64 `json:"output_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
	CacheCreation            *struct {
		Ephemeral5MInputTokens *int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1HInputTokens *int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	OutputTokensDetails *struct {
		ThinkingTokens *int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

func (adapter *MessagesAdapter) ExtractUsage(_ context.Context, input vnextprotocol.UsageInput) (vnextprotocol.Usage, error) {
	if len(input.Events) > 0 {
		var cumulative *anthropicUsagePayload
		seenStart, seenDelta, seenStop := false, false, false
		for _, event := range input.Events {
			if len(event.Body) == 0 {
				continue
			}
			if len(event.Body) > maxEventBytes {
				return vnextprotocol.Usage{}, errEventTooLarge
			}
			_, data, hasData := parseSSEEvent(event.Body)
			if !hasData {
				continue
			}
			var envelope struct {
				Type    string                 `json:"type"`
				Message json.RawMessage        `json:"message"`
				Usage   *anthropicUsagePayload `json:"usage"`
			}
			if err := decodeSingleJSON(data, &envelope); err != nil {
				return vnextprotocol.Usage{}, errors.New("Anthropic stream usage event is malformed")
			}
			switch strings.TrimSpace(envelope.Type) {
			case "message_start":
				var message struct {
					Usage *anthropicUsagePayload `json:"usage"`
				}
				if decodeSingleJSON(envelope.Message, &message) != nil || message.Usage == nil {
					return vnextprotocol.Usage{}, errors.New("Anthropic message_start did not contain usage")
				}
				cumulative = mergeUsage(nil, message.Usage)
				seenStart = true
			case "message_delta":
				if envelope.Usage == nil {
					return vnextprotocol.Usage{}, errors.New("Anthropic message_delta did not contain usage")
				}
				cumulative = mergeUsage(cumulative, envelope.Usage)
				seenDelta = true
			case "message_stop":
				seenStop = true
			}
		}
		if !seenStart || !seenDelta || !seenStop || cumulative == nil {
			return vnextprotocol.Usage{}, errors.New("Anthropic completed stream did not contain a complete usage lifecycle")
		}
		return normalizeUsage(cumulative)
	}
	if len(input.Body) == 0 {
		return vnextprotocol.Usage{}, errors.New("Anthropic response did not contain usage")
	}
	if len(input.Body) > maxBodyBytes {
		return vnextprotocol.Usage{}, errBodyTooLarge
	}
	var envelope struct {
		Usage *anthropicUsagePayload `json:"usage"`
	}
	if err := decodeSingleJSON(input.Body, &envelope); err != nil {
		return vnextprotocol.Usage{}, errors.New("Anthropic usage envelope is malformed")
	}
	if envelope.Usage == nil {
		return vnextprotocol.Usage{}, errors.New("Anthropic response did not contain usage")
	}
	return normalizeUsage(envelope.Usage)
}

func normalizeUsage(raw *anthropicUsagePayload) (vnextprotocol.Usage, error) {
	if raw == nil || raw.InputTokens == nil || raw.OutputTokens == nil {
		return vnextprotocol.Usage{}, errors.New("Anthropic usage is missing input or output token totals")
	}
	input, output := *raw.InputTokens, *raw.OutputTokens
	cacheWrite, cacheRead := tokenOrZero(raw.CacheCreationInputTokens), tokenOrZero(raw.CacheReadInputTokens)
	cacheWrite5M, cacheWrite1H := int64(0), int64(0)
	hasCacheWriteTTLDetails := raw.CacheCreation != nil
	if hasCacheWriteTTLDetails {
		cacheWrite5M = tokenOrZero(raw.CacheCreation.Ephemeral5MInputTokens)
		cacheWrite1H = tokenOrZero(raw.CacheCreation.Ephemeral1HInputTokens)
	}
	reasoning := int64(0)
	if raw.OutputTokensDetails != nil {
		reasoning = tokenOrZero(raw.OutputTokensDetails.ThinkingTokens)
	}
	if input < 0 || output < 0 || cacheWrite < 0 || cacheWrite5M < 0 || cacheWrite1H < 0 || cacheRead < 0 || reasoning < 0 {
		return vnextprotocol.Usage{}, errors.New("Anthropic usage contains negative token counts")
	}
	if hasCacheWriteTTLDetails && cacheWrite5M+cacheWrite1H != cacheWrite {
		return vnextprotocol.Usage{}, errors.New("Anthropic cache creation TTL details do not match the cache creation total")
	}
	if reasoning > output {
		return vnextprotocol.Usage{}, errors.New("Anthropic thinking tokens exceed output tokens")
	}
	usage := vnextprotocol.Usage{
		InputTokens:      int64Pointer(input),
		OutputTokens:     int64Pointer(output - reasoning),
		CacheReadTokens:  int64Pointer(cacheRead),
		CacheWriteTokens: int64Pointer(cacheWrite),
		ReasoningTokens:  int64Pointer(reasoning),
	}
	if hasCacheWriteTTLDetails {
		usage.CacheWrite5MTokens = int64Pointer(cacheWrite5M)
		usage.CacheWrite1HTokens = int64Pointer(cacheWrite1H)
	}
	return usage, nil
}

func mergeUsage(base, update *anthropicUsagePayload) *anthropicUsagePayload {
	merged := &anthropicUsagePayload{}
	if base != nil {
		*merged = *base
	}
	if update == nil {
		return merged
	}
	if update.InputTokens != nil {
		merged.InputTokens = update.InputTokens
	}
	if update.OutputTokens != nil {
		merged.OutputTokens = update.OutputTokens
	}
	if update.CacheCreationInputTokens != nil {
		merged.CacheCreationInputTokens = update.CacheCreationInputTokens
	}
	if update.CacheReadInputTokens != nil {
		merged.CacheReadInputTokens = update.CacheReadInputTokens
	}
	if update.CacheCreation != nil {
		merged.CacheCreation = update.CacheCreation
	}
	if update.OutputTokensDetails != nil {
		merged.OutputTokensDetails = update.OutputTokensDetails
	}
	return merged
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

func nonNullJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}
