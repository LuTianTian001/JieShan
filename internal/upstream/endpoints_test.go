package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"testing"

	"github.com/LuTianTian001/JieShan/internal/secrets"
	"github.com/LuTianTian001/JieShan/internal/store"
)

func TestEndpointCapabilities(t *testing.T) {
	for _, protocol := range []string{"openai", "compatible"} {
		for _, endpoint := range []EndpointCapability{EndpointModels, EndpointChatCompletions, EndpointResponses} {
			if !SupportsEndpoint(protocol, endpoint) {
				t.Errorf("SupportsEndpoint(%q, %q) = false", protocol, endpoint)
			}
		}
	}
	for _, protocol := range []string{"anthropic", "gemini"} {
		if !SupportsEndpoint(protocol, EndpointModels) {
			t.Errorf("SupportsEndpoint(%q, models) = false", protocol)
		}
		if SupportsEndpoint(protocol, EndpointChatCompletions) || SupportsEndpoint(protocol, EndpointResponses) {
			t.Errorf("protocol %q unexpectedly supports OpenAI inference surfaces", protocol)
		}
	}
	if SupportsEndpoint("unknown", EndpointModels) || SupportsEndpoint("openai", EndpointCapability("unknown")) {
		t.Fatal("unknown protocol or endpoint unexpectedly supported")
	}
}

func TestOpenAIEndpointURLs(t *testing.T) {
	tests := []struct {
		base          string
		wantChatPath  string
		wantReplyPath string
	}{
		{base: "https://api.example.com", wantChatPath: "/v1/chat/completions", wantReplyPath: "/v1/responses"},
		{base: "https://api.example.com/v1", wantChatPath: "/v1/chat/completions", wantReplyPath: "/v1/responses"},
		{base: "https://api.example.com/api/v1", wantChatPath: "/api/v1/chat/completions", wantReplyPath: "/api/v1/responses"},
	}
	for _, test := range tests {
		chat, err := chatURL(test.base)
		if err != nil {
			t.Fatal(err)
		}
		responses, err := responsesURL(test.base)
		if err != nil {
			t.Fatal(err)
		}
		if got := mustParseURL(t, chat).Path; got != test.wantChatPath {
			t.Errorf("chatURL(%q) path = %q, want %q", test.base, got, test.wantChatPath)
		}
		if got := mustParseURL(t, responses).Path; got != test.wantReplyPath {
			t.Errorf("responsesURL(%q) path = %q, want %q", test.base, got, test.wantReplyPath)
		}
	}
}

func TestBuildResponsesRequestPreservesPayloadAndAppliesRouting(t *testing.T) {
	cipher, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Encrypt("upstream-secret")
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{cipher: cipher}
	target := store.RouteTarget{
		UpstreamKind:  "compatible",
		UpstreamModel: "provider-model",
		BaseURL:       "https://relay.example/api/v1",
		SecretCipher:  encrypted,
		CustomHeaders: []byte(`{"X-Tenant":"tenant-a","Authorization":"must-not-override"}`),
	}
	body := []byte(`{"model":"public-model","input":"hello","stream":true,"reasoning":{"effort":"high"}}`)

	request, err := client.BuildResponsesRequest(context.Background(), target, body)
	if err != nil {
		t.Fatalf("BuildResponsesRequest() error = %v", err)
	}
	if request.Method != http.MethodPost || request.URL.Path != "/api/v1/responses" {
		t.Fatalf("request = %s %s", request.Method, request.URL.String())
	}
	if got := request.Header.Get("Authorization"); got != "Bearer upstream-secret" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := request.Header.Get("X-Tenant"); got != "tenant-a" {
		t.Fatalf("X-Tenant = %q", got)
	}
	raw, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["model"] != "provider-model" || payload["input"] != "hello" || payload["stream"] != true {
		t.Fatalf("payload = %#v", payload)
	}
	if !reflect.DeepEqual(payload["reasoning"], map[string]any{"effort": "high"}) {
		t.Fatalf("reasoning = %#v", payload["reasoning"])
	}
}

func TestBuildResponsesRequestRejectsUnsupportedProtocol(t *testing.T) {
	cipher, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{cipher: cipher}
	_, err = client.BuildResponsesRequest(context.Background(), store.RouteTarget{UpstreamKind: "anthropic"}, []byte(`{"model":"x"}`))
	if err == nil {
		t.Fatal("BuildResponsesRequest() error = nil")
	}
}
