package redact

import (
	"strings"
	"testing"
)

func TestStringRemovesCredentialsAndURLQueries(t *testing.T) {
	input := `Get "https://user:pass@generativelanguage.googleapis.com/v1beta/models?key=AIza1234567890abcdefghijkl&alt=json": Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJ1c2VyIjoxMjM0NTY3ODkwfQ.signature123456 Cookie=session-secret x-api-key=sk-secretabcdefgh`
	got := String(input)
	for _, secret := range []string{"user:pass", "AIza123", "eyJhbG", "session-secret", "sk-secret", "?key="} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted text still contains %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "https://generativelanguage.googleapis.com/v1beta/models") {
		t.Fatalf("diagnostic URL path was lost: %s", got)
	}
}

func TestStringLeavesOrdinaryErrorsReadable(t *testing.T) {
	input := "upstream returned 503: temporarily unavailable"
	if got := String(input); got != input {
		t.Fatalf("ordinary error changed: %q", got)
	}
}
