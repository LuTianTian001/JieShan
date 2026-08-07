package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
)

type doerFunc func(*http.Request) (*http.Response, error)

func (function doerFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestAdapterRegistersAsCompleteOpenAIChatCompletions(t *testing.T) {
	adapter, err := NewChatCompletionsAdapter(doerFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("unused")
	}))
	if err != nil {
		t.Fatal(err)
	}
	registry := vnextprotocol.NewRegistry()
	contract, err := registry.Register(vnextprotocol.OpenAI, vnextprotocol.OpenAIChatCompletions, adapter)
	if err != nil {
		t.Fatal(err)
	}
	if !contract.Routable() || contract.Capabilities != (vnextprotocol.Capabilities{
		Discovery: true, Request: true, Response: true, Stream: true, Usage: true, Error: true,
	}) {
		t.Fatalf("unexpected registered contract: %+v", contract)
	}
}

func TestDiscoverModelsUsesGETAndExplicitAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/relay/v1/models" {
			t.Errorf("unexpected discovery request %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer discovery-secret" {
			t.Errorf("Authorization = %q", got)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"object":"list","data":[{"id":"model-z"},{"id":"model-a"},{"id":"model-a"}]}`))
	}))
	defer server.Close()
	adapter, err := NewChatCompletionsAdapter(server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.DiscoverModels(t.Context(), vnextprotocol.DiscoveryInput{
		BaseURL: server.URL + "/relay",
		Auth:    vnextprotocol.AuthInput{Scheme: vnextprotocol.AuthBearer, Secret: "discovery-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Models, []string{"model-a", "model-z"}) {
		t.Fatalf("models = %#v", result.Models)
	}
}

func TestEncodeRequestStructurallyRewritesModelAndAppliesConfiguredAuth(t *testing.T) {
	adapter, err := NewChatCompletionsAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := adapter.EncodeRequest(t.Context(), vnextprotocol.RequestBuildInput{
		Protocol: vnextprotocol.OpenAI,
		Surface:  vnextprotocol.OpenAIChatCompletions,
		BaseURL:  "https://relay.example.test/root/v1",
		Model:    "source-model",
		Payload:  []byte(`{"model":"public-model","messages":[{"role":"user","content":"public-model"}],"stream":true,"stream_options":{"future_flag":true}}`),
		Auth:     vnextprotocol.AuthInput{Scheme: vnextprotocol.AuthXAPIKey, Secret: "configured-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if encoded.Method != http.MethodPost || encoded.URL != "https://relay.example.test/root/v1/chat/completions" {
		t.Fatalf("unexpected encoded destination: %s %s", encoded.Method, encoded.URL)
	}
	if encoded.Header.Get("x-api-key") != "configured-secret" || encoded.Header.Get("Authorization") != "" {
		t.Fatalf("adapter inferred the wrong auth material: %#v", encoded.Header)
	}
	var payload struct {
		Model    string `json:"model"`
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
		StreamOptions struct {
			IncludeUsage bool `json:"include_usage"`
			FutureFlag   bool `json:"future_flag"`
		} `json:"stream_options"`
	}
	if err := json.Unmarshal(encoded.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Model != "source-model" || len(payload.Messages) != 1 || payload.Messages[0].Content != "public-model" {
		t.Fatalf("payload was not structurally rewritten: %s", encoded.Body)
	}
	if !payload.StreamOptions.IncludeUsage || !payload.StreamOptions.FutureFlag {
		t.Fatalf("stream options were not preserved and completed: %s", encoded.Body)
	}
}

func TestEndpointURLCanSwitchFromAConfiguredFullSurface(t *testing.T) {
	models, err := endpointURL("https://relay.example.test/prefix/v1/chat/completions", "/models")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := endpointURL("https://relay.example.test/prefix/v1/models", "/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	if models.String() != "https://relay.example.test/prefix/v1/models" || chat.String() != "https://relay.example.test/prefix/v1/chat/completions" {
		t.Fatalf("surface switch failed: models=%s chat=%s", models, chat)
	}
}

func TestEncodeRequestRequiresExplicitAuthWithoutLeakingIt(t *testing.T) {
	adapter, _ := NewChatCompletionsAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	secret := "sk-must-not-leak"
	_, err := adapter.EncodeRequest(t.Context(), vnextprotocol.RequestBuildInput{
		Protocol: vnextprotocol.OpenAI,
		Surface:  vnextprotocol.OpenAIChatCompletions,
		BaseURL:  "https://api.example.test",
		Model:    "source-model",
		Payload:  []byte(`{"messages":[]}`),
		Auth:     vnextprotocol.AuthInput{Secret: secret},
	})
	if err == nil || !strings.Contains(err.Error(), "auth scheme is required") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("request validation leaked the credential")
	}
}

func TestDecodeResponseAndExtractCanonicalUsage(t *testing.T) {
	adapter, _ := NewChatCompletionsAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	body := []byte(`{
		"id":"chatcmpl-1","model":"source-model",
		"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":100,"completion_tokens":30,"total_tokens":130,
			"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":10},
			"completion_tokens_details":{"reasoning_tokens":5}}
	}`)
	decoded, err := adapter.DecodeResponse(t.Context(), vnextprotocol.ResponseInput{StatusCode: http.StatusOK, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Model != "source-model" || !bytes.Equal(decoded.Body, body) {
		t.Fatalf("unexpected decoded response: %+v", decoded)
	}
	usage, err := adapter.ExtractUsage(t.Context(), vnextprotocol.UsageInput{Body: body})
	if err != nil {
		t.Fatal(err)
	}
	assertUsage(t, usage, 70, 25, 20, 10, 5)
}

func TestDecodeResponseRequiresChoicesAndSemanticOutput(t *testing.T) {
	adapter, _ := NewChatCompletionsAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "choices", body: `{"model":"m","usage":{"prompt_tokens":1,"completion_tokens":1}}`, want: "choices"},
		{name: "semantic", body: `{"model":"m","choices":[{"message":{"role":"assistant","content":""}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`, want: "semantic"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := adapter.DecodeResponse(t.Context(), vnextprotocol.ResponseInput{StatusCode: http.StatusOK, Body: []byte(test.body)})
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("DecodeResponse() error = %v", err)
			}
		})
	}
}

func TestDecodeResponseAllowsMissingUsageButExtractionFails(t *testing.T) {
	adapter, _ := NewChatCompletionsAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	body := []byte(`{"model":"m","choices":[{"message":{"content":"ok"}}]}`)
	if _, err := adapter.DecodeResponse(t.Context(), vnextprotocol.ResponseInput{StatusCode: http.StatusOK, Body: body}); err != nil {
		t.Fatalf("DecodeResponse() error = %v", err)
	}
	if usage, err := adapter.ExtractUsage(t.Context(), vnextprotocol.UsageInput{Body: body}); err == nil {
		t.Fatalf("ExtractUsage() usage = %+v, want missing usage error", usage)
	}
}

func TestDecodeStreamParsesEventsSemanticDeltaUsageAndDone(t *testing.T) {
	adapter, _ := NewChatCompletionsAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	stream := strings.Join([]string{
		": keepalive\n\n",
		"data: {\"model\":\"source-model\",\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n",
		"data: {\"model\":\"source-model\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n",
		"data: {\"model\":\"source-model\",\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":7,\"total_tokens\":18,\"prompt_tokens_details\":{\"cached_tokens\":3},\"completion_tokens_details\":{\"reasoning_tokens\":2}}}\n\n",
		"data: [DONE]\n\n",
	}, "")
	events := make([]vnextprotocol.StreamEvent, 0)
	result, err := adapter.DecodeStream(t.Context(), vnextprotocol.StreamInput{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       strings.NewReader(stream),
	}, func(event vnextprotocol.StreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Terminal || result.Model != "source-model" {
		t.Fatalf("unexpected stream result: %+v", result)
	}
	if len(events) != 5 || events[1].Semantic || !events[2].Semantic || events[3].Semantic || !events[4].Terminal {
		t.Fatalf("unexpected stream events: %+v", events)
	}
	usage, err := adapter.ExtractUsage(t.Context(), vnextprotocol.UsageInput{Events: events})
	if err != nil {
		t.Fatal(err)
	}
	assertUsage(t, usage, 8, 5, 3, 0, 2)
}

func TestDecodeStreamRequiresDoneEvenAfterFinishReason(t *testing.T) {
	adapter, _ := NewChatCompletionsAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	stream := "data: {\"model\":\"source-model\",\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}]}\n\n"
	result, err := adapter.DecodeStream(t.Context(), vnextprotocol.StreamInput{StatusCode: http.StatusOK, Body: strings.NewReader(stream)}, nil)
	if !errors.Is(err, ErrStreamTruncated) {
		t.Fatalf("DecodeStream() error = %v", err)
	}
	if result.Terminal {
		t.Fatalf("finish_reason was incorrectly treated as terminal: %+v", result)
	}
}

func TestDecodeStreamRejectsOversizedEvent(t *testing.T) {
	adapter, _ := NewChatCompletionsAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	stream := "data: " + strings.Repeat("x", maxEventBytes) + "\n\n"
	_, err := adapter.DecodeStream(t.Context(), vnextprotocol.StreamInput{StatusCode: http.StatusOK, Body: strings.NewReader(stream)}, nil)
	if !errors.Is(err, errEventTooLarge) {
		t.Fatalf("DecodeStream() error = %v", err)
	}
}

func TestDecodeErrorClassifiesFailuresAndNeverReturnsUpstreamSecrets(t *testing.T) {
	adapter, _ := NewChatCompletionsAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	secret := "sk-live-secret-must-not-appear"
	tests := []struct {
		status            int
		class             string
		retryable         bool
		credentialFailure bool
	}{
		{status: http.StatusUnauthorized, class: "credential_auth", credentialFailure: true},
		{status: http.StatusForbidden, class: "credential_permission", credentialFailure: true},
		{status: http.StatusTooManyRequests, class: "credential_rate_limited", retryable: true, credentialFailure: true},
		{status: http.StatusServiceUnavailable, class: "upstream_transient", retryable: true},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			decoded, err := adapter.DecodeError(t.Context(), vnextprotocol.ErrorInput{
				StatusCode: test.status,
				Body:       []byte(`{"error":{"code":"invalid_api_key","message":"` + secret + `"}}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Class != test.class || decoded.Retryable != test.retryable || decoded.CredentialFailure != test.credentialFailure {
				t.Fatalf("decoded error = %+v", decoded)
			}
			encoded, err := json.Marshal(decoded)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), secret) || strings.Contains(decoded.Message, secret) {
				t.Fatalf("decoded error leaked upstream secret: %s", encoded)
			}
		})
	}
}

func TestDiscoveryErrorDoesNotLeakCredentialOrEchoedBody(t *testing.T) {
	secret := "sk-discovery-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":{"message":"bad key ` + secret + `","code":"invalid_api_key"}}`))
	}))
	defer server.Close()
	adapter, _ := NewChatCompletionsAdapter(server.Client())
	_, err := adapter.DiscoverModels(context.Background(), vnextprotocol.DiscoveryInput{
		BaseURL: server.URL,
		Auth:    vnextprotocol.AuthInput{Scheme: vnextprotocol.AuthBearer, Secret: secret},
	})
	if err == nil {
		t.Fatal("expected discovery failure")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("discovery error leaked secret: %v", err)
	}
}

func TestResponseAndErrorBodiesAreBounded(t *testing.T) {
	adapter, _ := NewChatCompletionsAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	oversized := bytes.Repeat([]byte("x"), maxBodyBytes+1)
	if _, err := adapter.DecodeResponse(t.Context(), vnextprotocol.ResponseInput{StatusCode: http.StatusOK, Body: oversized}); !errors.Is(err, errBodyTooLarge) {
		t.Fatalf("DecodeResponse() error = %v", err)
	}
	decoded, err := adapter.DecodeError(t.Context(), vnextprotocol.ErrorInput{StatusCode: http.StatusUnauthorized, Body: oversized})
	if err != nil || decoded.Class != "credential_auth" || decoded.Code != "http_401" {
		t.Fatalf("oversized error body was not safely ignored: %+v, %v", decoded, err)
	}
}

func TestExtractUsageRejectsInvalidTokenSubsets(t *testing.T) {
	adapter, _ := NewChatCompletionsAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	_, err := adapter.ExtractUsage(t.Context(), vnextprotocol.UsageInput{Body: []byte(`{
		"usage":{"prompt_tokens":5,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":6}}
	}`)})
	if err == nil || !strings.Contains(err.Error(), "exceed") {
		t.Fatalf("ExtractUsage() error = %v", err)
	}
}

func assertUsage(t *testing.T, usage vnextprotocol.Usage, input, output, cacheRead, cacheWrite, reasoning int64) {
	t.Helper()
	got := []any{usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens, usage.ReasoningTokens}
	for index, value := range got {
		if value == nil {
			t.Fatalf("usage field %d is nil: %+v", index, usage)
		}
	}
	values := []int64{*usage.InputTokens, *usage.OutputTokens, *usage.CacheReadTokens, *usage.CacheWriteTokens, *usage.ReasoningTokens}
	want := []int64{input, output, cacheRead, cacheWrite, reasoning}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("usage = %#v, want %#v", values, want)
	}
}
