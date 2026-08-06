package inferenceprotocol

import (
	"fmt"
	"strings"
)

// Capabilities describes the upstream surfaces JieShan can construct for a
// protocol. A compatible relay may still omit an optional OpenAI surface at
// runtime, but native Anthropic and Gemini endpoints are never advertised as
// OpenAI gateway targets until translation support exists.
type Capabilities struct {
	ModelDiscovery  bool `json:"modelDiscovery"`
	ChatCompletions bool `json:"chatCompletions"`
	Responses       bool `json:"responses"`
	RouteEligible   bool `json:"routeEligible"`
}

// Normalize returns the canonical protocol stored by the V3 domain model.
func Normalize(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "openai":
		return "openai", nil
	case "compatible", "openai-compatible", "openai_compatible", "openai_chat_completions", "openai_responses":
		return "compatible", nil
	case "anthropic":
		return "anthropic", nil
	case "gemini":
		return "gemini", nil
	default:
		return "", fmt.Errorf("unsupported inference protocol %q", value)
	}
}

func For(value string) Capabilities {
	protocol, err := Normalize(value)
	if err != nil {
		return Capabilities{}
	}
	switch protocol {
	case "openai", "compatible":
		return Capabilities{ModelDiscovery: true, ChatCompletions: true, Responses: true, RouteEligible: true}
	case "anthropic", "gemini":
		return Capabilities{ModelDiscovery: true}
	default:
		return Capabilities{}
	}
}

func DefaultAuthScheme(value string) string {
	protocol, err := Normalize(value)
	if err != nil {
		return "bearer"
	}
	switch protocol {
	case "anthropic":
		return "x-api-key"
	case "gemini":
		return "query-key"
	default:
		return "bearer"
	}
}
