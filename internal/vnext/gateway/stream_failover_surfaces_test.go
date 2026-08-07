package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol/anthropic"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol/gemini"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol/openai"
	"github.com/LuTianTian001/JieShan/internal/vnext/resolver"
	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
)

type streamSurfaceCase struct {
	name              string
	wire              protocol.Protocol
	surface           protocol.Surface
	authScheme        protocol.AuthScheme
	payload           string
	beforeSemantic    string
	afterSemantic     string
	success           string
	firstOutputMarker string
	successMarker     string
}

func TestStreamFailoverBoundaryAcrossProtocolSurfaces(t *testing.T) {
	for _, test := range streamSurfaceCases() {
		t.Run(test.name, func(t *testing.T) {
			t.Run("failure before semantic output fails over without leaking", func(t *testing.T) {
				doer := &scriptedDoer{scripts: []responseScript{
					{status: http.StatusOK, contentType: "text/event-stream", body: test.beforeSemantic},
					{status: http.StatusOK, contentType: "text/event-stream", body: test.success},
				}}
				service, health := newStreamSurfaceFixture(t, doer, test)
				sink := &recordingSink{}

				result, err := service.Execute(context.Background(), streamInput(test, "precommit"), sink)
				if err != nil {
					t.Fatal(err)
				}
				if len(doer.requests) != 2 || sink.commits != 1 || result.TargetID != 2 || len(result.Attempts) != 2 {
					t.Fatalf("result=%+v requests=%+v sink=%+v", result, doer.requests, sink)
				}
				emitted := strings.Join(sink.events, "")
				if strings.Contains(emitted, "source-a") || !strings.Contains(emitted, test.successMarker) {
					t.Fatalf("precommit attempt leaked or recovery was missing: %q", emitted)
				}
				first := result.Attempts[0]
				if first.FailureKind != routing.FailureStreamTruncated || first.ResponseCommitted ||
					first.SwitchReason != string(routing.RetryNextTarget) {
					t.Fatalf("first attempt=%+v", first)
				}
				if health.failureCount(1) != 1 || health.successCount(2) != 1 {
					t.Fatalf("health events=%+v", health.events)
				}
			})

			t.Run("failure after semantic output never replays", func(t *testing.T) {
				doer := &scriptedDoer{scripts: []responseScript{
					{status: http.StatusOK, contentType: "text/event-stream", body: test.afterSemantic},
					{status: http.StatusOK, contentType: "text/event-stream", body: test.success},
				}}
				service, health := newStreamSurfaceFixture(t, doer, test)
				sink := &recordingSink{}

				result, err := service.Execute(context.Background(), streamInput(test, "committed"), sink)
				if !errors.Is(err, ErrCommittedStreamFailed) {
					t.Fatalf("error=%v", err)
				}
				if len(doer.requests) != 1 || sink.commits != 1 || len(result.Attempts) != 1 {
					t.Fatalf("result=%+v requests=%+v sink=%+v", result, doer.requests, sink)
				}
				if !strings.Contains(strings.Join(sink.events, ""), test.firstOutputMarker) {
					t.Fatalf("semantic output was not emitted: %+v", sink.events)
				}
				first := result.Attempts[0]
				if !first.ResponseCommitted || first.FailureKind != routing.FailureStreamTruncated || first.SwitchReason != "" {
					t.Fatalf("committed attempt=%+v", first)
				}
				if health.failureCount(1) != 1 || health.successCount(2) != 0 {
					t.Fatalf("health events=%+v", health.events)
				}
			})
		})
	}
}

func TestCommittedSinkWriteFailureIsRecordedAndNeverReplayed(t *testing.T) {
	test := streamSurfaceCases()[0]
	doer := &scriptedDoer{scripts: []responseScript{
		{status: http.StatusOK, contentType: "text/event-stream", body: test.success},
		{status: http.StatusOK, contentType: "text/event-stream", body: test.success},
	}}
	service, health := newStreamSurfaceFixture(t, doer, test)
	sink := &writeFailingSink{}

	result, err := service.Execute(context.Background(), streamInput(test, "write-failure"), sink)
	if !errors.Is(err, ErrDownstreamClosed) {
		t.Fatalf("error=%v", err)
	}
	if len(doer.requests) != 1 || sink.commits != 1 || sink.writes != 1 || len(result.Attempts) != 1 {
		t.Fatalf("result=%+v requests=%+v sink=%+v", result, doer.requests, sink)
	}
	first := result.Attempts[0]
	if !first.ResponseCommitted || first.FailureKind != routing.FailureDownstreamCanceled || first.Outcome != "cancelled" {
		t.Fatalf("attempt=%+v", first)
	}
	if health.failureCount(1) != 0 || health.successCount(2) != 0 {
		t.Fatalf("downstream failure changed target health: %+v", health.events)
	}
}

func streamSurfaceCases() []streamSurfaceCase {
	responsesCompleted := `{"id":"resp_1","object":"response","status":"completed","model":"source-b","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"second-visible"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}`
	return []streamSurfaceCase{
		{
			name: "openai_chat", wire: protocol.OpenAI, surface: protocol.OpenAIChatCompletions,
			authScheme: protocol.AuthBearer,
			payload:    `{"model":"public-model","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"hello"}]}`,
			beforeSemantic: `data: {"model":"source-a","choices":[{"delta":{"role":"assistant"}}]}

`,
			afterSemantic: `data: {"model":"source-a","choices":[{"delta":{"content":"first-visible"}}]}

`,
			success: strings.Join([]string{
				"data: {\"model\":\"source-b\",\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n",
				"data: {\"model\":\"source-b\",\"choices\":[{\"delta\":{\"content\":\"second-visible\"}}]}\n\n",
				"data: {\"model\":\"source-b\",\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\n",
				"data: [DONE]\n\n",
			}, ""),
			firstOutputMarker: "first-visible", successMarker: "second-visible",
		},
		{
			name: "openai_responses", wire: protocol.OpenAI, surface: protocol.OpenAIResponses,
			authScheme: protocol.AuthBearer,
			payload:    `{"model":"public-model","stream":true,"max_output_tokens":8,"input":"hello"}`,
			beforeSemantic: `data: {"type":"response.created","response":{"object":"response","status":"in_progress","model":"source-a"}}

`,
			afterSemantic: `data: {"type":"response.output_text.delta","delta":"first-visible"}

`,
			success: strings.Join([]string{
				"data: {\"type\":\"response.created\",\"response\":{\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"source-b\"}}\n\n",
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"second-visible\"}\n\n",
				"data: {\"type\":\"response.completed\",\"response\":" + responsesCompleted + "}\n\n",
			}, ""),
			firstOutputMarker: "first-visible", successMarker: "second-visible",
		},
		{
			name: "anthropic_messages", wire: protocol.Anthropic, surface: protocol.AnthropicMessages,
			authScheme:     protocol.AuthXAPIKey,
			payload:        `{"model":"public-model","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"hello"}]}`,
			beforeSemantic: anthropicSSE("message_start", `{"type":"message_start","message":{"id":"msg_a","type":"message","role":"assistant","model":"source-a","content":[],"usage":{"input_tokens":2,"output_tokens":0}}}`),
			afterSemantic: strings.Join([]string{
				anthropicSSE("message_start", `{"type":"message_start","message":{"id":"msg_a","type":"message","role":"assistant","model":"source-a","content":[],"usage":{"input_tokens":2,"output_tokens":0}}}`),
				anthropicSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
				anthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"first-visible"}}`),
			}, ""),
			success: strings.Join([]string{
				anthropicSSE("message_start", `{"type":"message_start","message":{"id":"msg_b","type":"message","role":"assistant","model":"source-b","content":[],"usage":{"input_tokens":2,"output_tokens":0}}}`),
				anthropicSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
				anthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"second-visible"}}`),
				anthropicSSE("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`),
				anthropicSSE("message_stop", `{"type":"message_stop"}`),
			}, ""),
			firstOutputMarker: "first-visible", successMarker: "second-visible",
		},
		{
			name: "gemini_native", wire: protocol.Gemini, surface: protocol.GeminiGenerateContent,
			authScheme:     protocol.AuthXGoogAPIKey,
			payload:        `{"stream":true,"generationConfig":{"maxOutputTokens":8},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`,
			beforeSemantic: geminiSSE(`{"modelVersion":"source-a"}`),
			afterSemantic:  geminiSSE(`{"candidates":[{"content":{"role":"model","parts":[{"text":"first-visible"}]}}],"modelVersion":"source-a"}`),
			success: strings.Join([]string{
				geminiSSE(`{"candidates":[{"content":{"role":"model","parts":[{"text":"second-visible"}]}}],"modelVersion":"source-b"}`),
				geminiSSE(`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"cachedContentTokenCount":0,"candidatesTokenCount":1,"toolUsePromptTokenCount":0,"thoughtsTokenCount":0,"totalTokenCount":3},"modelVersion":"source-b"}`),
			}, ""),
			firstOutputMarker: "first-visible", successMarker: "second-visible",
		},
	}
}

func newStreamSurfaceFixture(t *testing.T, doer *scriptedDoer, test streamSurfaceCase) (*Service, *fakeHealth) {
	t.Helper()
	registry := protocol.NewRegistry()
	var adapter any
	var err error
	switch test.surface {
	case protocol.OpenAIChatCompletions:
		adapter, err = openai.NewChatCompletionsAdapter(doer)
	case protocol.OpenAIResponses:
		adapter, err = openai.NewResponsesAdapter(doer)
	case protocol.AnthropicMessages:
		adapter, err = anthropic.NewMessagesAdapter(doer)
	case protocol.GeminiGenerateContent:
		adapter, err = gemini.NewGenerateContentAdapter(doer)
	default:
		t.Fatalf("unsupported test surface %q", test.surface)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Register(test.wire, test.surface, adapter); err != nil {
		t.Fatal(err)
	}
	plan, err := routing.CompilePlan([]routing.Target{
		{ID: 1, Revision: 1, Position: 0, Enabled: true, Credentials: []routing.Credential{{ID: 11, Position: 0, Enabled: true}}},
		{ID: 2, Revision: 1, Position: 1, Enabled: true, Credentials: []routing.Credential{{ID: 21, Position: 0, Enabled: true}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolution := resolver.Resolution{
		DownstreamKeyID:  7,
		PublishedModelID: 70, PublishedModelRevision: 3,
		RoutingProfileID: 9, RoutingProfileName: "Strict order",
		SourceProfileID: 1, SourceProfileName: "Default", RouteRevision: 3,
		PublicModel: "public-model", OfficialPriceSKU: "public-model", Plan: plan,
		Endpoints: map[routing.TargetID]resolver.EndpointMetadata{
			1: streamEndpointMetadata(1, 701, 101, 1001, 11, "A", "source-a", test),
			2: streamEndpointMetadata(2, 702, 102, 1002, 21, "B", "source-b", test),
		},
		Health: map[routing.TargetID]routing.HealthState{},
	}
	health := newFakeHealth()
	service, err := New(
		exactSurfaceResolver{resolution: resolution, wire: test.wire, surface: test.surface},
		registry,
		health,
		staticSecrets{secrets: map[routing.CredentialID]string{11: "secret-11", 21: "secret-21"}},
		&fakeEffects{},
		&fakeAccounting{},
		newGatewayPriceBook(t),
		NewConservativeJSONReservationPlanner(),
		doer,
		Options{
			Now:                    monotonicClock(time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)),
			DefaultMaxOutputTokens: 128, Capacity: directCapacity{},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return service, health
}

func streamEndpointMetadata(
	targetID routing.TargetID,
	publishedTargetID, siteID, endpointID int64,
	credentialID routing.CredentialID,
	suffix, sourceModel string,
	test streamSurfaceCase,
) resolver.EndpointMetadata {
	return resolver.EndpointMetadata{
		TargetID: targetID, PublishedModelTargetID: publishedTargetID, PublishedModelTargetRevision: 1,
		SiteID: siteID, SiteName: "Site " + suffix,
		EndpointID: endpointID, EndpointName: "Endpoint " + suffix,
		BaseURL:  "https://" + strings.ToLower(suffix) + ".example/v1",
		Protocol: test.wire, Surface: test.surface, AuthScheme: test.authScheme, SourceModel: sourceModel,
		CredentialNames: map[routing.CredentialID]string{credentialID: "Key " + suffix},
	}
}

func streamInput(test streamSurfaceCase, suffix string) Input {
	return Input{
		RequestID:     "req-stream-surface-" + test.name + "-" + suffix,
		DownstreamKey: "js_test", PublicModel: "public-model",
		IngressProtocol: test.wire, IngressSurface: test.surface,
		Payload: []byte(test.payload), Stream: true,
	}
}

type exactSurfaceResolver struct {
	resolution resolver.Resolution
	wire       protocol.Protocol
	surface    protocol.Surface
}

func (item exactSurfaceResolver) Resolve(
	_ context.Context,
	rawKey, model string,
	wire protocol.Protocol,
	surface protocol.Surface,
) (resolver.Resolution, error) {
	if rawKey != "js_test" || model != item.resolution.PublicModel || wire != item.wire || surface != item.surface {
		return resolver.Resolution{}, resolver.ErrModelNotFound
	}
	return item.resolution, nil
}

type writeFailingSink struct {
	commits int
	writes  int
}

func (sink *writeFailingSink) Commit(http.Header) error {
	sink.commits++
	return nil
}

func (sink *writeFailingSink) Write([]byte) error {
	sink.writes++
	return errors.New("test downstream write failure")
}

func anthropicSSE(event, data string) string {
	return "event: " + event + "\n" + "data: " + data + "\n\n"
}

func geminiSSE(data string) string {
	return "data: " + data + "\n\n"
}
