package probeexec

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/gateway"
	"github.com/LuTianTian001/JieShan/internal/vnext/monitoring"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol/anthropic"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol/gemini"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol/openai"
	"github.com/LuTianTian001/JieShan/internal/vnext/resolver"
	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
)

func TestExecutorTriesNextCredentialAndUsesSharedCredentialEffects(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		if request.URL.Path != "/v1/chat/completions" {
			t.Fatalf("request path = %q", request.URL.Path)
		}
		if request.Header.Get("X-Probe-Route") != "primary" {
			t.Fatalf("endpoint header = %q", request.Header.Get("X-Probe-Route"))
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["model"] != "upstream-model" || payload["stream"] != true {
			t.Fatalf("probe payload = %#v", payload)
		}
		if request.Header.Get("Authorization") != "Bearer valid-key" {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = writer.Write([]byte(`{"error":{"code":"invalid_api_key","message":"bad key"}}`))
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"model\":\"upstream-model\",\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"))
		_, _ = writer.Write([]byte("data: {\"model\":\"upstream-model\",\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\n"))
	}))
	defer server.Close()

	registry := newOpenAIRegistry(t, server.Client())
	effects := &recordingEffects{}
	executor, err := New(registry, server.Client(), staticSecrets{
		values:  map[routing.CredentialID]string{11: "invalid-key", 12: "valid-key"},
		headers: http.Header{"X-Probe-Route": {"primary"}},
	}, effects, Options{})
	if err != nil {
		t.Fatal(err)
	}

	result, err := executor.Probe(t.Context(), openAIProbeRequest(server.URL, 11, 12))
	if err != nil {
		t.Fatal(err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("invalid observation: %v", err)
	}
	if result.Outcome != monitoring.OutcomeSuccess || result.HTTPStatus != http.StatusOK || calls != 2 {
		t.Fatalf("result = %+v calls=%d", result, calls)
	}
	if result.FirstOutputLatency == nil || len(result.Attempts) != 2 {
		t.Fatalf("probe timing/attempts = %+v", result)
	}
	if result.Attempts[0].FailureKind != routing.FailureCredentialAuth ||
		result.Attempts[1].Outcome != monitoring.OutcomeSuccess {
		t.Fatalf("attempts = %+v", result.Attempts)
	}
	if len(effects.events) != 1 || effects.events[0].CredentialID != 11 ||
		effects.events[0].Effect != routing.CredentialEffectInvalidate {
		t.Fatalf("credential effects = %+v", effects.events)
	}
}

func TestExecutorStopsCredentialIterationOnTargetFailure(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls++
		writer.Header().Set("Retry-After", "30")
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"error":{"code":"service_unavailable"}}`))
	}))
	defer server.Close()

	registry := newOpenAIRegistry(t, server.Client())
	effects := &recordingEffects{}
	executor, err := New(registry, server.Client(), staticSecrets{
		values: map[routing.CredentialID]string{11: "one", 12: "two"},
	}, effects, Options{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Probe(t.Context(), openAIProbeRequest(server.URL, 11, 12))
	if err != nil {
		t.Fatal(err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("invalid observation: %v", err)
	}
	if result.Outcome != monitoring.OutcomeFailure || result.Failure.Kind != routing.FailureUpstreamTransient {
		t.Fatalf("result = %+v", result)
	}
	if result.Failure.RetryAfter != 30*time.Second || calls != 1 || len(result.Attempts) != 1 {
		t.Fatalf("retry/attempt behavior = %+v calls=%d", result, calls)
	}
	if len(effects.events) != 0 {
		t.Fatalf("unexpected credential effects = %+v", effects.events)
	}
}

func TestProbePayloadsAreBoundedNativeJSON(t *testing.T) {
	for _, surface := range []protocol.Surface{
		protocol.OpenAIChatCompletions,
		protocol.OpenAIResponses,
		protocol.AnthropicMessages,
		protocol.GeminiGenerateContent,
	} {
		t.Run(string(surface), func(t *testing.T) {
			payload, err := probePayload(surface)
			if err != nil {
				t.Fatal(err)
			}
			var object map[string]any
			if err := json.Unmarshal(payload, &object); err != nil || len(object) == 0 {
				t.Fatalf("payload = %s err=%v", payload, err)
			}
			if _, leaksRouteModel := object["model"]; leaksRouteModel {
				t.Fatalf("probe payload must let the adapter inject the source model: %s", payload)
			}
			if !strings.Contains(string(payload), "Reply exactly OK.") {
				t.Fatalf("probe payload lost the bounded OK instruction: %s", payload)
			}
			if object["stream"] != true {
				t.Fatalf("probe payload must request semantic streaming: %s", payload)
			}
			switch surface {
			case protocol.OpenAIChatCompletions:
				if object["max_tokens"] != float64(4) {
					t.Fatalf("chat probe output is not capped at 4 tokens: %s", payload)
				}
			case protocol.OpenAIResponses:
				if object["max_output_tokens"] != float64(16) {
					t.Fatalf("responses probe output cap = %v", object["max_output_tokens"])
				}
			case protocol.AnthropicMessages:
				if object["max_tokens"] != float64(16) {
					t.Fatalf("anthropic probe output cap = %v", object["max_tokens"])
				}
			case protocol.GeminiGenerateContent:
				config, ok := object["generationConfig"].(map[string]any)
				if !ok || config["maxOutputTokens"] != float64(16) {
					t.Fatalf("gemini probe output cap = %v", object["generationConfig"])
				}
			}
		})
	}
}

func TestExecutorMeasuresFirstSemanticOutputAcrossSurfaces(t *testing.T) {
	tests := []struct {
		name          string
		wire          protocol.Protocol
		surface       protocol.Surface
		auth          protocol.AuthScheme
		path          string
		query         string
		encodedStream bool
		streamBody    string
		adapter       func(*http.Client) (any, error)
	}{
		{
			name: "openai_chat", wire: protocol.OpenAI, surface: protocol.OpenAIChatCompletions,
			auth: protocol.AuthBearer, path: "/v1/chat/completions", encodedStream: true,
			streamBody: "data: {\"model\":\"upstream-model\",\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n" +
				"data: {\"model\":\"upstream-model\",\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\n",
			adapter: func(client *http.Client) (any, error) { return openai.NewChatCompletionsAdapter(client) },
		},
		{
			name: "openai_responses", wire: protocol.OpenAI, surface: protocol.OpenAIResponses,
			auth: protocol.AuthBearer, path: "/v1/responses", encodedStream: true,
			streamBody: "data: {\"type\":\"response.created\",\"response\":{\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"upstream-model\"}}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n",
			adapter: func(client *http.Client) (any, error) { return openai.NewResponsesAdapter(client) },
		},
		{
			name: "anthropic_messages", wire: protocol.Anthropic, surface: protocol.AnthropicMessages,
			auth: protocol.AuthXAPIKey, path: "/v1/messages", encodedStream: true,
			streamBody: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_probe\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"upstream-model\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"OK\"}}\n\n",
			adapter: func(client *http.Client) (any, error) { return anthropic.NewMessagesAdapter(client) },
		},
		{
			name: "gemini_generate_content", wire: protocol.Gemini, surface: protocol.GeminiGenerateContent,
			auth: protocol.AuthXGoogAPIKey, path: "/v1beta/models/upstream-model:streamGenerateContent", query: "alt=sse",
			streamBody: "data: {\"modelVersion\":\"upstream-model\"}\n\n" +
				"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"OK\"}]}}],\"modelVersion\":\"upstream-model\"}\n\n",
			adapter: func(client *http.Client) (any, error) { return gemini.NewGenerateContentAdapter(client) },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != test.path || request.URL.RawQuery != test.query {
					t.Fatalf("request target = %s?%s", request.URL.Path, request.URL.RawQuery)
				}
				if request.Header.Get("Accept") != "text/event-stream" {
					t.Fatalf("accept = %q", request.Header.Get("Accept"))
				}
				var payload map[string]any
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if test.encodedStream && payload["stream"] != true {
					t.Fatalf("encoded payload = %#v", payload)
				}
				writer.Header().Set("Content-Type", "text/event-stream")
				_, _ = writer.Write([]byte(test.streamBody))
			}))
			defer server.Close()

			adapter, err := test.adapter(server.Client())
			if err != nil {
				t.Fatal(err)
			}
			registry := protocol.NewRegistry()
			if _, err := registry.Register(test.wire, test.surface, adapter); err != nil {
				t.Fatal(err)
			}
			executor, err := New(registry, server.Client(), staticSecrets{
				values: map[routing.CredentialID]string{11: "valid-key"},
			}, &recordingEffects{}, Options{})
			if err != nil {
				t.Fatal(err)
			}
			request := openAIProbeRequest(server.URL, 11)
			request.Target.WireProtocol = string(test.wire)
			request.Target.Surface = string(test.surface)
			request.Target.AuthScheme = string(test.auth)
			result, err := executor.Probe(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if result.Outcome != monitoring.OutcomeSuccess || result.FirstOutputLatency == nil {
				t.Fatalf("probe result = %+v", result)
			}
		})
	}
}

func newOpenAIRegistry(t *testing.T, client *http.Client) *protocol.Registry {
	t.Helper()
	adapter, err := openai.NewChatCompletionsAdapter(client)
	if err != nil {
		t.Fatal(err)
	}
	registry := protocol.NewRegistry()
	if _, err := registry.Register(protocol.OpenAI, protocol.OpenAIChatCompletions, adapter); err != nil {
		t.Fatal(err)
	}
	return registry
}

func openAIProbeRequest(baseURL string, credentialIDs ...int64) monitoring.ProbeRequest {
	return monitoring.ProbeRequest{
		RunID: "probe-run", PublishedModelID: 31, PublicModel: "published-model", PublishedModelRevision: 2,
		Target: monitoring.ProbeTarget{
			PublishedModelTargetID: 41, PublishedModelTargetRevision: 3,
			ProviderModelTargetID: 51, ProviderModelTargetRevision: 4,
			Position: 1, SiteID: 61, SiteName: "Relay", EndpointID: 71, EndpointName: "OpenAI",
			BaseURL: baseURL, WireProtocol: string(protocol.OpenAI), Surface: string(protocol.OpenAIChatCompletions),
			AdapterKind: "native", AuthScheme: string(protocol.AuthBearer), SourceModel: "upstream-model",
			CredentialIDs: credentialIDs,
		},
	}
}

type staticSecrets struct {
	values  map[routing.CredentialID]string
	headers http.Header
}

func (provider staticSecrets) Materialize(
	_ context.Context,
	_ resolver.EndpointMetadata,
	credentialID routing.CredentialID,
) (gateway.SecretMaterial, error) {
	value, exists := provider.values[credentialID]
	if !exists {
		return gateway.SecretMaterial{}, context.Canceled
	}
	return gateway.SecretMaterial{Credential: value, Headers: provider.headers.Clone()}, nil
}

type recordingEffects struct {
	events []gateway.CredentialEffectEvent
}

func (effects *recordingEffects) ApplyCredentialEffect(_ context.Context, event gateway.CredentialEffectEvent) error {
	effects.events = append(effects.events, event)
	return nil
}
