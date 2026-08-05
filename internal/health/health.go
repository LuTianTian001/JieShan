package health

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Class string

const (
	ClassNone                Class = "none"
	ClassClientInvalid       Class = "client_invalid"
	ClassTargetMisconfigured Class = "target_misconfigured"
	ClassAuthInvalid         Class = "auth_invalid"
	ClassPermissionDenied    Class = "permission_denied"
	ClassPaymentRequired     Class = "payment_required"
	ClassRateLimited         Class = "rate_limited"
	ClassTransport           Class = "transport"
	ClassUpstreamTransient   Class = "upstream_transient"
	ClassModelUnsupported    Class = "model_unsupported"
	ClassStreamInterrupted   Class = "stream_interrupted"
	ClassUnknown             Class = "unknown"
)

type Decision struct {
	Class                Class
	Failover             bool
	PenalizeTarget       bool
	InvalidateCredential bool
	UnsupportedModel     bool
	RetryAfter           time.Duration
}

func Classify(status int, body []byte, requestErr error, committed bool, headers http.Header) Decision {
	if committed {
		return Decision{Class: ClassStreamInterrupted, PenalizeTarget: true}
	}
	if requestErr != nil {
		var netErr net.Error
		if errors.As(requestErr, &netErr) || errors.Is(requestErr, context.DeadlineExceeded) {
			return Decision{Class: ClassTransport, Failover: true, PenalizeTarget: true}
		}
		return Decision{Class: ClassTransport, Failover: true, PenalizeTarget: true}
	}
	text := strings.ToLower(string(body))
	if status >= 200 && status < 300 {
		return Decision{Class: ClassNone}
	}
	if status == http.StatusUnauthorized {
		return Decision{Class: ClassAuthInvalid, Failover: true, InvalidateCredential: true}
	}
	if status == http.StatusForbidden {
		return Decision{Class: ClassPermissionDenied, Failover: true, PenalizeTarget: true}
	}
	if status == http.StatusPaymentRequired {
		return Decision{Class: ClassPaymentRequired, Failover: true, PenalizeTarget: true}
	}
	if isModelUnsupportedMessage(text) {
		return Decision{Class: ClassModelUnsupported, Failover: true, UnsupportedModel: true}
	}
	if status == http.StatusNotFound {
		return Decision{Class: ClassTargetMisconfigured, Failover: true, PenalizeTarget: true}
	}
	if status == http.StatusTooManyRequests {
		return Decision{Class: ClassRateLimited, Failover: true, PenalizeTarget: true, RetryAfter: ParseRetryAfter(headers, time.Now())}
	}
	if status == http.StatusRequestTimeout || status == http.StatusTooEarly || status >= 500 {
		return Decision{Class: ClassUpstreamTransient, Failover: true, PenalizeTarget: true}
	}
	if status >= 400 && status < 500 {
		return Decision{Class: ClassClientInvalid}
	}
	return Decision{Class: ClassUnknown}
}

func ClassifyInvalidSuccess(body []byte) Decision {
	if isModelUnsupportedMessage(strings.ToLower(string(body))) {
		return Decision{Class: ClassModelUnsupported, Failover: true, UnsupportedModel: true}
	}
	return Decision{Class: ClassUpstreamTransient, Failover: true, PenalizeTarget: true}
}

func isModelUnsupportedMessage(text string) bool {
	phrases := []string{
		"model not found", "model does not exist", "no such model", "unsupported model", "unknown model",
		"模型不存在", "模型未找到", "不支持的模型", "不支持此模型", "无此模型", "没有该模型",
	}
	for _, phrase := range phrases {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

func ParseRetryAfter(headers http.Header, now time.Time) time.Duration {
	if headers == nil {
		return 0
	}
	raw := strings.TrimSpace(headers.Get("Retry-After"))
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if value, err := http.ParseTime(raw); err == nil && value.After(now) {
		return value.Sub(now)
	}
	return 0
}

type State struct {
	Phase               string
	ConsecutiveFailures int
	LastFailureAt       int64
	CooldownUntil       int64
}

func RecordFailure(state State, nowMS int64, threshold int, window, cooldown time.Duration, immediate bool) State {
	if threshold < 1 {
		threshold = 1
	}
	withinWindow := state.LastFailureAt > 0 && nowMS >= state.LastFailureAt && nowMS-state.LastFailureAt <= window.Milliseconds()
	failures := 1
	if withinWindow {
		failures = state.ConsecutiveFailures + 1
	}
	if state.Phase == "half_open" || immediate || failures >= threshold {
		return State{Phase: "open", LastFailureAt: nowMS, CooldownUntil: nowMS + maxDuration(cooldown, time.Second).Milliseconds()}
	}
	return State{Phase: "closed", ConsecutiveFailures: failures, LastFailureAt: nowMS}
}

func RecordSuccess(_ State) State { return State{Phase: "closed"} }

func maxDuration(left, right time.Duration) time.Duration {
	if left > right {
		return left
	}
	return right
}
