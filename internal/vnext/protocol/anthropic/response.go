package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
)

func (adapter *MessagesAdapter) DecodeResponse(_ context.Context, input vnextprotocol.ResponseInput) (vnextprotocol.DecodedResponse, error) {
	if len(input.Body) > maxBodyBytes {
		return vnextprotocol.DecodedResponse{}, errBodyTooLarge
	}
	if input.StatusCode < http.StatusOK || input.StatusCode >= http.StatusMultipleChoices {
		return vnextprotocol.DecodedResponse{}, adapter.statusError(input.StatusCode, input.Header, input.Body)
	}
	var message struct {
		Type    string                 `json:"type"`
		Role    string                 `json:"role"`
		Model   string                 `json:"model"`
		Error   json.RawMessage        `json:"error"`
		Content *[]json.RawMessage     `json:"content"`
		Usage   *anthropicUsagePayload `json:"usage"`
	}
	if err := decodeSingleJSON(input.Body, &message); err != nil {
		return vnextprotocol.DecodedResponse{}, errors.New("Anthropic returned a malformed Message object")
	}
	if strings.TrimSpace(message.Type) == "error" || nonNullJSON(message.Error) {
		return vnextprotocol.DecodedResponse{}, adapter.statusError(0, input.Header, input.Body)
	}
	if strings.TrimSpace(message.Type) != "message" || strings.TrimSpace(message.Role) != "assistant" {
		return vnextprotocol.DecodedResponse{}, errors.New("Anthropic response is not an assistant Message object")
	}
	if message.Content == nil || len(*message.Content) == 0 {
		return vnextprotocol.DecodedResponse{}, errors.New("Anthropic Message did not contain content")
	}
	for _, block := range *message.Content {
		if _, _, err := validateContentBlock(block); err != nil {
			return vnextprotocol.DecodedResponse{}, err
		}
	}
	model := strings.TrimSpace(message.Model)
	if model == "" {
		return vnextprotocol.DecodedResponse{}, errors.New("Anthropic Message did not identify its model")
	}
	return vnextprotocol.DecodedResponse{Model: model, Body: bytes.Clone(input.Body)}, nil
}

func contentBlockIsSemantic(raw json.RawMessage) (bool, error) {
	block, blockType, err := validateContentBlock(raw)
	if err != nil {
		return false, err
	}
	switch blockType {
	case "text":
		return semanticJSONValue(block["text"]), nil
	case "thinking":
		return semanticJSONValue(block["thinking"]), nil
	case "redacted_thinking":
		return semanticJSONValue(block["data"]), nil
	case "tool_use":
		return semanticJSONValue(block["name"]) || nonNullJSON(block["input"]), nil
	default:
		return len(block) > 1, nil
	}
}

func validateContentBlock(raw json.RawMessage) (map[string]json.RawMessage, string, error) {
	var block map[string]json.RawMessage
	if err := decodeSingleJSON(raw, &block); err != nil || block == nil {
		return nil, "", errors.New("Anthropic Message contained a malformed content block")
	}
	var blockType string
	if json.Unmarshal(block["type"], &blockType) != nil || strings.TrimSpace(blockType) == "" {
		return nil, "", errors.New("Anthropic Message content block did not contain a type")
	}
	requireString := func(field string, allowEmpty bool) error {
		var value string
		if json.Unmarshal(block[field], &value) != nil || (!allowEmpty && strings.TrimSpace(value) == "") {
			return fmt.Errorf("Anthropic %s content block has an invalid %s field", blockType, field)
		}
		return nil
	}
	switch blockType {
	case "text":
		if err := requireString("text", true); err != nil {
			return nil, "", err
		}
	case "thinking":
		if err := requireString("thinking", true); err != nil {
			return nil, "", err
		}
	case "redacted_thinking":
		if err := requireString("data", false); err != nil {
			return nil, "", err
		}
	case "tool_use":
		if err := requireString("id", false); err != nil {
			return nil, "", err
		}
		if err := requireString("name", false); err != nil {
			return nil, "", err
		}
		if !nonNullJSON(block["input"]) {
			return nil, "", errors.New("Anthropic tool_use content block has an invalid input field")
		}
	default:
		if len(block) <= 1 {
			return nil, "", errors.New("Anthropic content block did not contain block data")
		}
	}
	return block, blockType, nil
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
