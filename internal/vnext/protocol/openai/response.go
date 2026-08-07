package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
)

func (adapter *ChatCompletionsAdapter) DecodeResponse(_ context.Context, input vnextprotocol.ResponseInput) (vnextprotocol.DecodedResponse, error) {
	if len(input.Body) > maxBodyBytes {
		return vnextprotocol.DecodedResponse{}, errBodyTooLarge
	}
	if input.StatusCode < http.StatusOK || input.StatusCode >= http.StatusMultipleChoices {
		return vnextprotocol.DecodedResponse{}, adapter.statusError(input.StatusCode, input.Header, input.Body)
	}

	var envelope struct {
		Model   string             `json:"model"`
		Error   json.RawMessage    `json:"error"`
		Choices []completionChoice `json:"choices"`
	}
	if err := decodeSingleJSON(input.Body, &envelope); err != nil {
		return vnextprotocol.DecodedResponse{}, errors.New("OpenAI returned a malformed chat completion")
	}
	if nonNullJSON(envelope.Error) {
		return vnextprotocol.DecodedResponse{}, errors.New("OpenAI returned an error envelope with a success status")
	}
	if len(envelope.Choices) == 0 {
		return vnextprotocol.DecodedResponse{}, errors.New("OpenAI chat completion did not contain choices")
	}
	semantic := false
	for _, choice := range envelope.Choices {
		if choice.hasSemanticOutput() {
			semantic = true
			break
		}
	}
	if !semantic {
		return vnextprotocol.DecodedResponse{}, errors.New("OpenAI chat completion contained no semantic model output")
	}
	model := strings.TrimSpace(envelope.Model)
	if model == "" {
		return vnextprotocol.DecodedResponse{}, errors.New("OpenAI chat completion did not identify its model")
	}
	return vnextprotocol.DecodedResponse{Model: model, Body: bytes.Clone(input.Body)}, nil
}

type completionChoice struct {
	Text    json.RawMessage    `json:"text"`
	Message *completionMessage `json:"message"`
}

type completionMessage struct {
	Content          json.RawMessage   `json:"content"`
	ReasoningContent json.RawMessage   `json:"reasoning_content"`
	Refusal          json.RawMessage   `json:"refusal"`
	Audio            json.RawMessage   `json:"audio"`
	ToolCalls        []json.RawMessage `json:"tool_calls"`
	FunctionCall     json.RawMessage   `json:"function_call"`
}

func (choice completionChoice) hasSemanticOutput() bool {
	if semanticJSONValue(choice.Text) {
		return true
	}
	if choice.Message == nil {
		return false
	}
	message := choice.Message
	return semanticJSONValue(message.Content) || semanticJSONValue(message.ReasoningContent) ||
		semanticJSONValue(message.Refusal) || nonNullJSON(message.Audio) || len(message.ToolCalls) > 0 ||
		nonEmptyJSONObject(message.FunctionCall)
}

func semanticJSONValue(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	var text string
	if json.Unmarshal(trimmed, &text) == nil {
		return strings.TrimSpace(text) != ""
	}
	var list []json.RawMessage
	if json.Unmarshal(trimmed, &list) == nil {
		return len(list) > 0
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(trimmed, &object) == nil && len(object) > 0
}

func nonNullJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func nonEmptyJSONObject(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && len(object) > 0
}
