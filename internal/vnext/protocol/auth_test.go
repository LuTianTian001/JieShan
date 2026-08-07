package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAuthMaterialUsesExplicitScheme(t *testing.T) {
	tests := []struct {
		name string
		auth AuthInput
		want AuthMaterial
	}{
		{
			name: "bearer",
			auth: AuthInput{Scheme: AuthBearer, Secret: "secret-value"},
			want: AuthMaterial{HeaderName: "Authorization", HeaderValue: "Bearer secret-value"},
		},
		{
			name: "anthropic header",
			auth: AuthInput{Scheme: AuthXAPIKey, Secret: "secret-value"},
			want: AuthMaterial{HeaderName: "x-api-key", HeaderValue: "secret-value"},
		},
		{
			name: "gemini header",
			auth: AuthInput{Scheme: AuthXGoogAPIKey, Secret: "secret-value"},
			want: AuthMaterial{HeaderName: "x-goog-api-key", HeaderValue: "secret-value"},
		},
		{
			name: "gemini query",
			auth: AuthInput{Scheme: AuthQueryKey, Secret: "secret-value"},
			want: AuthMaterial{QueryParameter: "key", QueryValue: "secret-value"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.auth.Material()
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("material = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestRequestBuildInputDoesNotInferAuthScheme(t *testing.T) {
	registry := NewRegistry()
	if _, err := registry.Register(OpenAI, OpenAIChatCompletions, completeAdapter{}); err != nil {
		t.Fatal(err)
	}
	input := RequestBuildInput{
		Protocol: OpenAI,
		Surface:  OpenAIChatCompletions,
		BaseURL:  "https://api.example.test/v1",
		Model:    "provider-model",
		Payload:  []byte(`{"model":"public-model"}`),
		Auth:     AuthInput{Secret: "must-not-appear"},
	}
	_, _, err := registry.ValidateBuildInput(input)
	if err == nil || !strings.Contains(err.Error(), "auth scheme is required") {
		t.Fatalf("ValidateBuildInput() error = %v", err)
	}
	if strings.Contains(err.Error(), input.Auth.Secret) {
		t.Fatal("validation error leaked auth secret")
	}
}

func TestAuthSecretsAreExcludedFromJSON(t *testing.T) {
	input := RequestBuildInput{
		Protocol: OpenAI,
		Surface:  OpenAIChatCompletions,
		BaseURL:  "https://api.example.test/v1",
		Model:    "provider-model",
		Payload:  []byte(`{"private":"payload"}`),
		Auth:     AuthInput{Scheme: AuthBearer, Secret: "private-secret"},
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-secret") || strings.Contains(string(encoded), "private") {
		t.Fatalf("serialized request input leaked sensitive data: %s", encoded)
	}

	material, err := input.Auth.Material()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(material)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-secret") {
		t.Fatalf("serialized auth material leaked secret: %s", encoded)
	}
}

func TestRequestBuildInputReturnsConfiguredAuthMaterial(t *testing.T) {
	registry := NewRegistry()
	if _, err := registry.Register(OpenAI, OpenAIResponses, completeAdapter{}); err != nil {
		t.Fatal(err)
	}
	input := RequestBuildInput{
		Protocol: OpenAI,
		Surface:  OpenAIResponses,
		BaseURL:  "https://relay.example.test/v1",
		Model:    "provider-model",
		Payload:  []byte(`{"model":"public-model"}`),
		Auth:     AuthInput{Scheme: AuthXAPIKey, Secret: "relay-key"},
	}
	contract, material, err := registry.ValidateBuildInput(input)
	if err != nil {
		t.Fatal(err)
	}
	if contract.Protocol != OpenAI || contract.Surface != OpenAIResponses || !contract.Routable() {
		t.Fatalf("contract = %+v", contract)
	}
	if material.HeaderName != "x-api-key" || material.HeaderValue != "relay-key" {
		t.Fatalf("material = %+v", material)
	}
}

func TestRequestBuildInputRejectsDiscoveryOnlySurfaceBeforeAuth(t *testing.T) {
	registry := NewRegistry()
	if _, err := registry.Register(Anthropic, AnthropicMessages, discoveryOnlyAdapter{}); err != nil {
		t.Fatal(err)
	}
	input := RequestBuildInput{
		Protocol: Anthropic,
		Surface:  AnthropicMessages,
		BaseURL:  "https://api.anthropic.com",
		Model:    "claude-model",
		Payload:  []byte(`{"model":"claude-model"}`),
		Auth:     AuthInput{Scheme: AuthXAPIKey, Secret: "secret-value"},
	}
	_, _, err := registry.ValidateBuildInput(input)
	if err == nil || !strings.Contains(err.Error(), "discovery-only or incomplete") {
		t.Fatalf("ValidateBuildInput() error = %v", err)
	}
	for _, stage := range []string{"request", "response", "stream", "usage", "error"} {
		if !strings.Contains(err.Error(), stage) {
			t.Fatalf("error %q does not name missing %s stage", err, stage)
		}
	}
}

func TestDefaultAuthSchemeIsSeparateFromRequestValidation(t *testing.T) {
	tests := []struct {
		protocol Protocol
		want     AuthScheme
	}{
		{protocol: OpenAI, want: AuthBearer},
		{protocol: Anthropic, want: AuthXAPIKey},
		{protocol: Gemini, want: AuthXGoogAPIKey},
	}
	for _, test := range tests {
		got, err := DefaultAuthScheme(test.protocol)
		if err != nil || got != test.want {
			t.Fatalf("DefaultAuthScheme(%q) = %q, %v", test.protocol, got, err)
		}
	}
	if _, err := DefaultAuthScheme(Protocol("unknown")); err == nil {
		t.Fatal("unknown protocol unexpectedly received an auth default")
	}
}
