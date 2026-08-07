package probeexec

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/gateway"
	"github.com/LuTianTian001/JieShan/internal/vnext/monitoring"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol"
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
		if payload["model"] != "upstream-model" || payload["stream"] != false {
			t.Fatalf("probe payload = %#v", payload)
		}
		if request.Header.Get("Authorization") != "Bearer valid-key" {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = writer.Write([]byte(`{"error":{"code":"invalid_api_key","message":"bad key"}}`))
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"id":"chatcmpl_probe","object":"chat.completion","model":"upstream-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}
		}`))
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
