package gateway

import (
	"errors"
	"net/http"
	"strings"
)

func mergeEndpointHeaders(destination, extra http.Header) error {
	for rawName, values := range extra {
		name := http.CanonicalHeaderKey(strings.TrimSpace(rawName))
		if name == "" || forbiddenEndpointHeader(name) {
			return errors.New("endpoint header is not allowed")
		}
		destination.Del(name)
		for _, value := range values {
			if strings.ContainsAny(value, "\r\n") {
				return errors.New("endpoint header value is invalid")
			}
			destination.Add(name, value)
		}
	}
	return nil
}

// MergeEndpointHeaders applies already-decrypted endpoint headers while
// enforcing the same hop-by-hop and authentication-header restrictions as the
// live gateway. Active probes must use this helper rather than maintaining a
// second, weaker header policy.
func MergeEndpointHeaders(destination, extra http.Header) error {
	return mergeEndpointHeaders(destination, extra)
}

func forbiddenEndpointHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "host", "content-length",
		"connection", "keep-alive", "proxy-connection", "transfer-encoding", "upgrade", "trailer",
		"x-api-key", "api-key", "x-goog-api-key":
		return true
	default:
		return false
	}
}

func safeResponseHeaders(source http.Header, stream bool) http.Header {
	result := make(http.Header)
	for _, name := range []string{"Content-Type", "Cache-Control", "X-Request-Id", "Openai-Request-Id", "Anthropic-Request-Id", "Request-Id"} {
		for _, value := range source.Values(name) {
			if !strings.ContainsAny(value, "\r\n") {
				result.Add(name, value)
			}
		}
	}
	if result.Get("Content-Type") == "" {
		if stream {
			result.Set("Content-Type", "text/event-stream")
		} else {
			result.Set("Content-Type", "application/json")
		}
	}
	return result
}
