package dataplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LuTianTian001/JieShan/internal/vnext/gateway"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	"github.com/LuTianTian001/JieShan/internal/vnext/resolver"
)

func TestModelsUsesBearerAuthenticationAndOpenAIProtocolProjection(t *testing.T) {
	models := &fakeModels{items: []resolver.Model{{ID: "gpt-a"}, {ID: "gpt-b"}}}
	handler := mustHandler(t, executorFunc(func(context.Context, gateway.Input, gateway.StreamSink) (gateway.Result, error) {
		t.Fatal("executor called for models")
		return gateway.Result{}, nil
	}), models)
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer js_models")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || models.key != "js_models" || models.wire != protocol.OpenAI {
		t.Fatalf("response=%d models=%+v", response.Code, models)
	}
	var body struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Object != "list" || len(body.Data) != 2 || body.Data[0].ID != "gpt-a" || body.Data[1].ID != "gpt-b" {
		t.Fatalf("body = %+v", body)
	}

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	assertAPIError(t, unauthorized, http.StatusUnauthorized, "invalid_api_key")
}

func TestChatCompletionsPassesAnExactIngressSurfaceAndReturnsTheGatewayResult(t *testing.T) {
	var captured gateway.Input
	executor := executorFunc(func(_ context.Context, input gateway.Input, sink gateway.StreamSink) (gateway.Result, error) {
		captured = input
		if sink != nil {
			t.Fatal("non-stream request received a sink")
		}
		return gateway.Result{
			RequestID: input.RequestID, StatusCode: http.StatusOK,
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body:   []byte(`{"id":"ok","model":"source","choices":[{"message":{"content":"done"}}]}`),
		}, nil
	})
	handler := mustHandler(t, executor, &fakeModels{})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"public-model","messages":[{"role":"user","content":"hello"}]}`,
	))
	request.Header.Set("Authorization", "Bearer js_chat")
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("X-Request-Id", "req-client")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("X-JieShan-Request-Id") != "req-client" {
		t.Fatalf("response = %d, headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if captured.DownstreamKey != "js_chat" || captured.PublicModel != "public-model" || captured.Stream ||
		captured.IngressProtocol != protocol.OpenAI || captured.IngressSurface != protocol.OpenAIChatCompletions ||
		captured.RequestID != "req-client" {
		t.Fatalf("captured input = %+v", captured)
	}
	if !strings.Contains(response.Body.String(), `"content":"done"`) {
		t.Fatalf("body = %s", response.Body.String())
	}
}

func TestResponsesPassesTheNativeOpenAIResponsesSurface(t *testing.T) {
	var captured gateway.Input
	handler := mustHandler(t, executorFunc(func(_ context.Context, input gateway.Input, sink gateway.StreamSink) (gateway.Result, error) {
		captured = input
		if sink != nil {
			t.Fatal("non-stream Responses request received a sink")
		}
		return gateway.Result{
			RequestID: input.RequestID, StatusCode: http.StatusOK,
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body:   []byte(`{"id":"resp_1","object":"response","output":[]}`),
		}, nil
	}), &fakeModels{})
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(
		`{"model":"public-response","input":"hello"}`,
	))
	request.Header.Set("Authorization", "Bearer js_responses")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"object":"response"`) {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	if captured.DownstreamKey != "js_responses" || captured.PublicModel != "public-response" ||
		captured.IngressProtocol != protocol.OpenAI || captured.IngressSurface != protocol.OpenAIResponses || captured.Stream {
		t.Fatalf("captured input = %+v", captured)
	}
}

func TestAnthropicMessagesAcceptsNativeAPIKeyAndUsesTheExactSurface(t *testing.T) {
	var captured gateway.Input
	handler := mustHandler(t, executorFunc(func(_ context.Context, input gateway.Input, sink gateway.StreamSink) (gateway.Result, error) {
		captured = input
		if sink != nil {
			t.Fatal("non-stream Anthropic request received a sink")
		}
		return gateway.Result{
			RequestID: input.RequestID, StatusCode: http.StatusOK,
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body:   []byte(`{"id":"msg_1","type":"message","content":[]}`),
		}, nil
	}), &fakeModels{})
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"public-claude","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`,
	))
	request.Header.Set("x-api-key", "js_anthropic")
	request.Header.Set("anthropic-version", "2023-06-01")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"type":"message"`) {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	if captured.DownstreamKey != "js_anthropic" || captured.PublicModel != "public-claude" ||
		captured.IngressProtocol != protocol.Anthropic || captured.IngressSurface != protocol.AnthropicMessages || captured.Stream {
		t.Fatalf("captured input = %+v", captured)
	}
}

func TestAnthropicModelsUseAnthropicProjection(t *testing.T) {
	models := &fakeModels{items: []resolver.Model{{ID: "claude-a"}, {ID: "claude-b"}}}
	handler := mustHandler(t, executorFunc(func(context.Context, gateway.Input, gateway.StreamSink) (gateway.Result, error) {
		t.Fatal("executor called for models")
		return gateway.Result{}, nil
	}), models)
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("x-api-key", "js_anthropic_models")
	request.Header.Set("anthropic-version", "2023-06-01")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || models.wire != protocol.Anthropic || models.key != "js_anthropic_models" {
		t.Fatalf("response=%d models=%+v body=%s", response.Code, models, response.Body.String())
	}
	var body struct {
		Data []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"data"`
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.HasMore || len(body.Data) != 2 || body.Data[0].ID != "claude-a" || body.Data[0].Type != "model" {
		t.Fatalf("body = %+v", body)
	}
}

func TestGeminiGenerateContentUsesThePathModelAndNativeStreamSurface(t *testing.T) {
	var captured gateway.Input
	handler := mustHandler(t, executorFunc(func(_ context.Context, input gateway.Input, sink gateway.StreamSink) (gateway.Result, error) {
		captured = input
		if !input.Stream || sink == nil {
			t.Fatalf("Gemini stream input = %+v sink=%v", input, sink)
		}
		var payload map[string]any
		if err := json.Unmarshal(input.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if stream, ok := payload["stream"].(bool); !ok || !stream {
			t.Fatalf("payload stream marker = %#v", payload["stream"])
		}
		if err := sink.Commit(http.Header{"Content-Type": []string{"text/event-stream"}}); err != nil {
			return gateway.Result{}, err
		}
		if err := sink.Write([]byte("data: {\"candidates\":[]}\n\n")); err != nil {
			return gateway.Result{}, err
		}
		return gateway.Result{RequestID: input.RequestID, Stream: true}, nil
	}), &fakeModels{})
	request := httptest.NewRequest(http.MethodPost, "/v1beta/models/public-gemini:streamGenerateContent?alt=sse", strings.NewReader(
		`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`,
	))
	request.Header.Set("x-goog-api-key", "js_gemini")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "candidates") {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	if captured.DownstreamKey != "js_gemini" || captured.PublicModel != "public-gemini" ||
		captured.IngressProtocol != protocol.Gemini || captured.IngressSurface != protocol.GeminiGenerateContent || !captured.Stream {
		t.Fatalf("captured input = %+v", captured)
	}
}

func TestGeminiModelsUseGeminiProjectionAndQueryKey(t *testing.T) {
	models := &fakeModels{items: []resolver.Model{{ID: "gemini-a"}, {ID: "models/gemini-b"}}}
	handler := mustHandler(t, executorFunc(func(context.Context, gateway.Input, gateway.StreamSink) (gateway.Result, error) {
		t.Fatal("executor called for models")
		return gateway.Result{}, nil
	}), models)
	request := httptest.NewRequest(http.MethodGet, "/v1beta/models?key=js_gemini_models", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || models.wire != protocol.Gemini || models.key != "js_gemini_models" {
		t.Fatalf("response=%d models=%+v body=%s", response.Code, models, response.Body.String())
	}
	var body struct {
		Models []struct {
			Name    string   `json:"name"`
			Methods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Models) != 2 || body.Models[0].Name != "models/gemini-a" || body.Models[1].Name != "models/gemini-b" ||
		len(body.Models[0].Methods) != 1 || body.Models[0].Methods[0] != "generateContent" {
		t.Fatalf("body = %+v", body)
	}
}

func TestNativeProtocolsRejectAmbiguousKeysWithTheirOwnErrorEnvelope(t *testing.T) {
	handler := mustHandler(t, executorFunc(func(context.Context, gateway.Input, gateway.StreamSink) (gateway.Result, error) {
		t.Fatal("executor called")
		return gateway.Result{}, nil
	}), &fakeModels{})

	anthropicRequest := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude"}`))
	anthropicRequest.Header.Set("Content-Type", "application/json")
	anthropicRequest.Header.Set("x-api-key", "js_one")
	anthropicRequest.Header.Set("Authorization", "Bearer js_two")
	anthropicResponse := httptest.NewRecorder()
	handler.ServeHTTP(anthropicResponse, anthropicRequest)
	assertAnthropicError(t, anthropicResponse, http.StatusUnauthorized, "authentication_error")

	geminiRequest := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini:generateContent", strings.NewReader(`{"contents":[]}`))
	geminiRequest.Header.Set("Content-Type", "application/json")
	geminiResponse := httptest.NewRecorder()
	handler.ServeHTTP(geminiResponse, geminiRequest)
	assertGeminiError(t, geminiResponse, http.StatusUnauthorized, "UNAUTHENTICATED")
}

func TestAccountingAdmissionErrorsAreStableAndProtocolNative(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		auth      func(*http.Request)
		err       error
		status    int
		assertErr func(*testing.T, *httptest.ResponseRecorder)
	}{
		{
			name: "OpenAI quota", path: "/v1/chat/completions",
			auth: func(request *http.Request) { request.Header.Set("Authorization", "Bearer js_quota") },
			err:  gateway.ErrQuotaExceeded, status: http.StatusTooManyRequests,
			assertErr: func(t *testing.T, response *httptest.ResponseRecorder) {
				assertAPIError(t, response, http.StatusTooManyRequests, "insufficient_quota")
			},
		},
		{
			name: "Anthropic pricing", path: "/v1/messages",
			auth: func(request *http.Request) { request.Header.Set("x-api-key", "js_pricing") },
			err:  gateway.ErrPricingUnavailable, status: http.StatusServiceUnavailable,
			assertErr: func(t *testing.T, response *httptest.ResponseRecorder) {
				assertAnthropicError(t, response, http.StatusServiceUnavailable, "api_error")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := mustHandler(t, executorFunc(func(context.Context, gateway.Input, gateway.StreamSink) (gateway.Result, error) {
				return gateway.Result{}, test.err
			}), &fakeModels{})
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(`{"model":"public"}`))
			request.Header.Set("Content-Type", "application/json")
			test.auth(request)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.status, response.Body.String())
			}
			test.assertErr(t, response)
		})
	}
}

func TestChatCompletionsStreamsOnlyWhatTheGatewayCommits(t *testing.T) {
	executor := executorFunc(func(_ context.Context, input gateway.Input, sink gateway.StreamSink) (gateway.Result, error) {
		if !input.Stream || sink == nil {
			t.Fatalf("stream input = %+v sink=%v", input, sink)
		}
		if err := sink.Commit(http.Header{"Content-Type": []string{"text/event-stream"}}); err != nil {
			return gateway.Result{}, err
		}
		if err := sink.Write([]byte("data: {\"delta\":\"hello\"}\n\n")); err != nil {
			return gateway.Result{}, err
		}
		if err := sink.Write([]byte("data: [DONE]\n\n")); err != nil {
			return gateway.Result{}, err
		}
		return gateway.Result{RequestID: input.RequestID, Stream: true}, nil
	})
	handler := mustHandler(t, executor, &fakeModels{})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"public-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`,
	))
	request.Header.Set("Authorization", "Bearer js_stream")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" ||
		!strings.Contains(response.Body.String(), "hello") || !strings.Contains(response.Body.String(), "[DONE]") {
		t.Fatalf("stream response = %d %v %q", response.Code, response.Header(), response.Body.String())
	}
}

func TestCommittedStreamFailureDoesNotAppendAJSONErrorEnvelope(t *testing.T) {
	executor := executorFunc(func(_ context.Context, _ gateway.Input, sink gateway.StreamSink) (gateway.Result, error) {
		if err := sink.Commit(http.Header{"Content-Type": []string{"text/event-stream"}}); err != nil {
			return gateway.Result{}, err
		}
		_ = sink.Write([]byte("data: partial\n\n"))
		return gateway.Result{}, gateway.ErrCommittedStreamFailed
	})
	handler := mustHandler(t, executor, &fakeModels{})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"public","stream":true}`))
	request.Header.Set("Authorization", "Bearer js_stream")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "data: partial\n\n" {
		t.Fatalf("committed failure response = %d %q", response.Code, response.Body.String())
	}
}

func TestChatCompletionsRejectsAmbiguousOrMalformedRequests(t *testing.T) {
	handler := mustHandler(t, executorFunc(func(context.Context, gateway.Input, gateway.StreamSink) (gateway.Result, error) {
		t.Fatal("executor called")
		return gateway.Result{}, nil
	}), &fakeModels{})
	tests := []struct {
		contentType string
		body        string
		status      int
		code        string
	}{
		{"text/plain", `{"model":"gpt"}`, http.StatusUnsupportedMediaType, "unsupported_media_type"},
		{"application/json", `{} `, http.StatusBadRequest, "invalid_request"},
		{"application/json", `{"model":"gpt"} {"model":"other"}`, http.StatusBadRequest, "invalid_request"},
	}
	for _, test := range tests {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(test.body))
		request.Header.Set("Authorization", "Bearer js_invalid")
		request.Header.Set("Content-Type", test.contentType)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assertAPIError(t, response, test.status, test.code)
	}
}

type executorFunc func(context.Context, gateway.Input, gateway.StreamSink) (gateway.Result, error)

func (function executorFunc) Execute(ctx context.Context, input gateway.Input, sink gateway.StreamSink) (gateway.Result, error) {
	return function(ctx, input, sink)
}

type fakeModels struct {
	items []resolver.Model
	err   error
	key   string
	wire  protocol.Protocol
}

func (models *fakeModels) ListModels(_ context.Context, key string, wire protocol.Protocol) ([]resolver.Model, error) {
	models.key = key
	models.wire = wire
	return append([]resolver.Model(nil), models.items...), models.err
}

func mustHandler(t *testing.T, executor Executor, models ModelResolver) *Handler {
	t.Helper()
	handler, err := New(executor, models)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func assertAPIError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d: %s", response.Code, status, response.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != code {
		t.Fatalf("error code = %q, want %q", body.Error.Code, code)
	}
}

func assertAnthropicError(t *testing.T, response *httptest.ResponseRecorder, status int, errorType string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d: %s", response.Code, status, response.Body.String())
	}
	var body struct {
		Type  string `json:"type"`
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Type != "error" || body.Error.Type != errorType {
		t.Fatalf("Anthropic error = %+v", body)
	}
}

func assertGeminiError(t *testing.T, response *httptest.ResponseRecorder, status int, errorStatus string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d: %s", response.Code, status, response.Body.String())
	}
	var body struct {
		Error struct {
			Code   int    `json:"code"`
			Status string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != status || body.Error.Status != errorStatus {
		t.Fatalf("Gemini error = %+v", body)
	}
}
