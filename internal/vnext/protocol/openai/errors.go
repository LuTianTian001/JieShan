package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
)

func (adapter *ChatCompletionsAdapter) DecodeError(_ context.Context, input vnextprotocol.ErrorInput) (vnextprotocol.DecodedError, error) {
	return decodeOpenAIError(input), nil
}

func (adapter *ResponsesAdapter) DecodeError(_ context.Context, input vnextprotocol.ErrorInput) (vnextprotocol.DecodedError, error) {
	return decodeOpenAIError(input), nil
}

func decodeOpenAIError(input vnextprotocol.ErrorInput) vnextprotocol.DecodedError {
	code := safeOpenAIErrorCode(input.Body)
	decoded := classifyStatus(input.StatusCode, code)
	if code != "" {
		decoded.Code = code
	}
	return decoded
}

func (adapter *ChatCompletionsAdapter) statusError(status int, header http.Header, body []byte) error {
	return openAIStatusError(status, header, body)
}

func (adapter *ResponsesAdapter) statusError(status int, header http.Header, body []byte) error {
	return openAIStatusError(status, header, body)
}

func openAIStatusError(status int, header http.Header, body []byte) error {
	decoded := decodeOpenAIError(vnextprotocol.ErrorInput{StatusCode: status, Header: header, Body: body})
	return fmt.Errorf("%s: %s", decoded.Class, decoded.Message)
}

func classifyStatus(status int, parsedCode string) vnextprotocol.DecodedError {
	result := vnextprotocol.DecodedError{Code: "upstream_error", Message: "upstream request failed", Class: "unknown"}
	if status > 0 {
		result.Code = "http_" + strconv.Itoa(status)
	}
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		result.Class = "client_invalid"
		result.Message = "upstream rejected the request"
	case http.StatusUnauthorized:
		result.Class = "credential_auth"
		result.Message = "upstream credential authentication failed"
		result.CredentialFailure = true
	case http.StatusPaymentRequired:
		result.Class = "credential_payment_required"
		result.Message = "upstream credential has insufficient balance"
		result.CredentialFailure = true
	case http.StatusForbidden:
		result.Class = "credential_permission"
		result.Message = "upstream credential is not permitted for this model"
		result.CredentialFailure = true
	case http.StatusNotFound:
		result.Class = "target_misconfigured"
		result.Message = "upstream model or endpoint was not found"
		if parsedCode == "model_not_found" {
			result.Class = "model_unsupported"
		}
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly:
		result.Class = "upstream_transient"
		result.Message = "upstream request failed temporarily"
		result.Retryable = true
	case http.StatusTooManyRequests:
		result.Class = "credential_rate_limited"
		result.Message = "upstream credential is rate limited"
		result.Retryable = true
		result.CredentialFailure = true
	default:
		if status >= http.StatusInternalServerError && status <= 599 {
			result.Class = "upstream_transient"
			result.Message = "upstream service is temporarily unavailable"
			result.Retryable = true
		}
	}
	if status < http.StatusBadRequest {
		switch parsedCode {
		case "invalid_api_key":
			result.Class, result.Message, result.CredentialFailure = "credential_auth", "upstream credential authentication failed", true
		case "permission_denied", "data_residency_mismatch":
			result.Class, result.Message, result.CredentialFailure = "credential_permission", "upstream credential is not permitted for this model", true
		case "rate_limit_exceeded":
			result.Class, result.Message, result.Retryable, result.CredentialFailure = "credential_rate_limited", "upstream credential is rate limited", true, true
		case "insufficient_quota":
			result.Class, result.Message, result.CredentialFailure = "credential_payment_required", "upstream credential has insufficient balance", true
		case "server_error", "service_unavailable", "vector_store_timeout":
			result.Class, result.Message, result.Retryable = "upstream_transient", "upstream service is temporarily unavailable", true
		case "model_not_found":
			result.Class, result.Message = "model_unsupported", "upstream model was not found"
		case "bad_request", "context_length_exceeded", "invalid_request_error", "invalid_prompt", "unsupported_value",
			"bio_policy", "invalid_image", "invalid_image_format", "invalid_base64_image", "invalid_image_url",
			"image_too_large", "image_too_small", "image_parse_error", "image_content_policy_violation",
			"invalid_image_mode", "image_file_too_large", "unsupported_image_media_type", "empty_image_file",
			"failed_to_download_image", "image_file_not_found":
			result.Class, result.Message = "client_invalid", "upstream rejected the request"
		}
	}
	return result
}

func safeOpenAIErrorCode(body []byte) string {
	if len(body) == 0 || len(body) > maxBodyBytes {
		return ""
	}
	var envelope struct {
		Error json.RawMessage `json:"error"`
		Code  json.RawMessage `json:"code"`
		Type  json.RawMessage `json:"type"`
	}
	if decodeSingleJSON(body, &envelope) != nil {
		return ""
	}
	candidates := []json.RawMessage{envelope.Code, envelope.Type}
	if nonNullJSON(envelope.Error) {
		var nested struct {
			Code json.RawMessage `json:"code"`
			Type json.RawMessage `json:"type"`
		}
		if json.Unmarshal(envelope.Error, &nested) == nil {
			candidates = append([]json.RawMessage{nested.Code, nested.Type}, candidates...)
		}
	}
	for _, raw := range candidates {
		var value string
		if json.Unmarshal(raw, &value) != nil {
			continue
		}
		value = strings.ToLower(strings.TrimSpace(value))
		if knownSafeErrorCodes[value] {
			return value
		}
	}
	return ""
}

var knownSafeErrorCodes = map[string]bool{
	"bad_request":                    true,
	"bio_policy":                     true,
	"context_length_exceeded":        true,
	"data_residency_mismatch":        true,
	"empty_image_file":               true,
	"failed_to_download_image":       true,
	"image_content_policy_violation": true,
	"image_file_not_found":           true,
	"image_file_too_large":           true,
	"image_parse_error":              true,
	"image_too_large":                true,
	"image_too_small":                true,
	"insufficient_quota":             true,
	"invalid_api_key":                true,
	"invalid_base64_image":           true,
	"invalid_image":                  true,
	"invalid_image_format":           true,
	"invalid_image_mode":             true,
	"invalid_image_url":              true,
	"invalid_prompt":                 true,
	"invalid_request_error":          true,
	"model_not_found":                true,
	"permission_denied":              true,
	"rate_limit_exceeded":            true,
	"server_error":                   true,
	"service_unavailable":            true,
	"unsupported_image_media_type":   true,
	"unsupported_value":              true,
	"vector_store_timeout":           true,
}
