package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
)

type generateContentResponse struct {
	Candidates     []candidate     `json:"candidates"`
	PromptFeedback *promptFeedback `json:"promptFeedback"`
	Usage          *usagePayload   `json:"usageMetadata"`
	ModelVersion   string          `json:"modelVersion"`
	Error          json.RawMessage `json:"error"`
}

type candidate struct {
	Content      *content `json:"content"`
	FinishReason string   `json:"finishReason"`
}

type content struct {
	Role  string            `json:"role"`
	Parts []json.RawMessage `json:"parts"`
}

type promptFeedback struct {
	BlockReason string `json:"blockReason"`
}

func (adapter *GenerateContentAdapter) DecodeResponse(_ context.Context, input vnextprotocol.ResponseInput) (vnextprotocol.DecodedResponse, error) {
	if len(input.Body) > maxBodyBytes {
		return vnextprotocol.DecodedResponse{}, errBodyTooLarge
	}
	if input.StatusCode < http.StatusOK || input.StatusCode >= http.StatusMultipleChoices {
		return vnextprotocol.DecodedResponse{}, adapter.statusError(input.StatusCode, input.Header, input.Body)
	}
	var response generateContentResponse
	if decodeSingleJSON(input.Body, &response) != nil {
		return vnextprotocol.DecodedResponse{}, errors.New("Gemini returned a malformed GenerateContentResponse")
	}
	if nonNullJSON(response.Error) {
		return vnextprotocol.DecodedResponse{}, adapter.statusError(0, input.Header, input.Body)
	}
	if err := validateResponseShape(&response); err != nil {
		return vnextprotocol.DecodedResponse{}, err
	}
	model := strings.TrimSpace(response.ModelVersion)
	if model == "" {
		return vnextprotocol.DecodedResponse{}, errors.New("Gemini response did not identify its model version")
	}
	return vnextprotocol.DecodedResponse{Model: model, Body: bytes.Clone(input.Body)}, nil
}

func validateResponseShape(response *generateContentResponse) error {
	blocked := response.PromptFeedback != nil && strings.TrimSpace(response.PromptFeedback.BlockReason) != ""
	if len(response.Candidates) == 0 {
		if blocked {
			return nil
		}
		return errors.New("Gemini response did not contain candidates or a prompt block reason")
	}
	for _, candidate := range response.Candidates {
		if candidate.Content == nil {
			if terminalFinishReason(candidate.FinishReason) {
				continue
			}
			return errors.New("Gemini candidate did not contain content")
		}
		if len(candidate.Content.Parts) == 0 && !terminalFinishReason(candidate.FinishReason) {
			return errors.New("Gemini candidate content did not contain parts")
		}
		for _, part := range candidate.Content.Parts {
			var object map[string]json.RawMessage
			if decodeSingleJSON(part, &object) != nil || len(object) == 0 {
				return errors.New("Gemini candidate contained a malformed content part")
			}
		}
	}
	return nil
}

func terminalFinishReason(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && value != "FINISH_REASON_UNSPECIFIED"
}

func responseHasSemanticContent(response *generateContentResponse) bool {
	for _, candidate := range response.Candidates {
		if candidate.Content == nil {
			continue
		}
		for _, raw := range candidate.Content.Parts {
			var part map[string]json.RawMessage
			if decodeSingleJSON(raw, &part) != nil {
				continue
			}
			for key, value := range part {
				if key == "thought" {
					continue
				}
				if semanticJSONValue(value) {
					return true
				}
			}
		}
	}
	return false
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
