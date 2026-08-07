package protocol

import (
	"errors"
	"fmt"
	"strings"
)

// AuthScheme is the concrete credential placement used for an outbound
// request. It is configuration, not something request builders infer from a
// protocol label.
type AuthScheme string

const (
	AuthBearer      AuthScheme = "bearer"
	AuthXAPIKey     AuthScheme = "x-api-key"
	AuthXGoogAPIKey AuthScheme = "x-goog-api-key"
	AuthQueryKey    AuthScheme = "query-key"
)

// AuthInput is required request-construction input. Secret is deliberately not
// included in errors or serializable output.
type AuthInput struct {
	Scheme AuthScheme `json:"scheme"`
	Secret string     `json:"-"`
}

// AuthMaterial contains the one header or query value a request builder must
// apply. Empty fields are intentional and allow callers to avoid maps that may
// accidentally merge or override authentication.
type AuthMaterial struct {
	HeaderName     string `json:"headerName,omitempty"`
	HeaderValue    string `json:"-"`
	QueryParameter string `json:"queryParameter,omitempty"`
	QueryValue     string `json:"-"`
}

func ParseAuthScheme(value string) (AuthScheme, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(AuthBearer):
		return AuthBearer, nil
	case string(AuthXAPIKey), "x_api_key":
		return AuthXAPIKey, nil
	case string(AuthXGoogAPIKey), "x_goog_api_key":
		return AuthXGoogAPIKey, nil
	case string(AuthQueryKey), "query_key":
		return AuthQueryKey, nil
	default:
		return "", fmt.Errorf("unsupported auth scheme %q", value)
	}
}

// DefaultAuthScheme is for endpoint creation UX only. ValidateBuildInput never
// calls it; every request must carry the configured scheme explicitly.
func DefaultAuthScheme(protocol Protocol) (AuthScheme, error) {
	switch protocol {
	case OpenAI:
		return AuthBearer, nil
	case Anthropic:
		return AuthXAPIKey, nil
	case Gemini:
		return AuthXGoogAPIKey, nil
	default:
		return "", fmt.Errorf("unsupported inference protocol %q", protocol)
	}
}

func (input AuthInput) Material() (AuthMaterial, error) {
	if input.Scheme == "" {
		return AuthMaterial{}, errors.New("auth scheme is required")
	}
	scheme, err := ParseAuthScheme(string(input.Scheme))
	if err != nil {
		return AuthMaterial{}, err
	}
	secret := strings.TrimSpace(input.Secret)
	if secret == "" {
		return AuthMaterial{}, errors.New("auth secret is required")
	}
	switch scheme {
	case AuthBearer:
		return AuthMaterial{HeaderName: "Authorization", HeaderValue: "Bearer " + secret}, nil
	case AuthXAPIKey:
		return AuthMaterial{HeaderName: "x-api-key", HeaderValue: secret}, nil
	case AuthXGoogAPIKey:
		return AuthMaterial{HeaderName: "x-goog-api-key", HeaderValue: secret}, nil
	case AuthQueryKey:
		return AuthMaterial{QueryParameter: "key", QueryValue: secret}, nil
	default:
		return AuthMaterial{}, fmt.Errorf("unsupported auth scheme %q", input.Scheme)
	}
}

// RequestBuildInput is the minimum contract shared by future protocol
// adapters. The exact surface and auth placement are both mandatory.
type RequestBuildInput struct {
	Protocol Protocol  `json:"protocol"`
	Surface  Surface   `json:"surface"`
	BaseURL  string    `json:"baseUrl"`
	Model    string    `json:"model"`
	Payload  []byte    `json:"-"`
	Auth     AuthInput `json:"auth"`
}

// ValidateBuildInput rejects discovery-only or partially implemented adapters
// before any outbound request can be constructed.
func (registry *Registry) ValidateBuildInput(input RequestBuildInput) (Contract, AuthMaterial, error) {
	if registry == nil {
		return Contract{}, AuthMaterial{}, errors.New("protocol registry is unavailable")
	}
	contract, err := registry.Lookup(input.Protocol, input.Surface)
	if err != nil {
		return Contract{}, AuthMaterial{}, err
	}
	if !contract.Routable() {
		return Contract{}, AuthMaterial{}, fmt.Errorf(
			"protocol surface %q/%q is discovery-only or incomplete; missing %s",
			input.Protocol,
			input.Surface,
			strings.Join(contract.Capabilities.MissingRouteStages(), ", "),
		)
	}
	if strings.TrimSpace(input.BaseURL) == "" {
		return Contract{}, AuthMaterial{}, errors.New("request base URL is required")
	}
	if strings.TrimSpace(input.Model) == "" {
		return Contract{}, AuthMaterial{}, errors.New("request model is required")
	}
	if len(input.Payload) == 0 {
		return Contract{}, AuthMaterial{}, errors.New("request payload is required")
	}
	material, err := input.Auth.Material()
	if err != nil {
		return Contract{}, AuthMaterial{}, err
	}
	return contract, material, nil
}
