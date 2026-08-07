package openai

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

var (
	ErrResponseFailed     = errors.New("response_failed")
	ErrResponseIncomplete = errors.New("response_incomplete")
	ErrResponseCanceled   = errors.New("response_canceled")
)

func (adapter *ResponsesAdapter) DecodeResponse(_ context.Context, input vnextprotocol.ResponseInput) (vnextprotocol.DecodedResponse, error) {
	if len(input.Body) > maxBodyBytes {
		return vnextprotocol.DecodedResponse{}, errBodyTooLarge
	}
	if input.StatusCode < http.StatusOK || input.StatusCode >= http.StatusMultipleChoices {
		return vnextprotocol.DecodedResponse{}, adapter.statusError(input.StatusCode, input.Header, input.Body)
	}
	model, err := validateResponsesDocument(input.Body)
	if err != nil {
		return vnextprotocol.DecodedResponse{}, err
	}
	return vnextprotocol.DecodedResponse{Model: model, Body: bytes.Clone(input.Body)}, nil
}

type responsesDocument struct {
	Object            string             `json:"object"`
	Status            string             `json:"status"`
	Model             string             `json:"model"`
	Error             json.RawMessage    `json:"error"`
	IncompleteDetails json.RawMessage    `json:"incomplete_details"`
	Output            *[]json.RawMessage `json:"output"`
	Usage             *usagePayload      `json:"usage"`
}

func validateResponsesDocument(raw []byte) (string, error) {
	var document responsesDocument
	if err := decodeSingleJSON(raw, &document); err != nil {
		return "", errors.New("OpenAI returned a malformed Responses object")
	}
	if strings.TrimSpace(document.Object) != "response" {
		return "", errors.New("OpenAI Responses object has an invalid object type")
	}
	status := strings.ToLower(strings.TrimSpace(document.Status))
	switch status {
	case "failed":
		return "", responseFailedError(raw)
	case "incomplete":
		return "", fmt.Errorf("%w: OpenAI response ended incomplete", ErrResponseIncomplete)
	case "cancelled", "canceled":
		return "", fmt.Errorf("%w: OpenAI response was canceled", ErrResponseCanceled)
	case "completed":
	default:
		return "", errors.New("OpenAI response did not reach completed status")
	}
	if nonNullJSON(document.Error) {
		return "", errors.New("OpenAI completed response contained an error object")
	}
	if document.Output == nil || len(*document.Output) == 0 {
		return "", errors.New("OpenAI completed response did not contain output")
	}
	meaningful := false
	for _, item := range *document.Output {
		var output struct {
			Type string `json:"type"`
		}
		if decodeSingleJSON(item, &output) != nil || strings.TrimSpace(output.Type) == "" {
			return "", errors.New("OpenAI completed response contained a malformed output item")
		}
		meaningful = true
	}
	if !meaningful {
		return "", errors.New("OpenAI completed response did not contain semantic output")
	}
	model := strings.TrimSpace(document.Model)
	if model == "" {
		return "", errors.New("OpenAI completed response did not identify its model")
	}
	return model, nil
}

func responseFailedError(raw []byte) error {
	code := safeOpenAIErrorCode(raw)
	decoded := classifyStatus(0, code)
	if decoded.Class == "unknown" {
		return fmt.Errorf("%w: OpenAI response failed", ErrResponseFailed)
	}
	return fmt.Errorf("%w: %s: %s", ErrResponseFailed, decoded.Class, decoded.Message)
}
