package inventoryapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const maxRequestBytes = 64 << 10

var errNilRepository = errors.New("inventory repository is required")

func routeSegments(path string) ([]string, bool) {
	if !strings.HasPrefix(path, apiPrefix+"/") {
		return nil, false
	}
	rest := strings.TrimPrefix(path, apiPrefix+"/")
	if rest == "" || strings.HasSuffix(rest, "/") || strings.Contains(rest, "//") {
		return nil, false
	}
	return strings.Split(rest, "/"), true
}

func positiveID(w http.ResponseWriter, raw, kind string) (int64, bool) {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("%s ID must be a positive integer", kind))
		return 0, false
	}
	return value, true
}

func requiredIfMatch(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" {
		writeError(w, http.StatusPreconditionRequired, "precondition_required", "If-Match header is required")
		return 0, false
	}
	if strings.Contains(raw, ",") || raw == "*" || strings.HasPrefix(raw, "W/") {
		writeError(w, http.StatusBadRequest, "invalid_revision", "If-Match must contain one strong numeric revision")
		return 0, false
	}
	if strings.HasPrefix(raw, `"`) || strings.HasSuffix(raw, `"`) {
		if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
			writeError(w, http.StatusBadRequest, "invalid_revision", "If-Match must contain one strong numeric revision")
			return 0, false
		}
		raw = raw[1 : len(raw)-1]
	}
	revision, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || revision <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_revision", "If-Match must contain one strong numeric revision")
		return 0, false
	}
	return revision, true
}

func revisionETag(revision int64) string {
	if revision <= 0 {
		return ""
	}
	return `"` + strconv.FormatInt(revision, 10) + `"`
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must be a valid JSON object")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must contain exactly one JSON value")
		return false
	}
	return true
}

func methodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func writeRepositoryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, vnextstore.ErrRevisionConflict):
		writeError(w, http.StatusConflict, "revision_conflict", "resource changed; refresh and try again")
	case errors.Is(err, vnextstore.ErrConflict), isUniqueConstraint(err):
		writeError(w, http.StatusConflict, "conflict", "resource already exists or conflicts with current state")
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, ErrCredentialUnavailable):
		writeError(w, http.StatusConflict, "credential_unavailable", "credential must be enabled and explicitly bound to this endpoint")
	case errors.Is(err, ErrDiscoveryUnavailable):
		writeError(w, http.StatusConflict, "discovery_unavailable", "this endpoint does not have an executable model discovery adapter")
	case errors.Is(err, ErrDiscoveryFailed):
		writeError(w, http.StatusBadGateway, "discovery_failed", "upstream model discovery failed")
	case errors.Is(err, ErrDiscoveryAuthFailed):
		writeError(w, http.StatusUnauthorized, "upstream_authentication_failed", "the upstream rejected this API key")
	case errors.Is(err, ErrDiscoveryForbidden):
		writeError(w, http.StatusForbidden, "upstream_permission_denied", "this API key cannot list upstream models")
	case errors.Is(err, ErrDiscoveryPayment):
		writeError(w, http.StatusPaymentRequired, "upstream_balance_exhausted", "the upstream reports insufficient balance")
	case errors.Is(err, ErrDiscoveryRateLimited):
		writeError(w, http.StatusTooManyRequests, "upstream_rate_limited", "the upstream rate limited model discovery")
	case errors.Is(err, ErrDiscoveryTimedOut):
		writeError(w, http.StatusGatewayTimeout, "discovery_timeout", "upstream model discovery timed out")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "the request could not be completed")
	}
}

func isUniqueConstraint(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint") || strings.Contains(message, "constraint failed: unique")
}

func writeValidationError(w http.ResponseWriter, err error) {
	writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
