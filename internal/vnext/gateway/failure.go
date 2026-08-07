package gateway

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
)

var ErrCommittedStreamFailed = errors.New("upstream stream failed after downstream output began")

type retryError struct {
	failure routing.Failure
}

func (err *retryError) Error() string {
	return "retry the request according to the explicit route"
}

func failureFromDecoded(decoded protocol.DecodedError, header http.Header) routing.Failure {
	return FailureFromDecoded(decoded, header, time.Now().UTC())
}

// FailureFromDecoded is the shared protocol-error classification boundary used
// by both live gateway traffic and active health probes. Keeping this mapping in
// one place prevents monitoring from teaching the router a different failure
// vocabulary than real requests use.
func FailureFromDecoded(decoded protocol.DecodedError, header http.Header, now time.Time) routing.Failure {
	failure := routing.Failure{RetryAfter: retryAfter(header, now)}
	switch strings.TrimSpace(decoded.Class) {
	case string(routing.FailureClientInvalid):
		failure.Kind = routing.FailureClientInvalid
	case string(routing.FailureCredentialAuth):
		failure.Kind = routing.FailureCredentialAuth
	case string(routing.FailureCredentialPermission):
		failure.Kind = routing.FailureCredentialPermission
	case string(routing.FailureCredentialPaymentRequired):
		failure.Kind = routing.FailureCredentialPaymentRequired
	case string(routing.FailureCredentialRateLimited):
		failure.Kind = routing.FailureCredentialRateLimited
	case string(routing.FailureTargetMisconfigured):
		failure.Kind = routing.FailureTargetMisconfigured
	case string(routing.FailureTransport):
		failure.Kind = routing.FailureTransport
	case string(routing.FailureUpstreamTransient):
		failure.Kind = routing.FailureUpstreamTransient
	case string(routing.FailureModelUnsupported):
		failure.Kind = routing.FailureModelUnsupported
	default:
		failure.Kind = routing.FailureUnknown
	}
	return failure
}

func retryAfter(header http.Header, now time.Time) time.Duration {
	if header == nil {
		return 0
	}
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	when, err := http.ParseTime(value)
	if err != nil || !when.After(now) {
		return 0
	}
	return when.Sub(now)
}
