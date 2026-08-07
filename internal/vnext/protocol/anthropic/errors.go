package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
)

func (adapter *MessagesAdapter) DecodeError(_ context.Context, input vnextprotocol.ErrorInput) (vnextprotocol.DecodedError, error) {
	return decodeAnthropicError(input), nil
}

func (adapter *MessagesAdapter) statusError(status int, header http.Header, body []byte) error {
	decoded := decodeAnthropicError(vnextprotocol.ErrorInput{StatusCode: status, Header: header, Body: body})
	return fmt.Errorf("%s: %s", decoded.Class, decoded.Message)
}

func decodeAnthropicError(input vnextprotocol.ErrorInput) vnextprotocol.DecodedError {
	errorType := safeAnthropicErrorType(input.Body)
	decoded := classifyAnthropicFailure(input.StatusCode, errorType)
	if errorType != "" {
		decoded.Code = errorType
	}
	return decoded
}

func classifyAnthropicFailure(status int, errorType string) vnextprotocol.DecodedError {
	result := vnextprotocol.DecodedError{Code: "upstream_error", Message: "upstream request failed", Class: "unknown"}
	if status > 0 {
		result.Code = "http_" + strconv.Itoa(status)
	}
	switch status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		result.Class, result.Message = "client_invalid", "upstream rejected the request"
	case http.StatusUnauthorized:
		result.Class, result.Message, result.CredentialFailure = "credential_auth", "upstream credential authentication failed", true
	case http.StatusPaymentRequired:
		result.Class, result.Message, result.CredentialFailure = "credential_payment_required", "upstream credential has insufficient balance", true
	case http.StatusForbidden:
		result.Class, result.Message, result.CredentialFailure = "credential_permission", "upstream credential is not permitted for this model", true
	case http.StatusNotFound:
		result.Class, result.Message = "target_misconfigured", "upstream model or endpoint was not found"
	case http.StatusConflict, http.StatusRequestTimeout:
		result.Class, result.Message, result.Retryable = "upstream_transient", "upstream request failed temporarily", true
	case http.StatusTooManyRequests:
		result.Class, result.Message, result.Retryable, result.CredentialFailure = "credential_rate_limited", "upstream credential is rate limited", true, true
	case http.StatusGatewayTimeout, 529:
		result.Class, result.Message, result.Retryable = "upstream_transient", "upstream service is temporarily unavailable", true
	default:
		if status >= http.StatusInternalServerError && status <= 599 {
			result.Class, result.Message, result.Retryable = "upstream_transient", "upstream service is temporarily unavailable", true
		}
	}
	if status < http.StatusBadRequest {
		switch errorType {
		case "invalid_request_error", "request_too_large":
			result.Class, result.Message = "client_invalid", "upstream rejected the request"
		case "authentication_error":
			result.Class, result.Message, result.CredentialFailure = "credential_auth", "upstream credential authentication failed", true
		case "billing_error":
			result.Class, result.Message, result.CredentialFailure = "credential_payment_required", "upstream credential has insufficient balance", true
		case "permission_error":
			result.Class, result.Message, result.CredentialFailure = "credential_permission", "upstream credential is not permitted for this model", true
		case "not_found_error":
			result.Class, result.Message = "target_misconfigured", "upstream model or endpoint was not found"
		case "conflict_error", "api_error", "timeout_error", "overloaded_error":
			result.Class, result.Message, result.Retryable = "upstream_transient", "upstream service is temporarily unavailable", true
		case "rate_limit_error":
			result.Class, result.Message, result.Retryable, result.CredentialFailure = "credential_rate_limited", "upstream credential is rate limited", true, true
		}
	}
	return result
}

func safeAnthropicErrorType(body []byte) string {
	if len(body) == 0 || len(body) > maxBodyBytes {
		return ""
	}
	var envelope struct {
		Type  json.RawMessage `json:"type"`
		Error json.RawMessage `json:"error"`
	}
	if decodeSingleJSON(body, &envelope) != nil {
		return ""
	}
	candidates := make([]json.RawMessage, 0, 2)
	if len(envelope.Error) > 0 && !isJSONNull(envelope.Error) {
		var nested struct {
			Type json.RawMessage `json:"type"`
		}
		if json.Unmarshal(envelope.Error, &nested) == nil {
			candidates = append(candidates, nested.Type)
		}
	}
	candidates = append(candidates, envelope.Type)
	for _, raw := range candidates {
		var value string
		if json.Unmarshal(raw, &value) != nil {
			continue
		}
		value = strings.ToLower(strings.TrimSpace(value))
		if knownAnthropicErrorTypes[value] {
			return value
		}
	}
	return ""
}

var knownAnthropicErrorTypes = map[string]bool{
	"api_error":             true,
	"authentication_error":  true,
	"billing_error":         true,
	"conflict_error":        true,
	"invalid_request_error": true,
	"not_found_error":       true,
	"overloaded_error":      true,
	"permission_error":      true,
	"rate_limit_error":      true,
	"request_too_large":     true,
	"timeout_error":         true,
}
