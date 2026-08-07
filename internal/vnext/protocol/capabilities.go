package protocol

import (
	"fmt"
	"strings"
)

// Protocol identifies the upstream wire protocol. Compatibility quirks belong
// to endpoint profiles; they must not silently grant another protocol's
// capabilities.
type Protocol string

const (
	OpenAI    Protocol = "openai"
	Anthropic Protocol = "anthropic"
	Gemini    Protocol = "gemini"
)

// Surface identifies one concrete inference API. A route always targets one
// surface instead of inheriting every surface associated with a vendor name.
type Surface string

const (
	OpenAIChatCompletions Surface = "openai.chat_completions"
	OpenAIResponses       Surface = "openai.responses"
	AnthropicMessages     Surface = "anthropic.messages"
	GeminiGenerateContent Surface = "gemini.generate_content"
)

// Capabilities records independently implemented stages of an inference
// adapter. Discovery-only support is useful for inventory, but is not enough
// to serve downstream traffic.
type Capabilities struct {
	Discovery bool `json:"discovery"`
	Request   bool `json:"request"`
	Response  bool `json:"response"`
	Stream    bool `json:"stream"`
	Usage     bool `json:"usage"`
	Error     bool `json:"error"`
}

// Routable is deliberately derived. Callers cannot advertise a route while a
// required adapter stage is missing.
func (c Capabilities) Routable() bool {
	return c.Discovery && c.Request && c.Response && c.Stream && c.Usage && c.Error
}

// MissingRouteStages returns stable machine-readable stage names.
func (c Capabilities) MissingRouteStages() []string {
	missing := make([]string, 0, 6)
	if !c.Discovery {
		missing = append(missing, "discovery")
	}
	if !c.Request {
		missing = append(missing, "request")
	}
	if !c.Response {
		missing = append(missing, "response")
	}
	if !c.Stream {
		missing = append(missing, "stream")
	}
	if !c.Usage {
		missing = append(missing, "usage")
	}
	if !c.Error {
		missing = append(missing, "error")
	}
	return missing
}

// Contract is the implementation contract for exactly one protocol surface.
// Capabilities are populated by a Registry from concrete adapter components.
type Contract struct {
	Protocol     Protocol     `json:"protocol"`
	Surface      Surface      `json:"surface"`
	Capabilities Capabilities `json:"capabilities"`
}

// CapabilityLookup is the narrow capability boundary used by routing
// resolution. It permits tests and future adapter registries to provide
// executable capability truth without coupling the resolver to Registry.
type CapabilityLookup interface {
	Lookup(Protocol, Surface) (Contract, error)
}

func (c Contract) Routable() bool {
	return c.Capabilities.Routable()
}

// Catalog returns every known VNext surface with no inferred implementation
// capabilities. A protocol or vendor name alone never makes a surface usable.
func Catalog() []Contract {
	return []Contract{
		{Protocol: OpenAI, Surface: OpenAIChatCompletions},
		{Protocol: OpenAI, Surface: OpenAIResponses},
		{Protocol: Anthropic, Surface: AnthropicMessages},
		{Protocol: Gemini, Surface: GeminiGenerateContent},
	}
}

func lookupKnown(protocol Protocol, surface Surface) (Contract, error) {
	for _, contract := range Catalog() {
		if contract.Protocol == protocol && contract.Surface == surface {
			return contract, nil
		}
	}
	return Contract{}, fmt.Errorf("unsupported protocol surface %q/%q", protocol, surface)
}

// ValidatePair rejects a known protocol combined with the wrong surface. A
// protocol label alone is never enough to infer a route surface.
func ValidatePair(protocol Protocol, surface Surface) error {
	_, err := lookupKnown(protocol, surface)
	return err
}

func ParseProtocol(value string) (Protocol, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(OpenAI):
		return OpenAI, nil
	case string(Anthropic):
		return Anthropic, nil
	case string(Gemini):
		return Gemini, nil
	default:
		return "", fmt.Errorf("unsupported inference protocol %q", value)
	}
}

func ParseSurface(value string) (Surface, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case string(OpenAIChatCompletions), "chat_completions", "openai_chat_completions":
		return OpenAIChatCompletions, nil
	case string(OpenAIResponses), "responses", "openai_responses":
		return OpenAIResponses, nil
	case string(AnthropicMessages), "messages", "anthropic_messages":
		return AnthropicMessages, nil
	case string(GeminiGenerateContent), "generatecontent", "generate_content", "gemini_generate_content":
		return GeminiGenerateContent, nil
	default:
		return "", fmt.Errorf("unsupported inference surface %q", value)
	}
}
