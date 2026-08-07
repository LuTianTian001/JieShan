package protocol

import (
	"context"
	"reflect"
	"testing"
)

type discoveryOnlyAdapter struct{}

func (discoveryOnlyAdapter) DiscoverModels(context.Context, DiscoveryInput) (DiscoveryResult, error) {
	return DiscoveryResult{Models: []string{}}, nil
}

type missingUsageAdapter struct{}

func (missingUsageAdapter) DiscoverModels(context.Context, DiscoveryInput) (DiscoveryResult, error) {
	return DiscoveryResult{Models: []string{}}, nil
}

func (missingUsageAdapter) EncodeRequest(context.Context, RequestBuildInput) (EncodedRequest, error) {
	return EncodedRequest{}, nil
}

func (missingUsageAdapter) DecodeResponse(context.Context, ResponseInput) (DecodedResponse, error) {
	return DecodedResponse{}, nil
}

func (missingUsageAdapter) DecodeStream(context.Context, StreamInput, func(StreamEvent) error) (StreamResult, error) {
	return StreamResult{}, nil
}

func (missingUsageAdapter) DecodeError(context.Context, ErrorInput) (DecodedError, error) {
	return DecodedError{}, nil
}

type completeAdapter struct {
	missingUsageAdapter
}

func (completeAdapter) ExtractUsage(context.Context, UsageInput) (Usage, error) {
	return Usage{}, nil
}

func TestKnownCatalogDoesNotInferCapabilities(t *testing.T) {
	contracts := Catalog()
	want := []struct {
		protocol Protocol
		surface  Surface
	}{
		{protocol: OpenAI, surface: OpenAIChatCompletions},
		{protocol: OpenAI, surface: OpenAIResponses},
		{protocol: Anthropic, surface: AnthropicMessages},
		{protocol: Gemini, surface: GeminiGenerateContent},
	}
	if len(contracts) != len(want) {
		t.Fatalf("catalog length = %d, want %d", len(contracts), len(want))
	}
	for index, contract := range contracts {
		if contract.Protocol != want[index].protocol || contract.Surface != want[index].surface {
			t.Fatalf("contract %d = %+v, want %q/%q", index, contract, want[index].protocol, want[index].surface)
		}
		if contract.Capabilities != (Capabilities{}) || contract.Routable() {
			t.Fatalf("unregistered known surface advertised capabilities: %+v", contract)
		}
	}
}

func TestRegistryDerivesPartialAndCompleteCapabilities(t *testing.T) {
	registry := NewRegistry()
	partial, err := registry.Register(OpenAI, OpenAIChatCompletions, missingUsageAdapter{})
	if err != nil {
		t.Fatal(err)
	}
	wantPartial := Capabilities{Discovery: true, Request: true, Response: true, Stream: true, Error: true}
	if partial.Capabilities != wantPartial || partial.Routable() {
		t.Fatalf("partial contract = %+v", partial)
	}
	if got := partial.Capabilities.MissingRouteStages(); !reflect.DeepEqual(got, []string{"usage"}) {
		t.Fatalf("partial missing stages = %#v", got)
	}

	complete, err := registry.Register(OpenAI, OpenAIResponses, completeAdapter{})
	if err != nil {
		t.Fatal(err)
	}
	wantComplete := Capabilities{Discovery: true, Request: true, Response: true, Stream: true, Usage: true, Error: true}
	if complete.Capabilities != wantComplete || !complete.Routable() {
		t.Fatalf("complete contract = %+v", complete)
	}
}

func TestRegistrationDoesNotGrantSiblingSurfaceCapabilities(t *testing.T) {
	registry := NewRegistry()
	if _, err := registry.Register(OpenAI, OpenAIChatCompletions, completeAdapter{}); err != nil {
		t.Fatal(err)
	}
	responses, err := registry.Lookup(OpenAI, OpenAIResponses)
	if err != nil {
		t.Fatal(err)
	}
	if responses.Capabilities != (Capabilities{}) || responses.Routable() {
		t.Fatalf("chat registration leaked into Responses: %+v", responses)
	}
}

func TestDiscoveryOnlyNativeAdaptersAreNotRoutable(t *testing.T) {
	registry := NewRegistry()
	for _, target := range []struct {
		protocol Protocol
		surface  Surface
	}{
		{protocol: Anthropic, surface: AnthropicMessages},
		{protocol: Gemini, surface: GeminiGenerateContent},
	} {
		contract, err := registry.Register(target.protocol, target.surface, discoveryOnlyAdapter{})
		if err != nil {
			t.Fatal(err)
		}
		if !contract.Capabilities.Discovery || contract.Routable() {
			t.Fatalf("discovery-only contract = %+v", contract)
		}
		wantMissing := []string{"request", "response", "stream", "usage", "error"}
		if got := contract.Capabilities.MissingRouteStages(); !reflect.DeepEqual(got, wantMissing) {
			t.Fatalf("missing stages = %#v, want %#v", got, wantMissing)
		}
	}
}

func TestRoutableRequiresEveryStage(t *testing.T) {
	complete := Capabilities{Discovery: true, Request: true, Response: true, Stream: true, Usage: true, Error: true}
	if !complete.Routable() {
		t.Fatal("complete adapter is not routable")
	}

	tests := []struct {
		name   string
		mutate func(*Capabilities)
		stage  string
	}{
		{name: "discovery", mutate: func(value *Capabilities) { value.Discovery = false }, stage: "discovery"},
		{name: "request", mutate: func(value *Capabilities) { value.Request = false }, stage: "request"},
		{name: "response", mutate: func(value *Capabilities) { value.Response = false }, stage: "response"},
		{name: "stream", mutate: func(value *Capabilities) { value.Stream = false }, stage: "stream"},
		{name: "usage", mutate: func(value *Capabilities) { value.Usage = false }, stage: "usage"},
		{name: "error", mutate: func(value *Capabilities) { value.Error = false }, stage: "error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			capabilities := complete
			test.mutate(&capabilities)
			if capabilities.Routable() {
				t.Fatalf("capabilities without %s are routable", test.stage)
			}
			if got := capabilities.MissingRouteStages(); !reflect.DeepEqual(got, []string{test.stage}) {
				t.Fatalf("missing stages = %#v, want %#v", got, []string{test.stage})
			}
		})
	}
}

func TestRegistryNeverFallsBackAcrossSurfaces(t *testing.T) {
	registry := NewRegistry()
	for _, target := range []struct {
		protocol Protocol
		surface  Surface
	}{
		{protocol: OpenAI, surface: AnthropicMessages},
		{protocol: Anthropic, surface: OpenAIChatCompletions},
		{protocol: Gemini, surface: OpenAIResponses},
		{protocol: Protocol("compatible"), surface: OpenAIChatCompletions},
	} {
		if contract, err := registry.Lookup(target.protocol, target.surface); err == nil {
			t.Fatalf("Lookup(%q, %q) = %+v, want error", target.protocol, target.surface, contract)
		}
	}
}

func TestRegistryRejectsNilAndEmptyAdapters(t *testing.T) {
	registry := NewRegistry()
	var typedNil *completeAdapter
	for _, adapter := range []any{nil, typedNil, struct{}{}} {
		if contract, err := registry.Register(OpenAI, OpenAIChatCompletions, adapter); err == nil {
			t.Fatalf("Register(%T) = %+v, want error", adapter, contract)
		}
	}
}

func TestParsersAcceptOnlyKnownCanonicalFamilies(t *testing.T) {
	if protocol, err := ParseProtocol(" OpenAI "); err != nil || protocol != OpenAI {
		t.Fatalf("ParseProtocol() = %q, %v", protocol, err)
	}
	if surface, err := ParseSurface("generateContent"); err != nil || surface != GeminiGenerateContent {
		t.Fatalf("ParseSurface() = %q, %v", surface, err)
	}
	if _, err := ParseProtocol("compatible"); err == nil {
		t.Fatal("ambiguous compatible protocol unexpectedly accepted")
	}
	if _, err := ParseSurface("completion-ish"); err == nil {
		t.Fatal("unknown surface unexpectedly accepted")
	}
}
