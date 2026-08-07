package gemini

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
)

func (adapter *GenerateContentAdapter) DecodeError(_ context.Context, input vnextprotocol.ErrorInput) (vnextprotocol.DecodedError, error) {
	return decodeGeminiError(input), nil
}

func (adapter *GenerateContentAdapter) statusError(status int, header http.Header, body []byte) error {
	decoded := decodeGeminiError(vnextprotocol.ErrorInput{StatusCode: status, Header: header, Body: body})
	return fmt.Errorf("%s: %s", decoded.Class, decoded.Message)
}

func decodeGeminiError(input vnextprotocol.ErrorInput) vnextprotocol.DecodedError {
	rpcStatus, reason := safeGeminiErrorIdentity(input.Body)
	decoded := classifyGeminiFailure(input.StatusCode, rpcStatus, reason)
	if reason != "" {
		decoded.Code = strings.ToLower(reason)
	} else if rpcStatus != "" {
		decoded.Code = strings.ToLower(rpcStatus)
	}
	return decoded
}

func classifyGeminiFailure(status int, rpcStatus, reason string) vnextprotocol.DecodedError {
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
		result.Class, result.Message = "model_unsupported", "upstream model was not found"
	case http.StatusConflict, http.StatusRequestTimeout, http.StatusTooEarly:
		result.Class, result.Message, result.Retryable = "upstream_transient", "upstream request failed temporarily", true
	case http.StatusTooManyRequests:
		result.Class, result.Message, result.Retryable, result.CredentialFailure = "credential_rate_limited", "upstream credential is rate limited", true, true
	default:
		if status >= http.StatusInternalServerError && status <= 599 {
			result.Class, result.Message, result.Retryable = "upstream_transient", "upstream service is temporarily unavailable", true
		}
	}
	if status < http.StatusBadRequest {
		switch rpcStatus {
		case "INVALID_ARGUMENT", "FAILED_PRECONDITION", "OUT_OF_RANGE", "ALREADY_EXISTS":
			result.Class, result.Message = "client_invalid", "upstream rejected the request"
		case "UNAUTHENTICATED":
			result.Class, result.Message, result.CredentialFailure = "credential_auth", "upstream credential authentication failed", true
		case "PERMISSION_DENIED":
			result.Class, result.Message, result.CredentialFailure = "credential_permission", "upstream credential is not permitted for this model", true
		case "NOT_FOUND":
			result.Class, result.Message = "model_unsupported", "upstream model was not found"
		case "RESOURCE_EXHAUSTED":
			result.Class, result.Message, result.Retryable, result.CredentialFailure = "credential_rate_limited", "upstream credential is rate limited", true, true
		case "ABORTED", "CANCELLED", "DEADLINE_EXCEEDED", "INTERNAL", "UNAVAILABLE":
			result.Class, result.Message, result.Retryable = "upstream_transient", "upstream service is temporarily unavailable", true
		}
	}
	switch reason {
	case "API_KEY_INVALID":
		result.Class, result.Message, result.CredentialFailure = "credential_auth", "upstream credential authentication failed", true
	case "API_KEY_SERVICE_BLOCKED", "API_KEY_IP_ADDRESS_BLOCKED", "API_KEY_HTTP_REFERRER_BLOCKED":
		result.Class, result.Message, result.CredentialFailure = "credential_permission", "upstream credential is not permitted for this model", true
	case "BILLING_DISABLED", "BILLING_NOT_ACTIVE":
		result.Class, result.Message, result.CredentialFailure = "credential_payment_required", "upstream credential has insufficient balance", true
	case "RATE_LIMIT_EXCEEDED", "QUOTA_EXCEEDED":
		result.Class, result.Message, result.Retryable, result.CredentialFailure = "credential_rate_limited", "upstream credential is rate limited", true, true
	case "MODEL_NOT_FOUND":
		result.Class, result.Message = "model_unsupported", "upstream model was not found"
	}
	return result
}

func safeGeminiErrorIdentity(body []byte) (string, string) {
	if len(body) == 0 || len(body) > maxBodyBytes {
		return "", ""
	}
	var envelope struct {
		Error struct {
			Status  string `json:"status"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	if decodeSingleJSON(body, &envelope) != nil {
		return "", ""
	}
	status := strings.ToUpper(strings.TrimSpace(envelope.Error.Status))
	if !knownRPCStatuses[status] {
		status = ""
	}
	for _, detail := range envelope.Error.Details {
		reason := strings.ToUpper(strings.TrimSpace(detail.Reason))
		if knownErrorReasons[reason] {
			return status, reason
		}
	}
	return status, ""
}

var knownRPCStatuses = map[string]bool{
	"ABORTED": true, "ALREADY_EXISTS": true, "CANCELLED": true, "DEADLINE_EXCEEDED": true,
	"FAILED_PRECONDITION": true, "INTERNAL": true, "INVALID_ARGUMENT": true, "NOT_FOUND": true,
	"OUT_OF_RANGE": true, "PERMISSION_DENIED": true, "RESOURCE_EXHAUSTED": true,
	"UNAUTHENTICATED": true, "UNAVAILABLE": true, "UNKNOWN": true,
}

var knownErrorReasons = map[string]bool{
	"API_KEY_HTTP_REFERRER_BLOCKED": true, "API_KEY_INVALID": true, "API_KEY_IP_ADDRESS_BLOCKED": true,
	"API_KEY_SERVICE_BLOCKED": true, "BILLING_DISABLED": true, "BILLING_NOT_ACTIVE": true,
	"MODEL_NOT_FOUND": true, "QUOTA_EXCEEDED": true, "RATE_LIMIT_EXCEEDED": true,
}
