package gemini

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

func TestGenerateContentRegistrationIsCompleteAndIsolated(t *testing.T) {
	adapter, err := NewGenerateContentAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	if err != nil {
		t.Fatal(err)
	}
	registry := vnextprotocol.NewRegistry()
	contract, err := registry.Register(vnextprotocol.Gemini, vnextprotocol.GeminiGenerateContent, adapter)
	if err != nil {
		t.Fatal(err)
	}
	want := vnextprotocol.Capabilities{Discovery: true, Request: true, Response: true, Stream: true, Usage: true, Error: true}
	if !contract.Routable() || contract.Capabilities != want {
		t.Fatalf("Gemini GenerateContent is not fully routable: %+v", contract)
	}
	components, err := registry.Components(vnextprotocol.Gemini, vnextprotocol.GeminiGenerateContent)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := components.RequestEncoder.(*GenerateContentAdapter); !ok {
		t.Fatalf("registry contains the wrong adapter: %T", components.RequestEncoder)
	}
	for _, pair := range []struct {
		protocol vnextprotocol.Protocol
		surface  vnextprotocol.Surface
	}{
		{vnextprotocol.OpenAI, vnextprotocol.OpenAIChatCompletions},
		{vnextprotocol.OpenAI, vnextprotocol.OpenAIResponses},
		{vnextprotocol.Anthropic, vnextprotocol.AnthropicMessages},
	} {
		other, err := registry.Lookup(pair.protocol, pair.surface)
		if err != nil {
			t.Fatal(err)
		}
		if other.Routable() || other.Capabilities != (vnextprotocol.Capabilities{}) {
			t.Fatalf("Gemini registration leaked into %s/%s: %+v", pair.protocol, pair.surface, other)
		}
	}
	_, err = adapter.EncodeRequest(t.Context(), vnextprotocol.RequestBuildInput{
		Protocol: vnextprotocol.OpenAI, Surface: vnextprotocol.OpenAIChatCompletions,
		BaseURL: "https://example.test", Model: "gemini-2.5-pro", Payload: []byte(`{"contents":[{}]}`),
		Auth: vnextprotocol.AuthInput{Scheme: vnextprotocol.AuthXGoogAPIKey, Secret: "secret"},
	})
	if err == nil {
		t.Fatal("Gemini adapter accepted an OpenAI surface")
	}
}

func TestDiscoverModelsPaginatesFiltersAndUsesHeaderAuth(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/relay/v1beta/models" {
			t.Errorf("unexpected discovery request %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("x-goog-api-key") != "gemini-secret" || request.URL.Query().Get("key") != "" {
			t.Errorf("unexpected Gemini auth: header=%q query=%q", request.Header.Get("x-goog-api-key"), request.URL.Query().Get("key"))
		}
		if request.URL.Query().Get("pageSize") != "1000" {
			t.Errorf("pageSize = %q", request.URL.Query().Get("pageSize"))
		}
		switch request.URL.Query().Get("pageToken") {
		case "":
			_, _ = writer.Write([]byte(`{"models":[{"name":"models/gemini-z","supportedGenerationMethods":["generateContent"]},{"name":"models/embed-only","supportedGenerationMethods":["embedContent"]}],"nextPageToken":"page-2"}`))
		case "page-2":
			_, _ = writer.Write([]byte(`{"models":[{"name":"models/gemini-a","supportedGenerationMethods":["generateContent"]},{"name":"models/gemini-z","supportedGenerationMethods":["generateContent"]}]}`))
		default:
			t.Errorf("unexpected page token %q", request.URL.Query().Get("pageToken"))
		}
	}))
	defer server.Close()
	adapter, _ := NewGenerateContentAdapter(server.Client())
	result, err := adapter.DiscoverModels(t.Context(), vnextprotocol.DiscoveryInput{
		BaseURL: server.URL + "/relay/v1beta/models/old:streamGenerateContent?alt=sse&tenant=a",
		Auth:    vnextprotocol.AuthInput{Scheme: vnextprotocol.AuthXGoogAPIKey, Secret: "gemini-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || !reflect.DeepEqual(result.Models, []string{"models/gemini-a", "models/gemini-z"}) {
		t.Fatalf("unexpected discovery result calls=%d models=%#v", calls.Load(), result.Models)
	}
}

func TestDiscoverModelsRejectsRepeatedTokenAndControlsErrors(t *testing.T) {
	t.Run("repeated token", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			_, _ = writer.Write([]byte(`{"models":[],"nextPageToken":"same"}`))
		}))
		defer server.Close()
		adapter, _ := NewGenerateContentAdapter(server.Client())
		_, err := adapter.DiscoverModels(t.Context(), vnextprotocol.DiscoveryInput{BaseURL: server.URL, Auth: geminiAuth("secret")})
		if err == nil || !strings.Contains(err.Error(), "repeated") {
			t.Fatalf("DiscoverModels() error = %v", err)
		}
	})
	t.Run("secret safe", func(t *testing.T) {
		secret := "gemini-secret-must-not-leak"
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = writer.Write([]byte(`{"error":{"code":401,"message":"` + secret + `","status":"UNAUTHENTICATED"}}`))
		}))
		defer server.Close()
		adapter, _ := NewGenerateContentAdapter(server.Client())
		_, err := adapter.DiscoverModels(t.Context(), vnextprotocol.DiscoveryInput{BaseURL: server.URL, Auth: geminiAuth(secret)})
		if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "credential_auth") {
			t.Fatalf("DiscoverModels() error = %v", err)
		}
	})
}

func TestEncodeGenerateAndStreamRequests(t *testing.T) {
	adapter, _ := NewGenerateContentAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	tests := []struct {
		name       string
		payload    string
		wantSuffix string
		wantAccept string
		wantAlt    string
	}{
		{name: "generate", payload: `{"model":"public","contents":[{"role":"user","parts":[{"text":"public in prompt"}]}]}`, wantSuffix: "/v1beta/models/gemini-2.5-pro:generateContent", wantAccept: "application/json"},
		{name: "stream", payload: `{"stream":true,"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`, wantSuffix: "/v1beta/models/gemini-2.5-pro:streamGenerateContent", wantAccept: "text/event-stream", wantAlt: "sse"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := adapter.EncodeRequest(t.Context(), vnextprotocol.RequestBuildInput{
				Protocol: vnextprotocol.Gemini, Surface: vnextprotocol.GeminiGenerateContent,
				BaseURL: "https://relay.example.test/prefix/v1beta/models/old:generateContent?tenant=a", Model: "models/gemini-2.5-pro",
				Payload: []byte(test.payload), Auth: geminiAuth("gemini-key"),
			})
			if err != nil {
				t.Fatal(err)
			}
			request, _ := http.NewRequest(encoded.Method, encoded.URL, nil)
			if !strings.HasSuffix(request.URL.Path, test.wantSuffix) || request.URL.Query().Get("alt") != test.wantAlt || request.URL.Query().Get("tenant") != "a" {
				t.Fatalf("encoded URL = %s", encoded.URL)
			}
			if encoded.Header.Get("x-goog-api-key") != "gemini-key" || encoded.Header.Get("Accept") != test.wantAccept {
				t.Fatalf("encoded headers = %#v", encoded.Header)
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(encoded.Body, &payload); err != nil {
				t.Fatal(err)
			}
			if _, exists := payload["model"]; exists {
				t.Fatalf("model leaked into Gemini body: %s", encoded.Body)
			}
			if _, exists := payload["stream"]; exists {
				t.Fatalf("stream control leaked into Gemini body: %s", encoded.Body)
			}
			if !bytes.Contains(encoded.Body, []byte("public in prompt")) && test.name == "generate" {
				t.Fatalf("prompt was structurally changed: %s", encoded.Body)
			}
		})
	}
}

func TestEncodeRequiresNativeHeaderAuthAndSafeURL(t *testing.T) {
	adapter, _ := NewGenerateContentAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	secret := "query-secret-must-not-leak"
	base := vnextprotocol.RequestBuildInput{
		Protocol: vnextprotocol.Gemini, Surface: vnextprotocol.GeminiGenerateContent,
		BaseURL: "https://example.test", Model: "gemini-2.5-pro", Payload: []byte(`{"contents":[{}]}`),
		Auth: vnextprotocol.AuthInput{Scheme: vnextprotocol.AuthQueryKey, Secret: secret},
	}
	if _, err := adapter.EncodeRequest(t.Context(), base); err == nil || !strings.Contains(err.Error(), "x-goog-api-key") || strings.Contains(err.Error(), secret) {
		t.Fatalf("query auth error = %v", err)
	}
	base.Auth = geminiAuth(secret)
	base.BaseURL = "https://example.test/v1beta?key=embedded-secret"
	if _, err := adapter.EncodeRequest(t.Context(), base); err == nil || !strings.Contains(err.Error(), "query-key") {
		t.Fatalf("embedded query key error = %v", err)
	}
	base.BaseURL = "https://example.test"
	base.Model = "models/a/b"
	if _, err := adapter.EncodeRequest(t.Context(), base); err == nil || !strings.Contains(err.Error(), "path segment") {
		t.Fatalf("invalid model error = %v", err)
	}
}

func TestDecodeResponseAndUsageSemantics(t *testing.T) {
	adapter, _ := NewGenerateContentAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	body := validResponseBody("hello")
	decoded, err := adapter.DecodeResponse(t.Context(), vnextprotocol.ResponseInput{StatusCode: http.StatusOK, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Model != "gemini-2.5-pro-001" || !bytes.Equal(decoded.Body, body) {
		t.Fatalf("decoded response = %+v", decoded)
	}
	usage, err := adapter.ExtractUsage(t.Context(), vnextprotocol.UsageInput{Body: body})
	if err != nil {
		t.Fatal(err)
	}
	assertUsage(t, usage, 17, 7, 5, 0, 3)
}

func TestDecodeResponseAcceptsPromptBlockAndRejectsMalformedSuccess(t *testing.T) {
	adapter, _ := NewGenerateContentAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	blocked := []byte(`{"promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{"promptTokenCount":3,"totalTokenCount":3},"modelVersion":"gemini-2.5-pro-001"}`)
	if _, err := adapter.DecodeResponse(t.Context(), vnextprotocol.ResponseInput{StatusCode: http.StatusOK, Body: blocked}); err != nil {
		t.Fatalf("prompt block rejected: %v", err)
	}
	tests := []struct{ name, body, want string }{
		{name: "empty", body: `{}`, want: "candidates"},
		{name: "candidate", body: `{"candidates":[{}],"usageMetadata":{"promptTokenCount":1,"totalTokenCount":1},"modelVersion":"m"}`, want: "content"},
		{name: "model", body: `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`, want: "model"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := adapter.DecodeResponse(t.Context(), vnextprotocol.ResponseInput{StatusCode: http.StatusOK, Body: []byte(test.body)})
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("DecodeResponse() error = %v", err)
			}
		})
	}
	missingUsage := []byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"modelVersion":"m"}`)
	if _, err := adapter.DecodeResponse(t.Context(), vnextprotocol.ResponseInput{StatusCode: http.StatusOK, Body: missingUsage}); err != nil {
		t.Fatalf("response without usage rejected: %v", err)
	}
	if usage, err := adapter.ExtractUsage(t.Context(), vnextprotocol.UsageInput{Body: missingUsage}); err == nil {
		t.Fatalf("ExtractUsage() usage = %+v, want missing usage error", usage)
	}
}

func TestDecodeStreamLifecycleUsageAndSemanticCommit(t *testing.T) {
	adapter, _ := NewGenerateContentAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	stream := sse(`{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]}}],"modelVersion":"gemini-2.5-pro-001"}`) +
		sse(`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":20,"cachedContentTokenCount":5,"candidatesTokenCount":7,"toolUsePromptTokenCount":2,"thoughtsTokenCount":3,"totalTokenCount":32},"modelVersion":"gemini-2.5-pro-001"}`)
	events := make([]vnextprotocol.StreamEvent, 0, 2)
	result, err := adapter.DecodeStream(t.Context(), vnextprotocol.StreamInput{StatusCode: http.StatusOK, Body: strings.NewReader(stream)}, func(event vnextprotocol.StreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Terminal || result.Model != "gemini-2.5-pro-001" || len(events) != 2 || !events[0].Semantic || events[0].Terminal || !events[1].Terminal {
		t.Fatalf("unexpected stream result=%+v events=%+v", result, events)
	}
	usage, err := adapter.ExtractUsage(t.Context(), vnextprotocol.UsageInput{Events: events})
	if err != nil {
		t.Fatal(err)
	}
	assertUsage(t, usage, 17, 7, 5, 0, 3)
}

func TestDecodeStreamDetectsTruncationProtocolAndControlledErrors(t *testing.T) {
	adapter, _ := NewGenerateContentAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	t.Run("missing terminal usage", func(t *testing.T) {
		stream := sse(`{"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"modelVersion":"m"}`)
		events := make([]vnextprotocol.StreamEvent, 0, 1)
		result, err := adapter.DecodeStream(t.Context(), vnextprotocol.StreamInput{StatusCode: http.StatusOK, Body: strings.NewReader(stream)}, func(event vnextprotocol.StreamEvent) error {
			events = append(events, event)
			return nil
		})
		if err != nil || !result.Terminal {
			t.Fatalf("terminal result=%+v err=%v", result, err)
		}
		if usage, err := adapter.ExtractUsage(t.Context(), vnextprotocol.UsageInput{Events: events}); err == nil {
			t.Fatalf("ExtractUsage() usage = %+v, want missing usage error", usage)
		}
	})
	t.Run("model changes", func(t *testing.T) {
		stream := sse(`{"candidates":[{"content":{"parts":[{"text":"a"}]}}],"modelVersion":"a"}`) + sse(`{"candidates":[{"content":{"parts":[{"text":"b"}]}}],"modelVersion":"b"}`)
		_, err := adapter.DecodeStream(t.Context(), vnextprotocol.StreamInput{StatusCode: http.StatusOK, Body: strings.NewReader(stream)}, nil)
		if !errors.Is(err, ErrStreamProtocol) || !strings.Contains(err.Error(), "changed model") {
			t.Fatalf("DecodeStream() error = %v", err)
		}
	})
	t.Run("safe error event", func(t *testing.T) {
		secret := "gemini-stream-secret"
		stream := sse(`{"error":{"code":429,"message":"` + secret + `","status":"RESOURCE_EXHAUSTED"}}`)
		_, err := adapter.DecodeStream(t.Context(), vnextprotocol.StreamInput{StatusCode: http.StatusOK, Body: strings.NewReader(stream)}, nil)
		if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "credential_rate_limited") {
			t.Fatalf("DecodeStream() error = %v", err)
		}
	})
}

func TestGeminiErrorClassificationAndSecretSafety(t *testing.T) {
	adapter, _ := NewGenerateContentAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	secret := "gemini-error-secret"
	tests := []struct {
		status            int
		rpcStatus         string
		reason            string
		class             string
		retryable         bool
		credentialFailure bool
	}{
		{http.StatusBadRequest, "INVALID_ARGUMENT", "", "client_invalid", false, false},
		{http.StatusUnauthorized, "UNAUTHENTICATED", "API_KEY_INVALID", "credential_auth", false, true},
		{http.StatusForbidden, "PERMISSION_DENIED", "API_KEY_SERVICE_BLOCKED", "credential_permission", false, true},
		{http.StatusTooManyRequests, "RESOURCE_EXHAUSTED", "QUOTA_EXCEEDED", "credential_rate_limited", true, true},
		{http.StatusServiceUnavailable, "UNAVAILABLE", "", "upstream_transient", true, false},
		{http.StatusNotFound, "NOT_FOUND", "MODEL_NOT_FOUND", "model_unsupported", false, false},
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("%d-%s", test.status, test.reason), func(t *testing.T) {
			body := []byte(`{"error":{"code":` + fmt.Sprint(test.status) + `,"message":"` + secret + `","status":"` + test.rpcStatus + `","details":[{"reason":"` + test.reason + `"}]}}`)
			decoded, err := adapter.DecodeError(t.Context(), vnextprotocol.ErrorInput{StatusCode: test.status, Body: body})
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Class != test.class || decoded.Retryable != test.retryable || decoded.CredentialFailure != test.credentialFailure {
				t.Fatalf("decoded error = %+v", decoded)
			}
			encoded, _ := json.Marshal(decoded)
			if strings.Contains(string(encoded), secret) {
				t.Fatalf("decoded error leaked secret: %s", encoded)
			}
		})
	}
}

func TestGeminiBodyEventAndUsageLimits(t *testing.T) {
	adapter, _ := NewGenerateContentAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	oversized := bytes.Repeat([]byte("x"), maxBodyBytes+1)
	if _, err := adapter.DecodeResponse(t.Context(), vnextprotocol.ResponseInput{StatusCode: http.StatusOK, Body: oversized}); !errors.Is(err, errBodyTooLarge) {
		t.Fatalf("DecodeResponse() error = %v", err)
	}
	stream := "data: " + strings.Repeat("x", maxEventBytes) + "\n\n"
	if _, err := adapter.DecodeStream(t.Context(), vnextprotocol.StreamInput{StatusCode: http.StatusOK, Body: strings.NewReader(stream)}, nil); !errors.Is(err, errEventTooLarge) {
		t.Fatalf("DecodeStream() error = %v", err)
	}
	badUsage := []byte(`{"usageMetadata":{"promptTokenCount":4,"cachedContentTokenCount":5,"candidatesTokenCount":1,"totalTokenCount":5}}`)
	if _, err := adapter.ExtractUsage(t.Context(), vnextprotocol.UsageInput{Body: badUsage}); err == nil || !strings.Contains(err.Error(), "exceed") {
		t.Fatalf("ExtractUsage() error = %v", err)
	}
}

func validResponseBody(text string) []byte {
	payload := map[string]any{
		"candidates":    []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": text}}}, "finishReason": "STOP"}},
		"usageMetadata": map[string]any{"promptTokenCount": 20, "cachedContentTokenCount": 5, "candidatesTokenCount": 7, "toolUsePromptTokenCount": 2, "thoughtsTokenCount": 3, "totalTokenCount": 32},
		"modelVersion":  "gemini-2.5-pro-001",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return body
}

func geminiAuth(secret string) vnextprotocol.AuthInput {
	return vnextprotocol.AuthInput{Scheme: vnextprotocol.AuthXGoogAPIKey, Secret: secret}
}

func sse(data string) string {
	return "data: " + data + "\n\n"
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
