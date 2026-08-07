package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
)

var ErrInvalidResponsesTerminator = errors.New("invalid_responses_terminator")

func (adapter *ResponsesAdapter) DecodeStream(ctx context.Context, input vnextprotocol.StreamInput, emit func(vnextprotocol.StreamEvent) error) (vnextprotocol.StreamResult, error) {
	return decodeOpenAIEventStream(ctx, input, emit, adapter.statusError, decodeResponsesSSEEvent)
}

type responsesStreamEnvelope struct {
	Type            string          `json:"type"`
	Delta           json.RawMessage `json:"delta"`
	PartialImageB64 json.RawMessage `json:"partial_image_b64"`
	Response        json.RawMessage `json:"response"`
}

func decodeResponsesSSEEvent(raw []byte) (vnextprotocol.StreamEvent, string, bool, error) {
	event := vnextprotocol.StreamEvent{Body: bytes.Clone(raw)}
	data, hasData := sseDataFromEvent(raw)
	if !hasData {
		return event, "", len(bytes.TrimSpace(raw)) > 0, nil
	}
	if strings.TrimSpace(string(data)) == "[DONE]" {
		return vnextprotocol.StreamEvent{}, "", false, fmt.Errorf("%w: Responses streams terminate with response.completed", ErrInvalidResponsesTerminator)
	}
	var envelope responsesStreamEnvelope
	if err := decodeSingleJSON(data, &envelope); err != nil {
		return vnextprotocol.StreamEvent{}, "", false, errors.New("OpenAI Responses stream contained a malformed JSON event")
	}
	eventType := strings.TrimSpace(envelope.Type)
	if eventType == "" {
		return vnextprotocol.StreamEvent{}, "", false, errors.New("OpenAI Responses stream event did not contain a type")
	}
	model, err := responseModel(envelope.Response)
	if err != nil {
		return vnextprotocol.StreamEvent{}, "", false, err
	}
	switch eventType {
	case "response.completed":
		model, err = validateResponsesDocument(envelope.Response)
		if err != nil {
			return vnextprotocol.StreamEvent{}, "", false, err
		}
		event.Terminal = true
		return event, model, true, nil
	case "response.failed":
		return vnextprotocol.StreamEvent{}, model, false, responseFailedError(envelope.Response)
	case "response.incomplete":
		return vnextprotocol.StreamEvent{}, model, false, fmt.Errorf("%w: OpenAI response stream ended incomplete", ErrResponseIncomplete)
	case "response.cancelled", "response.canceled":
		return vnextprotocol.StreamEvent{}, model, false, fmt.Errorf("%w: OpenAI response stream was canceled", ErrResponseCanceled)
	case "error":
		decoded := decodeOpenAIError(vnextprotocol.ErrorInput{Body: data})
		return vnextprotocol.StreamEvent{}, model, false, fmt.Errorf("%s: %s", decoded.Class, decoded.Message)
	default:
		event.Semantic = responsesEventIsSemantic(eventType, envelope.Delta, envelope.PartialImageB64)
		return event, model, true, nil
	}
}

func responseModel(raw json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(raw)) == 0 || isJSONNull(raw) {
		return "", nil
	}
	var response struct {
		Model string `json:"model"`
	}
	if err := decodeSingleJSON(raw, &response); err != nil {
		return "", errors.New("OpenAI Responses stream contained a malformed response object")
	}
	return strings.TrimSpace(response.Model), nil
}

func responsesEventIsSemantic(eventType string, delta, partialImage json.RawMessage) bool {
	if strings.HasSuffix(eventType, ".delta") && semanticJSONValue(delta) {
		return true
	}
	return eventType == "response.image_generation_call.partial_image" && semanticJSONValue(partialImage)
}
