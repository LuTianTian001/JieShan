package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
)

func TestResponsesAndChatCapabilitiesRegisterIndependently(t *testing.T) {
	doer := doerFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("unused") })
	chat, err := NewChatCompletionsAdapter(doer)
	if err != nil {
		t.Fatal(err)
	}
	responses, err := NewResponsesAdapter(doer)
	if err != nil {
		t.Fatal(err)
	}
	registry := vnextprotocol.NewRegistry()
	if _, err := registry.Register(vnextprotocol.OpenAI, vnextprotocol.OpenAIChatCompletions, chat); err != nil {
		t.Fatal(err)
	}
	responsesBefore, err := registry.Lookup(vnextprotocol.OpenAI, vnextprotocol.OpenAIResponses)
	if err != nil {
		t.Fatal(err)
	}
	if responsesBefore.Routable() || responsesBefore.Capabilities != (vnextprotocol.Capabilities{}) {
		t.Fatalf("Chat registration leaked capabilities into Responses: %+v", responsesBefore)
	}
	registered, err := registry.Register(vnextprotocol.OpenAI, vnextprotocol.OpenAIResponses, responses)
	if err != nil {
		t.Fatal(err)
	}
	if !registered.Routable() {
		t.Fatalf("Responses adapter is not independently routable: %+v", registered)
	}
	chatComponents, err := registry.Components(vnextprotocol.OpenAI, vnextprotocol.OpenAIChatCompletions)
	if err != nil {
		t.Fatal(err)
	}
	responsesComponents, err := registry.Components(vnextprotocol.OpenAI, vnextprotocol.OpenAIResponses)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := chatComponents.RequestEncoder.(*ChatCompletionsAdapter); !ok {
		t.Fatalf("Chat components use the wrong adapter: %T", chatComponents.RequestEncoder)
	}
	if _, ok := responsesComponents.RequestEncoder.(*ResponsesAdapter); !ok {
		t.Fatalf("Responses components use the wrong adapter: %T", responsesComponents.RequestEncoder)
	}

	baseInput := vnextprotocol.RequestBuildInput{
		Protocol: vnextprotocol.OpenAI,
		BaseURL:  "https://api.example.test/v1",
		Model:    "source-model",
		Payload:  []byte(`{"input":"hello"}`),
		Auth:     vnextprotocol.AuthInput{Scheme: vnextprotocol.AuthBearer, Secret: "secret"},
	}
	chatInput := baseInput
	chatInput.Surface = vnextprotocol.OpenAIChatCompletions
	responsesInput := baseInput
	responsesInput.Surface = vnextprotocol.OpenAIResponses
	if _, err := chat.EncodeRequest(t.Context(), responsesInput); err == nil {
		t.Fatal("Chat adapter accepted the Responses surface")
	}
	if _, err := responses.EncodeRequest(t.Context(), chatInput); err == nil {
		t.Fatal("Responses adapter accepted the Chat surface")
	}
}

func TestResponsesDiscoveryUsesModelsEndpointAndExplicitAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/prefix/v1/models" {
			t.Errorf("unexpected discovery request %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("x-api-key") != "responses-discovery-key" {
			t.Errorf("x-api-key = %q", request.Header.Get("x-api-key"))
		}
		_, _ = writer.Write([]byte(`{"data":[{"id":"gpt-responses"}]}`))
	}))
	defer server.Close()
	adapter, err := NewResponsesAdapter(server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.DiscoverModels(t.Context(), vnextprotocol.DiscoveryInput{
		BaseURL: server.URL + "/prefix/v1/responses",
		Auth:    vnextprotocol.AuthInput{Scheme: vnextprotocol.AuthXAPIKey, Secret: "responses-discovery-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Models, []string{"gpt-responses"}) {
		t.Fatalf("models = %#v", result.Models)
	}
}

func TestEncodeResponsesRequestRewritesOnlyItsSourceModel(t *testing.T) {
	adapter, _ := NewResponsesAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	encoded, err := adapter.EncodeRequest(t.Context(), vnextprotocol.RequestBuildInput{
		Protocol: vnextprotocol.OpenAI,
		Surface:  vnextprotocol.OpenAIResponses,
		BaseURL:  "https://relay.example.test/root/v1/chat/completions",
		Model:    "responses-source-model",
		Payload:  []byte(`{"model":"public-model","input":"public-model in user input","stream":true}`),
		Auth:     vnextprotocol.AuthInput{Scheme: vnextprotocol.AuthBearer, Secret: "responses-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if encoded.Method != http.MethodPost || encoded.URL != "https://relay.example.test/root/v1/responses" {
		t.Fatalf("unexpected encoded request: %s %s", encoded.Method, encoded.URL)
	}
	if encoded.Header.Get("Authorization") != "Bearer responses-key" || encoded.Header.Get("Accept") != "text/event-stream" {
		t.Fatalf("unexpected Responses headers: %#v", encoded.Header)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(encoded.Body, &payload); err != nil {
		t.Fatal(err)
	}
	var model, input string
	if err := json.Unmarshal(payload["model"], &model); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload["input"], &input); err != nil {
		t.Fatal(err)
	}
	if model != "responses-source-model" || input != "public-model in user input" {
		t.Fatalf("Responses payload was not structurally rewritten: %s", encoded.Body)
	}
	if _, exists := payload["stream_options"]; exists {
		t.Fatalf("Responses adapter injected Chat-only stream_options: %s", encoded.Body)
	}
}

func TestDecodeResponsesResponseAndUsage(t *testing.T) {
	adapter, _ := NewResponsesAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	body := completedResponsesBody("responses-source-model", "hello")
	decoded, err := adapter.DecodeResponse(t.Context(), vnextprotocol.ResponseInput{StatusCode: http.StatusOK, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Model != "responses-source-model" || !bytes.Equal(decoded.Body, body) {
		t.Fatalf("unexpected decoded response: %+v", decoded)
	}
	usage, err := adapter.ExtractUsage(t.Context(), vnextprotocol.UsageInput{Body: body})
	if err != nil {
		t.Fatal(err)
	}
	assertUsage(t, usage, 70, 25, 20, 10, 5)
}

func TestDecodeResponsesResponseRejectsInvalidTerminalObjects(t *testing.T) {
	adapter, _ := NewResponsesAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	secret := "sk-response-body-secret"
	tests := []struct {
		name   string
		body   string
		want   string
		is     error
		secret bool
	}{
		{name: "object", body: `{"object":"chat.completion","status":"completed","model":"m","output":[{"type":"message"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`, want: "object type"},
		{name: "failed", body: `{"object":"response","status":"failed","model":"m","error":{"code":"server_error","message":"` + secret + `"},"output":[],"usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1}}`, is: ErrResponseFailed, secret: true},
		{name: "incomplete", body: `{"object":"response","status":"incomplete","model":"m","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`, is: ErrResponseIncomplete},
		{name: "output", body: `{"object":"response","status":"completed","model":"m","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`, want: "output"},
		{name: "output item", body: `{"object":"response","status":"completed","model":"m","output":[{}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`, want: "output item"},
		{name: "queued", body: `{"object":"response","status":"queued","model":"m","output":[{"type":"message"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`, want: "completed status"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := adapter.DecodeResponse(t.Context(), vnextprotocol.ResponseInput{StatusCode: http.StatusOK, Body: []byte(test.body)})
			if err == nil {
				t.Fatal("invalid Responses object was accepted")
			}
			if test.is != nil && !errors.Is(err, test.is) {
				t.Fatalf("DecodeResponse() error = %v", err)
			}
			if test.want != "" && !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("DecodeResponse() error = %v", err)
			}
			if test.secret && strings.Contains(err.Error(), secret) {
				t.Fatalf("DecodeResponse() leaked secret: %v", err)
			}
		})
	}
}

func TestDecodeResponsesResponseAllowsMissingUsageButExtractionFails(t *testing.T) {
	adapter, _ := NewResponsesAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	body := []byte(`{"object":"response","status":"completed","model":"m","output":[{"type":"message"}]}`)
	if _, err := adapter.DecodeResponse(t.Context(), vnextprotocol.ResponseInput{StatusCode: http.StatusOK, Body: body}); err != nil {
		t.Fatalf("DecodeResponse() error = %v", err)
	}
	if usage, err := adapter.ExtractUsage(t.Context(), vnextprotocol.UsageInput{Body: body}); err == nil {
		t.Fatalf("ExtractUsage() usage = %+v, want missing usage error", usage)
	}
}

func TestDecodeResponsesStreamCompletesOnlyOnResponseCompleted(t *testing.T) {
	adapter, _ := NewResponsesAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	completed := string(completedResponsesBody("responses-source-model", "hello"))
	stream := strings.Join([]string{
		"data: {\"type\":\"response.created\",\"response\":{\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"responses-source-model\"}}\n\n",
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hel\"}\n\n",
		"data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{\\\"city\\\":\"}\n\n",
		"data: {\"type\":\"response.completed\",\"response\":" + completed + "}\n\n",
	}, "")
	events := make([]vnextprotocol.StreamEvent, 0, 4)
	result, err := adapter.DecodeStream(t.Context(), vnextprotocol.StreamInput{StatusCode: http.StatusOK, Body: strings.NewReader(stream)}, func(event vnextprotocol.StreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Terminal || result.Model != "responses-source-model" {
		t.Fatalf("unexpected stream result: %+v", result)
	}
	if len(events) != 4 || events[0].Terminal || !events[1].Semantic || !events[2].Semantic || !events[3].Terminal {
		t.Fatalf("unexpected Responses events: %+v", events)
	}
	usage, err := adapter.ExtractUsage(t.Context(), vnextprotocol.UsageInput{Events: events})
	if err != nil {
		t.Fatal(err)
	}
	assertUsage(t, usage, 70, 25, 20, 10, 5)
}

func TestResponsesStreamEOFWithoutCompletedIsTruncated(t *testing.T) {
	adapter, _ := NewResponsesAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	stream := "data: {\"type\":\"response.output_text.done\",\"text\":\"hello\"}\n\n"
	result, err := adapter.DecodeStream(t.Context(), vnextprotocol.StreamInput{StatusCode: http.StatusOK, Body: strings.NewReader(stream)}, nil)
	if !errors.Is(err, ErrStreamTruncated) {
		t.Fatalf("DecodeStream() error = %v", err)
	}
	if result.Terminal {
		t.Fatalf("non-completed event became terminal: %+v", result)
	}
}

func TestResponsesStreamRejectsChatDoneMarker(t *testing.T) {
	adapter, _ := NewResponsesAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	_, err := adapter.DecodeStream(t.Context(), vnextprotocol.StreamInput{StatusCode: http.StatusOK, Body: strings.NewReader("data: [DONE]\n\n")}, nil)
	if !errors.Is(err, ErrInvalidResponsesTerminator) {
		t.Fatalf("DecodeStream() error = %v", err)
	}
}

func TestResponsesStreamFailedAndIncompleteAreErrors(t *testing.T) {
	adapter, _ := NewResponsesAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	secret := "sk-stream-secret"
	tests := []struct {
		name string
		body string
		is   error
	}{
		{
			name: "failed",
			body: `data: {"type":"response.failed","response":{"object":"response","status":"failed","model":"m","error":{"code":"server_error","message":"` + secret + `"}}}` + "\n\n",
			is:   ErrResponseFailed,
		},
		{
			name: "incomplete",
			body: `data: {"type":"response.incomplete","response":{"object":"response","status":"incomplete","model":"m","incomplete_details":{"reason":"max_output_tokens"}}}` + "\n\n",
			is:   ErrResponseIncomplete,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := adapter.DecodeStream(t.Context(), vnextprotocol.StreamInput{StatusCode: http.StatusOK, Body: strings.NewReader(test.body)}, nil)
			if !errors.Is(err, test.is) {
				t.Fatalf("DecodeStream() error = %v", err)
			}
			if test.name == "failed" && !strings.Contains(err.Error(), "upstream_transient") {
				t.Fatalf("failed event was not safely classified: %v", err)
			}
			if result.Terminal || strings.Contains(err.Error(), secret) {
				t.Fatalf("unsafe terminal error result=%+v err=%v", result, err)
			}
		})
	}
}

func TestResponsesStreamErrorEventUsesControlledClassification(t *testing.T) {
	adapter, _ := NewResponsesAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	secret := "sk-error-event-secret"
	stream := `data: {"type":"error","code":"rate_limit_exceeded","message":"` + secret + `","param":null}` + "\n\n"
	_, err := adapter.DecodeStream(t.Context(), vnextprotocol.StreamInput{StatusCode: http.StatusOK, Body: strings.NewReader(stream)}, nil)
	if err == nil || !strings.Contains(err.Error(), "credential_rate_limited") {
		t.Fatalf("DecodeStream() error = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Responses error event leaked secret: %v", err)
	}
}

func TestResponsesErrorDecoderIsControlledAndIndependent(t *testing.T) {
	adapter, _ := NewResponsesAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	secret := "sk-responses-error-secret"
	decoded, err := adapter.DecodeError(t.Context(), vnextprotocol.ErrorInput{
		StatusCode: http.StatusTooManyRequests,
		Body:       []byte(`{"error":{"code":"rate_limit_exceeded","message":"` + secret + `"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Class != "credential_rate_limited" || !decoded.Retryable || !decoded.CredentialFailure {
		t.Fatalf("decoded error = %+v", decoded)
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("Responses error decoder leaked secret: %s", encoded)
	}
}

func TestResponsesBodyAndEventLimitsRemainEnforced(t *testing.T) {
	adapter, _ := NewResponsesAdapter(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }))
	oversized := bytes.Repeat([]byte("x"), maxBodyBytes+1)
	if _, err := adapter.DecodeResponse(t.Context(), vnextprotocol.ResponseInput{StatusCode: http.StatusOK, Body: oversized}); !errors.Is(err, errBodyTooLarge) {
		t.Fatalf("DecodeResponse() error = %v", err)
	}
	stream := "data: " + strings.Repeat("x", maxEventBytes) + "\n\n"
	if _, err := adapter.DecodeStream(t.Context(), vnextprotocol.StreamInput{StatusCode: http.StatusOK, Body: strings.NewReader(stream)}, nil); !errors.Is(err, errEventTooLarge) {
		t.Fatalf("DecodeStream() error = %v", err)
	}
}

func completedResponsesBody(model, text string) []byte {
	payload := map[string]any{
		"id":     "resp_1",
		"object": "response",
		"status": "completed",
		"model":  model,
		"output": []any{map[string]any{
			"id":   "msg_1",
			"type": "message",
			"role": "assistant",
			"content": []any{map[string]any{
				"type": "output_text",
				"text": text,
			}},
		}},
		"usage": map[string]any{
			"input_tokens":  100,
			"output_tokens": 30,
			"total_tokens":  130,
			"input_tokens_details": map[string]any{
				"cached_tokens":      20,
				"cache_write_tokens": 10,
			},
			"output_tokens_details": map[string]any{
				"reasoning_tokens": 5,
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return body
}
