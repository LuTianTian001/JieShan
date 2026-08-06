package inferenceprotocol

import "testing"

func TestCapabilitiesMatchImplementedSurfaces(t *testing.T) {
	for _, protocol := range []string{"openai", "compatible", "openai_chat_completions", "openai_responses"} {
		capabilities := For(protocol)
		if !capabilities.ModelDiscovery || !capabilities.ChatCompletions || !capabilities.Responses || !capabilities.RouteEligible {
			t.Fatalf("For(%q) = %+v", protocol, capabilities)
		}
	}
	for _, protocol := range []string{"anthropic", "gemini"} {
		capabilities := For(protocol)
		if !capabilities.ModelDiscovery || capabilities.ChatCompletions || capabilities.Responses || capabilities.RouteEligible {
			t.Fatalf("For(%q) = %+v", protocol, capabilities)
		}
	}
	if capabilities := For("unknown"); capabilities != (Capabilities{}) {
		t.Fatalf("unknown capabilities = %+v", capabilities)
	}
}

func TestNormalizeAndDefaultAuthScheme(t *testing.T) {
	if protocol, err := Normalize("OPENAI_CHAT_COMPLETIONS"); err != nil || protocol != "compatible" {
		t.Fatalf("Normalize() = %q, %v", protocol, err)
	}
	if _, err := Normalize("native-magic"); err == nil {
		t.Fatal("unknown protocol unexpectedly normalized")
	}
	if got := DefaultAuthScheme("anthropic"); got != "x-api-key" {
		t.Fatalf("Anthropic auth = %q", got)
	}
	if got := DefaultAuthScheme("gemini"); got != "query-key" {
		t.Fatalf("Gemini auth = %q", got)
	}
	if got := DefaultAuthScheme("compatible"); got != "bearer" {
		t.Fatalf("compatible auth = %q", got)
	}
}
