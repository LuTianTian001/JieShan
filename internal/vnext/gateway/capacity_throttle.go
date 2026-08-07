package gateway

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/LuTianTian001/JieShan/internal/vnext/capacity"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol"
)

type capacityRetryError struct{}

func (*capacityRetryError) Error() string { return "retry after an upstream concurrency throttle" }

func (service *Service) reportConcurrencyThrottle(
	status int,
	header http.Header,
	body []byte,
	decoded protocol.DecodedError,
	siteID int64,
	attempt *Attempt,
) bool {
	observedAt := service.now().UTC()
	retry := retryAfter(header, observedAt)
	if status != http.StatusTooManyRequests || !hasConcurrencyEvidence(header, body, decoded) {
		return false
	}
	if retry > 0 {
		if err := service.capacity.ReportThrottle(capacity.ThrottleSignal{
			SiteID: capacity.SiteID(siteID), ObservedAt: observedAt, RetryAfter: retry,
		}); err != nil {
			attempt.StateUpdateFailed = true
		}
	}
	attempt.FinishedAt = observedAt
	attempt.Outcome = "throttled"
	attempt.SwitchReason = "next_target"
	if strings.TrimSpace(attempt.ErrorCode) == "" {
		attempt.ErrorCode = "upstream_concurrency"
	}
	attempt.ErrorClass = "site_capacity"
	return true
}

func hasConcurrencyEvidence(header http.Header, body []byte, decoded protocol.DecodedError) bool {
	if explicitConcurrencyValue(decoded.Code) {
		return true
	}
	for _, name := range []string{
		"X-RateLimit-Scope", "X-Rate-Limit-Scope", "X-RateLimit-Reason", "X-Rate-Limit-Reason", "X-Upstream-Error-Code",
	} {
		for _, value := range header.Values(name) {
			if explicitConcurrencyValue(value) {
				return true
			}
		}
	}
	var payload any
	if len(body) == 0 || json.Unmarshal(body, &payload) != nil {
		return false
	}
	return hasConcurrencyJSONValue(payload, "")
}

func hasConcurrencyJSONValue(value any, key string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for childKey, child := range typed {
			if hasConcurrencyJSONValue(child, childKey) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if hasConcurrencyJSONValue(child, key) {
				return true
			}
		}
	case string:
		switch normalizedConcurrencyValue(key) {
		case "code", "type", "reason", "error_code", "category", "scope":
			return explicitConcurrencyValue(typed)
		}
	}
	return false
}

func explicitConcurrencyValue(value string) bool {
	switch normalizedConcurrencyValue(value) {
	case "concurrency", "concurrency_exceeded", "concurrency_limit", "concurrency_limit_exceeded",
		"concurrency_limit_reached", "concurrent_request_limit_exceeded", "too_many_concurrent_requests":
		return true
	default:
		return false
	}
}

func normalizedConcurrencyValue(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(value)
}
