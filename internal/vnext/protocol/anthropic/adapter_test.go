package anthropic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
)

type doerFunc func(*http.Request) (*http.Response, error)

func (function doerFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestMessagesAdapterRegistersOnlyAnthropicMessages(t *testing.T) {
	adapter, err := NewMessagesAdapter(doerFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("unused")
	}))
	if err != nil {
		t.Fatal(err)
	}
	registry := vnextprotocol.NewRegistry()
	contract, err := registry.Register(vnextprotocol.Anthropic, vnextprotocol.AnthropicMessages, adapter)
	if err != nil {
		t.Fatal(err)
	}
	if !contract.Routable() || contract.Capabilities != (vnextprotocol.Capabilities{
		Discovery: true, Request: true, Response: true, Stream: true, Usage: true, Error: true,
	}) {
		t.Fatalf("Anthropic Messages is not fully routable: %+v", contract)
	}
	components, err := registry.Components(vnextprotocol.Anthropic, vnextprotocol.AnthropicMessages)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := components.RequestEncoder.(*MessagesAdapter); !ok {
		t.Fatalf("registry contains the wrong adapter: %T", components.RequestEncoder)
	}
	for _, pair := range []struct {
		protocol vnextprotocol.Protocol
		surface  vnextprotocol.Surface
	}{
		{protocol: vnextprotocol.OpenAI, surface: vnextprotocol.OpenAIChatCompletions},
		{protocol: vnextprotocol.OpenAI, surface: vnextprotocol.OpenAIResponses},
		{protocol: vnextprotocol.Gemini, surface: vnextprotocol.GeminiGenerateContent},
	} {
		other, err := registry.Lookup(pair.protocol, pair.surface)
		if err != nil {
			t.Fatal(err)
		}
		if other.Routable() || other.Capabilities != (vnextprotocol.Capabilities{}) {
			t.Fatalf("Anthropic registration leaked into %s/%s: %+v", pair.protocol, pair.surface, other)
		}
	}
	_, err = adapter.EncodeRequest(t.Context(), vnextprotocol.RequestBuildInput{
		Protocol: vnextprotocol.OpenAI,
		Surface:  vnextprotocol.OpenAIChatCompletions,
		BaseURL:  "https://api.example.test",
		Model:    "claude-source",
		Payload:  []byte(`{"messages":[]}`),
		Auth:     vnextprotocol.AuthInput{Scheme: vnextprotocol.AuthXAPIKey, Secret: "secret"},
	})
	if err == nil {
		t.Fatal("Anthropic adapter accepted an OpenAI surface")
	}
}

func TestDiscoverModelsPaginatesNativeAnthropicAPI(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/relay/v1/models" {
			t.Errorf("unexpected discovery request %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("x-api-key") != "anthropic-secret" || request.Header.Get("anthropic-version") != anthropicVersion {
			t.Errorf("unexpected discovery headers: %#v", request.Header)
		}
		switch request.URL.Query().Get("after_id") {
		case "":
			_, _ = writer.Write([]byte(`{"data":[{"id":"claude-z"},{"id":"claude-a"}],"has_more":true,"first_id":"claude-z","last_id":"cursor-1"}`))
		case "cursor-1":
			_, _ = writer.Write([]byte(`{"data":[{"id":"claude-m"},{"id":"claude-a"}],"has_more":false,"first_id":"claude-m","last_id":"claude-a"}`))
		default:
			t.Errorf("unexpected cursor %q", request.URL.Query().Get("after_id"))
		}
	}))
	defer server.Close()
	adapter, err := NewMessagesAdapter(server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.DiscoverModels(t.Context(), vnextprotocol.DiscoveryInput{
		BaseURL: server.URL + "/relay/v1/messages",
		Auth:    vnextprotocol.AuthInput{Scheme: vnextprotocol.AuthXAPIKey, Secret: "anthropic-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || !reflect.DeepEqual(result.Models, []string{"claude-a", "claude-m", "claude-z"}) {
		t.Fatalf("unexpected discovery result calls=%d models=%#v", calls.Load(), result.Models)
	}
}

func TestDiscoverModelsRejectsBrokenPaginationAndSecretEcho(t *testing.T) {
	secret := "anthropic-secret-must-not-leak"
	t.Run("missing cursor", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			_, _ = writer.Write([]byte(`{"data":[],"has_more":true,"last_id":null}`))
		}))
		defer server.Close()
		adapter, _ := NewMessagesAdapter(server.Client())
		_, err := adapter.DiscoverModels(t.Context(), vnextprotocol.DiscoveryInput{
			BaseURL: server.URL,
			Auth:    vnextprotocol.AuthInput{Scheme: vnextprotocol.AuthXAPIKey, Secret: secret},
		})
		if err == nil || !strings.Contains(err.Error(), "last_id") {
			t.Fatalf("DiscoverModels() error = %v", err)
		}
	})
	t.Run("safe error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = writer.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"` + secret + `"}}`))
		}))
		defer server.Close()
		adapter, _ := NewMessagesAdapter(server.Client())
		_, err := adapter.DiscoverModels(t.Context(), vnextprotocol.DiscoveryInput{
			BaseURL: server.URL,
			Auth:    vnextprotocol.AuthInput{Scheme: vnextprotocol.AuthXAPIKey, Secret: secret},
		})
		if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "credential_auth") {
			t.Fatalf("DiscoverModels() error = %v", err)
		}
	})
}

func TestEncodeMessagesRequestRewritesModelAndAppliesNativeHeaders(t *testing.T) {
	adapter, _ := NewMessagesAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	encoded, err := adapter.EncodeRequest(t.Context(), vnextprotocol.RequestBuildInput{
		Protocol: vnextprotocol.Anthropic,
		Surface:  vnextprotocol.AnthropicMessages,
		BaseURL:  "https://relay.example.test/prefix/v1/models",
		Model:    "claude-source",
		Payload:  []byte(`{"model":"public-model","max_tokens":256,"messages":[{"role":"user","content":"public-model in prompt"}],"stream":true}`),
		Auth:     vnextprotocol.AuthInput{Scheme: vnextprotocol.AuthXAPIKey, Secret: "anthropic-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if encoded.Method != http.MethodPost || encoded.URL != "https://relay.example.test/prefix/v1/messages" {
		t.Fatalf("unexpected encoded destination: %s %s", encoded.Method, encoded.URL)
	}
	if encoded.Header.Get("x-api-key") != "anthropic-key" || encoded.Header.Get("anthropic-version") != anthropicVersion || encoded.Header.Get("Accept") != "text/event-stream" {
		t.Fatalf("unexpected native headers: %#v", encoded.Header)
	}
	var payload struct {
		Model    string `json:"model"`
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(encoded.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Model != "claude-source" || len(payload.Messages) != 1 || payload.Messages[0].Content != "public-model in prompt" {
		t.Fatalf("request was not structurally rewritten: %s", encoded.Body)
	}
}

func TestMessagesRequestRequiresExplicitXAPIKeyWithoutLeakingSecret(t *testing.T) {
	adapter, _ := NewMessagesAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	secret := "anthropic-bearer-secret"
	_, err := adapter.EncodeRequest(t.Context(), vnextprotocol.RequestBuildInput{
		Protocol: vnextprotocol.Anthropic,
		Surface:  vnextprotocol.AnthropicMessages,
		BaseURL:  "https://api.example.test",
		Model:    "claude-source",
		Payload:  []byte(`{"messages":[],"max_tokens":1}`),
		Auth:     vnextprotocol.AuthInput{Scheme: vnextprotocol.AuthBearer, Secret: secret},
	})
	if err == nil || !strings.Contains(err.Error(), "x-api-key") || strings.Contains(err.Error(), secret) {
		t.Fatalf("EncodeRequest() error = %v", err)
	}
}

func TestDecodeMessageResponseAndAnthropicUsageSemantics(t *testing.T) {
	adapter, _ := NewMessagesAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	body := validMessageBody("hello")
	decoded, err := adapter.DecodeResponse(t.Context(), vnextprotocol.ResponseInput{StatusCode: http.StatusOK, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Model != "claude-source" || !bytes.Equal(decoded.Body, body) {
		t.Fatalf("unexpected decoded response: %+v", decoded)
	}
	usage, err := adapter.ExtractUsage(t.Context(), vnextprotocol.UsageInput{Body: body})
	if err != nil {
		t.Fatal(err)
	}
	// Anthropic input_tokens excludes cache read/write, so it must not be reduced.
	assertUsage(t, usage, 70, 25, 20, 10, 5)
	assertCacheWriteTTLUsage(t, usage, 4, 6)
}

func TestDecodeMessageAcceptsToolUseAndRejectsMalformedSuccesses(t *testing.T) {
	adapter, _ := NewMessagesAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	toolBody := []byte(`{"type":"message","role":"assistant","model":"claude-source","content":[{"type":"tool_use","id":"tool_1","name":"weather","input":{"city":"Shanghai"}}],"usage":{"input_tokens":3,"output_tokens":2}}`)
	if _, err := adapter.DecodeResponse(t.Context(), vnextprotocol.ResponseInput{StatusCode: http.StatusOK, Body: toolBody}); err != nil {
		t.Fatalf("tool use response rejected: %v", err)
	}
	secret := "anthropic-error-secret"
	tests := []struct {
		name   string
		body   string
		want   string
		secret bool
	}{
		{name: "error envelope", body: `{"type":"error","error":{"type":"authentication_error","message":"` + secret + `"}}`, want: "credential_auth", secret: true},
		{name: "wrong type", body: `{"type":"completion","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`, want: "assistant message"},
		{name: "content", body: `{"type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`, want: "content"},
		{name: "content block", body: `{"type":"message","role":"assistant","model":"m","content":[{"type":"text"}],"usage":{"input_tokens":1,"output_tokens":1}}`, want: "text field"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := adapter.DecodeResponse(t.Context(), vnextprotocol.ResponseInput{StatusCode: http.StatusOK, Body: []byte(test.body)})
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("DecodeResponse() error = %v", err)
			}
			if test.secret && strings.Contains(err.Error(), secret) {
				t.Fatalf("DecodeResponse() leaked secret: %v", err)
			}
		})
	}
	emptyText := []byte(`{"type":"message","role":"assistant","model":"m","content":[{"type":"text","text":""}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	if _, err := adapter.DecodeResponse(t.Context(), vnextprotocol.ResponseInput{StatusCode: http.StatusOK, Body: emptyText}); err != nil {
		t.Fatalf("valid empty Anthropic text response rejected: %v", err)
	}
	missingUsage := []byte(`{"type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}]}`)
	if _, err := adapter.DecodeResponse(t.Context(), vnextprotocol.ResponseInput{StatusCode: http.StatusOK, Body: missingUsage}); err != nil {
		t.Fatalf("response without usage rejected: %v", err)
	}
	if usage, err := adapter.ExtractUsage(t.Context(), vnextprotocol.UsageInput{Body: missingUsage}); err == nil {
		t.Fatalf("ExtractUsage() usage = %+v, want missing usage error", usage)
	}
}

func TestDecodeMessagesStreamLifecycleAndUsage(t *testing.T) {
	adapter, _ := NewMessagesAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	stream := validStream(false)
	events := make([]vnextprotocol.StreamEvent, 0, 5)
	result, err := adapter.DecodeStream(t.Context(), vnextprotocol.StreamInput{StatusCode: http.StatusOK, Body: strings.NewReader(stream)}, func(event vnextprotocol.StreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Terminal || result.Model != "claude-source" {
		t.Fatalf("unexpected stream result: %+v", result)
	}
	if len(events) != 5 || events[0].Semantic || events[1].Semantic || !events[2].Semantic || events[3].Semantic || !events[4].Terminal {
		t.Fatalf("unexpected stream events: %+v", events)
	}
	usage, err := adapter.ExtractUsage(t.Context(), vnextprotocol.UsageInput{Events: events})
	if err != nil {
		t.Fatal(err)
	}
	assertUsage(t, usage, 70, 25, 20, 10, 5)
	assertCacheWriteTTLUsage(t, usage, 4, 6)
}

func TestMessagesStreamRequiresMessageStopAndCompleteUsage(t *testing.T) {
	adapter, _ := NewMessagesAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	t.Run("terminal event at EOF", func(t *testing.T) {
		stream := strings.TrimSuffix(validStream(false), "\n\n")
		result, err := adapter.DecodeStream(t.Context(), vnextprotocol.StreamInput{StatusCode: http.StatusOK, Body: strings.NewReader(stream)}, nil)
		if err != nil || !result.Terminal || result.Model != "claude-source" {
			t.Fatalf("unexpected terminal EOF result=%+v err=%v", result, err)
		}
	})
	t.Run("EOF", func(t *testing.T) {
		result, err := adapter.DecodeStream(t.Context(), vnextprotocol.StreamInput{StatusCode: http.StatusOK, Body: strings.NewReader(validStream(true))}, nil)
		if !errors.Is(err, ErrStreamTruncated) || result.Terminal {
			t.Fatalf("unexpected truncated result=%+v err=%v", result, err)
		}
	})
	t.Run("missing delta usage", func(t *testing.T) {
		stream := sse("message_start", `{"type":"message_start","message":{"type":"message","role":"assistant","model":"claude-source","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`) +
			sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`) +
			sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`) +
			sse("message_stop", `{"type":"message_stop"}`)
		events := make([]vnextprotocol.StreamEvent, 0, 4)
		result, err := adapter.DecodeStream(t.Context(), vnextprotocol.StreamInput{StatusCode: http.StatusOK, Body: strings.NewReader(stream)}, func(event vnextprotocol.StreamEvent) error {
			events = append(events, event)
			return nil
		})
		if err != nil || !result.Terminal {
			t.Fatalf("unexpected terminal result=%+v err=%v", result, err)
		}
		if usage, err := adapter.ExtractUsage(t.Context(), vnextprotocol.UsageInput{Events: events}); err == nil {
			t.Fatalf("ExtractUsage() usage = %+v, want incomplete usage lifecycle error", usage)
		}
	})
}

func TestMessagesStreamErrorEventIsControlled(t *testing.T) {
	adapter, _ := NewMessagesAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	secret := "anthropic-stream-secret"
	stream := sse("error", `{"type":"error","error":{"type":"overloaded_error","message":"`+secret+`"}}`)
	result, err := adapter.DecodeStream(t.Context(), vnextprotocol.StreamInput{StatusCode: http.StatusOK, Body: strings.NewReader(stream)}, nil)
	if err == nil || !strings.Contains(err.Error(), "upstream_transient") || strings.Contains(err.Error(), secret) || result.Terminal {
		t.Fatalf("unsafe error event result=%+v err=%v", result, err)
	}
}

func TestMessagesStreamRejectsEventNameMismatch(t *testing.T) {
	adapter, _ := NewMessagesAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	stream := sse("message_stop", `{"type":"ping"}`)
	_, err := adapter.DecodeStream(t.Context(), vnextprotocol.StreamInput{StatusCode: http.StatusOK, Body: strings.NewReader(stream)}, nil)
	if !errors.Is(err, ErrStreamProtocol) {
		t.Fatalf("DecodeStream() error = %v", err)
	}
}

func TestAnthropicErrorClassificationAndSecretSafety(t *testing.T) {
	adapter, _ := NewMessagesAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	secret := "anthropic-secret-in-message"
	tests := []struct {
		status            int
		errorType         string
		class             string
		retryable         bool
		credentialFailure bool
	}{
		{status: http.StatusBadRequest, errorType: "invalid_request_error", class: "client_invalid"},
		{status: http.StatusUnauthorized, errorType: "authentication_error", class: "credential_auth", credentialFailure: true},
		{status: http.StatusForbidden, errorType: "permission_error", class: "credential_permission", credentialFailure: true},
		{status: http.StatusTooManyRequests, errorType: "rate_limit_error", class: "credential_rate_limited", retryable: true, credentialFailure: true},
		{status: 529, errorType: "overloaded_error", class: "upstream_transient", retryable: true},
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("%d", test.status), func(t *testing.T) {
			decoded, err := adapter.DecodeError(t.Context(), vnextprotocol.ErrorInput{
				StatusCode: test.status,
				Body:       []byte(`{"type":"error","error":{"type":"` + test.errorType + `","message":"` + secret + `"}}`),
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
			if strings.Contains(string(encoded), secret) {
				t.Fatalf("decoded error leaked secret: %s", encoded)
			}
		})
	}
}

func TestAnthropicBodyEventAndUsageLimits(t *testing.T) {
	adapter, _ := NewMessagesAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	oversized := bytes.Repeat([]byte("x"), maxBodyBytes+1)
	if _, err := adapter.DecodeResponse(t.Context(), vnextprotocol.ResponseInput{StatusCode: http.StatusOK, Body: oversized}); !errors.Is(err, errBodyTooLarge) {
		t.Fatalf("DecodeResponse() error = %v", err)
	}
	stream := "data: " + strings.Repeat("x", maxEventBytes) + "\n\n"
	if _, err := adapter.DecodeStream(t.Context(), vnextprotocol.StreamInput{StatusCode: http.StatusOK, Body: strings.NewReader(stream)}, nil); !errors.Is(err, errEventTooLarge) {
		t.Fatalf("DecodeStream() error = %v", err)
	}
	badUsage := []byte(`{"usage":{"input_tokens":1,"output_tokens":2,"output_tokens_details":{"thinking_tokens":3}}}`)
	if _, err := adapter.ExtractUsage(t.Context(), vnextprotocol.UsageInput{Body: badUsage}); err == nil || !strings.Contains(err.Error(), "exceed") {
		t.Fatalf("ExtractUsage() error = %v", err)
	}
	badCacheTTL := []byte(`{"usage":{"input_tokens":1,"output_tokens":1,"cache_creation_input_tokens":10,"cache_creation":{"ephemeral_5m_input_tokens":3,"ephemeral_1h_input_tokens":4}}}`)
	if _, err := adapter.ExtractUsage(t.Context(), vnextprotocol.UsageInput{Body: badCacheTTL}); err == nil || !strings.Contains(err.Error(), "TTL details") {
		t.Fatalf("ExtractUsage() cache TTL error = %v", err)
	}
}

func validMessageBody(text string) []byte {
	payload := map[string]any{
		"id":          "msg_1",
		"type":        "message",
		"role":        "assistant",
		"model":       "claude-source",
		"stop_reason": "end_turn",
		"content": []any{map[string]any{
			"type": "text",
			"text": text,
		}},
		"usage": map[string]any{
			"input_tokens":                70,
			"output_tokens":               30,
			"cache_creation_input_tokens": 10,
			"cache_creation": map[string]any{
				"ephemeral_5m_input_tokens": 4,
				"ephemeral_1h_input_tokens": 6,
			},
			"cache_read_input_tokens": 20,
			"output_tokens_details": map[string]any{
				"thinking_tokens": 5,
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return body
}

func validStream(truncated bool) string {
	stream := sse("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-source","content":[],"usage":{"input_tokens":70,"output_tokens":0,"cache_creation_input_tokens":10,"cache_read_input_tokens":20,"cache_creation":{"ephemeral_5m_input_tokens":4,"ephemeral_1h_input_tokens":6}}}}`) +
		sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
		sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`) +
		sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":null,"output_tokens":30,"cache_creation_input_tokens":null,"cache_read_input_tokens":null,"output_tokens_details":{"thinking_tokens":5}}}`)
	if !truncated {
		stream += sse("message_stop", `{"type":"message_stop"}`)
	}
	return stream
}

func sse(event, data string) string {
	return "event: " + event + "\n" + "data: " + data + "\n\n"
}

func assertUsage(t *testing.T, usage vnextprotocol.Usage, input, output, cacheRead, cacheWrite, reasoning int64) {
	t.Helper()
	values := []*int64{usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens, usage.ReasoningTokens}
	for index, value := range values {
		if value == nil {
			t.Fatalf("usage field %d is nil: %+v", index, usage)
		}
	}
	got := []int64{*usage.InputTokens, *usage.OutputTokens, *usage.CacheReadTokens, *usage.CacheWriteTokens, *usage.ReasoningTokens}
	want := []int64{input, output, cacheRead, cacheWrite, reasoning}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("usage = %#v, want %#v", got, want)
	}
}

func assertCacheWriteTTLUsage(t *testing.T, usage vnextprotocol.Usage, cacheWrite5M, cacheWrite1H int64) {
	t.Helper()
	if usage.CacheWrite5MTokens == nil || usage.CacheWrite1HTokens == nil {
		t.Fatalf("cache write TTL usage is missing: %+v", usage)
	}
	if *usage.CacheWrite5MTokens != cacheWrite5M || *usage.CacheWrite1HTokens != cacheWrite1H {
		t.Fatalf("cache write TTL usage = (%d, %d), want (%d, %d)",
			*usage.CacheWrite5MTokens, *usage.CacheWrite1HTokens, cacheWrite5M, cacheWrite1H)
	}
}
