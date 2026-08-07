package gemini

import (
	"context"
	"errors"

	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
)

type usagePayload struct {
	PromptTokenCount        *int64 `json:"promptTokenCount"`
	CachedContentTokenCount *int64 `json:"cachedContentTokenCount"`
	CandidatesTokenCount    *int64 `json:"candidatesTokenCount"`
	ToolUsePromptTokenCount *int64 `json:"toolUsePromptTokenCount"`
	ThoughtsTokenCount      *int64 `json:"thoughtsTokenCount"`
	TotalTokenCount         *int64 `json:"totalTokenCount"`
}

func (adapter *GenerateContentAdapter) ExtractUsage(_ context.Context, input vnextprotocol.UsageInput) (vnextprotocol.Usage, error) {
	for index := len(input.Events) - 1; index >= 0; index-- {
		event := input.Events[index]
		if len(event.Body) == 0 {
			continue
		}
		if len(event.Body) > maxEventBytes {
			return vnextprotocol.Usage{}, errEventTooLarge
		}
		data, ok := sseData(event.Body)
		if !ok {
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
	if len(input.Body) == 0 {
		return vnextprotocol.Usage{}, errors.New("Gemini response did not contain usage metadata")
	}
	if len(input.Body) > maxBodyBytes {
		return vnextprotocol.Usage{}, errBodyTooLarge
	}
	usage, found, err := usageFromJSON(input.Body)
	if err != nil {
		return vnextprotocol.Usage{}, err
	}
	if !found {
		return vnextprotocol.Usage{}, errors.New("Gemini response did not contain usage metadata")
	}
	return usage, nil
}

func usageFromJSON(raw []byte) (vnextprotocol.Usage, bool, error) {
	var envelope struct {
		Usage *usagePayload `json:"usageMetadata"`
	}
	if decodeSingleJSON(raw, &envelope) != nil {
		return vnextprotocol.Usage{}, false, errors.New("Gemini usage envelope is malformed")
	}
	if envelope.Usage == nil {
		return vnextprotocol.Usage{}, false, nil
	}
	usage, err := normalizeUsage(envelope.Usage)
	return usage, true, err
}

func normalizeUsage(raw *usagePayload) (vnextprotocol.Usage, error) {
	if raw == nil || raw.PromptTokenCount == nil {
		return vnextprotocol.Usage{}, errors.New("Gemini usage is missing the prompt token total")
	}
	prompt := *raw.PromptTokenCount
	cacheRead := tokenOrZero(raw.CachedContentTokenCount)
	output := tokenOrZero(raw.CandidatesTokenCount)
	toolInput := tokenOrZero(raw.ToolUsePromptTokenCount)
	reasoning := tokenOrZero(raw.ThoughtsTokenCount)
	if prompt < 0 || cacheRead < 0 || output < 0 || toolInput < 0 || reasoning < 0 {
		return vnextprotocol.Usage{}, errors.New("Gemini usage contains negative token counts")
	}
	if cacheRead > prompt {
		return vnextprotocol.Usage{}, errors.New("Gemini cached content tokens exceed prompt tokens")
	}
	if raw.TotalTokenCount != nil {
		if *raw.TotalTokenCount < 0 {
			return vnextprotocol.Usage{}, errors.New("Gemini usage contains a negative total token count")
		}
		if *raw.TotalTokenCount != prompt+toolInput+output+reasoning {
			return vnextprotocol.Usage{}, errors.New("Gemini usage total does not match prompt plus tool-use prompt plus thoughts plus candidates")
		}
	}
	return vnextprotocol.Usage{
		InputTokens:      int64Pointer(prompt - cacheRead + toolInput),
		OutputTokens:     int64Pointer(output),
		CacheReadTokens:  int64Pointer(cacheRead),
		CacheWriteTokens: int64Pointer(0),
		ReasoningTokens:  int64Pointer(reasoning),
	}, nil
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
