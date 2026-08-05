// Package redact removes credentials from error text before it is returned or
// persisted. It intentionally preserves hosts, paths, status codes, and other
// diagnostic context.
package redact

import (
	"net/url"
	"regexp"
	"strings"
)

var (
	urlPattern       = regexp.MustCompile(`https?://[^\s"'<>]+`)
	bearerPattern    = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	jwtPattern       = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
	secretPattern    = regexp.MustCompile(`\b(?:sk|rk|pk|rt|key)[-_][A-Za-z0-9_-]{8,}\b`)
	googleKeyPattern = regexp.MustCompile(`\bAIza[A-Za-z0-9_-]{16,}\b`)
	headerPattern    = regexp.MustCompile(`(?i)\b(authorization|proxy-authorization|cookie|set-cookie|x-api-key|api[-_ ]?key)\b\s*[:=]\s*[^\s,;]+`)
)

func String(value string) string {
	if value == "" {
		return ""
	}
	value = urlPattern.ReplaceAllStringFunc(value, sanitizeURL)
	value = bearerPattern.ReplaceAllString(value, "Bearer [REDACTED]")
	value = jwtPattern.ReplaceAllString(value, "[REDACTED]")
	value = secretPattern.ReplaceAllString(value, "[REDACTED]")
	value = googleKeyPattern.ReplaceAllString(value, "[REDACTED]")
	value = headerPattern.ReplaceAllString(value, "$1=[REDACTED]")
	return value
}

func sanitizeURL(raw string) string {
	trimmed := strings.TrimRight(raw, ".,;:)]}")
	suffix := raw[len(trimmed):]
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return raw
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	return parsed.String() + suffix
}
