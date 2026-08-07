package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
)

func (adapter *ResponsesAdapter) ExtractUsage(_ context.Context, input vnextprotocol.UsageInput) (vnextprotocol.Usage, error) {
	for index := len(input.Events) - 1; index >= 0; index-- {
		event := input.Events[index]
		if len(event.Body) == 0 {
			continue
		}
		if len(event.Body) > maxEventBytes {
			return vnextprotocol.Usage{}, errEventTooLarge
		}
		data, ok := sseDataFromEvent(event.Body)
		if !ok || strings.TrimSpace(string(data)) == "[DONE]" {
			continue
		}
		usage, found, err := responsesUsageFromJSON(data)
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
		usage, found, err := responsesUsageFromJSON(input.Body)
		if err != nil {
			return vnextprotocol.Usage{}, err
		}
		if found {
			return usage, nil
		}
	}
	return vnextprotocol.Usage{}, errors.New("OpenAI Responses response did not contain usage")
}

func responsesUsageFromJSON(raw []byte) (vnextprotocol.Usage, bool, error) {
	var envelope struct {
		Usage    *usagePayload   `json:"usage"`
		Response json.RawMessage `json:"response"`
	}
	if err := decodeSingleJSON(raw, &envelope); err != nil {
		return vnextprotocol.Usage{}, false, errors.New("OpenAI Responses usage envelope is malformed")
	}
	usage := envelope.Usage
	if len(bytes.TrimSpace(envelope.Response)) > 0 && !isJSONNull(envelope.Response) {
		var nested struct {
			Usage *usagePayload `json:"usage"`
		}
		if err := decodeSingleJSON(envelope.Response, &nested); err != nil {
			return vnextprotocol.Usage{}, false, errors.New("OpenAI Responses usage response is malformed")
		}
		if nested.Usage != nil {
			usage = nested.Usage
		}
	}
	if usage == nil {
		return vnextprotocol.Usage{}, false, nil
	}
	normalized, err := normalizeUsage(usage)
	if err != nil {
		return vnextprotocol.Usage{}, true, err
	}
	return normalized, true, nil
}
