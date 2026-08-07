package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

func TestCompleteInferenceWithoutUsageNeverReplaysAcrossProtocolSurfaces(t *testing.T) {
	for _, test := range streamSurfaceCases() {
		t.Run(test.name, func(t *testing.T) {
			t.Run("non-stream", func(t *testing.T) {
				doer := &scriptedDoer{scripts: []responseScript{
					{status: http.StatusOK, body: missingUsageResponse(test.surface)},
					{status: http.StatusOK, body: `{"unexpected":"replay"}`},
				}}
				service, health := newStreamSurfaceFixture(t, doer, test)
				accounting := &fakeAccounting{}
				service.accounting = accounting

				result, err := service.Execute(context.Background(), usageUnavailableInput(test, false), nil)
				if err != nil {
					t.Fatal(err)
				}
				assertUsageUnavailableSuccess(t, result, doer, health, accounting, nil)
			})

			t.Run("stream", func(t *testing.T) {
				doer := &scriptedDoer{scripts: []responseScript{
					{status: http.StatusOK, contentType: "text/event-stream", body: missingUsageStream(test.surface)},
					{status: http.StatusOK, contentType: "text/event-stream", body: test.success},
				}}
				service, health := newStreamSurfaceFixture(t, doer, test)
				accounting := &fakeAccounting{}
				service.accounting = accounting
				sink := &recordingSink{}

				result, err := service.Execute(context.Background(), usageUnavailableInput(test, true), sink)
				if err != nil {
					t.Fatal(err)
				}
				assertUsageUnavailableSuccess(t, result, doer, health, accounting, sink)
				if !strings.Contains(strings.Join(sink.events, ""), "visible") {
					t.Fatalf("semantic stream output was not delivered: %+v", sink.events)
				}
			})
		})
	}
}

func assertUsageUnavailableSuccess(
	t *testing.T,
	result Result,
	doer *scriptedDoer,
	health *fakeHealth,
	accounting *fakeAccounting,
	sink *recordingSink,
) {
	t.Helper()
	if len(doer.requests) != 1 || result.TargetID != 1 || result.StatusCode != http.StatusOK || len(result.Attempts) != 1 {
		t.Fatalf("result=%+v requests=%+v", result, doer.requests)
	}
	attempt := result.Attempts[0]
	if attempt.Outcome != "succeeded" || attempt.FailureKind != "" ||
		attempt.ErrorCode != "" || attempt.ErrorClass != "" || attempt.SwitchReason != "" {
		t.Fatalf("attempt=%+v", attempt)
	}
	if health.successCount(1) != 1 || health.failureCount(1) != 0 ||
		health.successCount(2) != 0 || health.failureCount(2) != 0 {
		t.Fatalf("health events=%+v", health.events)
	}
	if result.MeteringStatus != meteringUnavailable || result.MeteringErrorCode != meteringErrorUsageUnavailable {
		t.Fatalf("metering status=%q error=%q", result.MeteringStatus, result.MeteringErrorCode)
	}
	assertUsageUnknown(t, result.Usage)
	if result.OfficialCostNanoUSD != 0 || result.ChargedNanoUSD != 0 || result.ReservationNanoUSD <= 0 {
		t.Fatalf("accounting result=%+v", result)
	}
	if sink != nil && sink.commits != 1 {
		t.Fatalf("stream commits=%d events=%+v", sink.commits, sink.events)
	}

	accounting.mu.Lock()
	defer accounting.mu.Unlock()
	if len(accounting.starts) != 1 || len(accounting.attempts) != 1 || len(accounting.settlements) != 1 {
		t.Fatalf("accounting calls: starts=%d attempts=%d settlements=%d", len(accounting.starts), len(accounting.attempts), len(accounting.settlements))
	}
	if accounting.attempts[0].Status != "success" || accounting.attempts[0].FailureKind != "" ||
		accounting.attempts[0].ErrorCode != "" || accounting.attempts[0].SwitchReason != "" {
		t.Fatalf("persisted attempt=%+v", accounting.attempts[0])
	}
	settlement := accounting.settlements[0]
	if settlement.Status != "success" || settlement.MeteringStatus != meteringUnavailable ||
		settlement.MeteringErrorCode != meteringErrorUsageUnavailable || settlement.ErrorCode != "" ||
		settlement.OfficialCostNanoUSD != 0 {
		t.Fatalf("settlement=%+v", settlement)
	}
	assertSettlementUsageUnknown(t, settlement)
}

func assertUsageUnknown(t *testing.T, usage protocol.Usage) {
	t.Helper()
	if usage.InputTokens != nil || usage.OutputTokens != nil || usage.CacheReadTokens != nil ||
		usage.CacheWriteTokens != nil || usage.CacheWrite5MTokens != nil || usage.CacheWrite1HTokens != nil ||
		usage.ReasoningTokens != nil {
		t.Fatalf("unknown usage was populated: %+v", usage)
	}
}

func assertSettlementUsageUnknown(t *testing.T, settlement vnextstore.RequestSettlement) {
	t.Helper()
	values := []*int64{
		settlement.InputTokens, settlement.OutputTokens, settlement.CacheReadTokens,
		settlement.CacheWriteTokens, settlement.CacheWrite5MTokens, settlement.CacheWrite1HTokens,
		settlement.ReasoningTokens,
	}
	for _, value := range values {
		if value != nil {
			t.Fatalf("unavailable metering persisted token values: %+v", settlement)
		}
	}
}

func usageUnavailableInput(test streamSurfaceCase, stream bool) Input {
	return Input{
		RequestID:     "req-usage-unavailable-" + test.name + "-" + map[bool]string{false: "response", true: "stream"}[stream],
		DownstreamKey: "js_test", PublicModel: "public-model",
		IngressProtocol: test.wire, IngressSurface: test.surface,
		Payload: missingUsagePayload(test.surface, stream), Stream: stream,
	}
}

func missingUsagePayload(surface protocol.Surface, stream bool) []byte {
	streamValue := "false"
	if stream {
		streamValue = "true"
	}
	switch surface {
	case protocol.OpenAIChatCompletions:
		return []byte(`{"model":"public-model","stream":` + streamValue + `,"max_tokens":8,"messages":[{"role":"user","content":"hello"}]}`)
	case protocol.OpenAIResponses:
		return []byte(`{"model":"public-model","stream":` + streamValue + `,"max_output_tokens":8,"input":"hello"}`)
	case protocol.AnthropicMessages:
		return []byte(`{"model":"public-model","stream":` + streamValue + `,"max_tokens":8,"messages":[{"role":"user","content":"hello"}]}`)
	case protocol.GeminiGenerateContent:
		return []byte(`{"stream":` + streamValue + `,"generationConfig":{"maxOutputTokens":8},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
	default:
		panic("unsupported protocol surface")
	}
}

func missingUsageResponse(surface protocol.Surface) string {
	switch surface {
	case protocol.OpenAIChatCompletions:
		return `{"id":"chatcmpl_missing_usage","object":"chat.completion","model":"source-a","choices":[{"index":0,"message":{"role":"assistant","content":"visible"},"finish_reason":"stop"}]}`
	case protocol.OpenAIResponses:
		return `{"id":"resp_missing_usage","object":"response","status":"completed","model":"source-a","output":[{"type":"message"}]}`
	case protocol.AnthropicMessages:
		return `{"id":"msg_missing_usage","type":"message","role":"assistant","model":"source-a","content":[{"type":"text","text":"visible"}],"stop_reason":"end_turn"}`
	case protocol.GeminiGenerateContent:
		return `{"candidates":[{"content":{"role":"model","parts":[{"text":"visible"}]},"finishReason":"STOP"}],"modelVersion":"source-a"}`
	default:
		panic("unsupported protocol surface")
	}
}

func missingUsageStream(surface protocol.Surface) string {
	switch surface {
	case protocol.OpenAIChatCompletions:
		return strings.Join([]string{
			"data: {\"model\":\"source-a\",\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n",
			"data: {\"model\":\"source-a\",\"choices\":[{\"delta\":{\"content\":\"visible\"}}]}\n\n",
			"data: [DONE]\n\n",
		}, "")
	case protocol.OpenAIResponses:
		return strings.Join([]string{
			"data: {\"type\":\"response.created\",\"response\":{\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"source-a\"}}\n\n",
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"visible\"}\n\n",
			"data: {\"type\":\"response.completed\",\"response\":" + missingUsageResponse(surface) + "}\n\n",
		}, "")
	case protocol.AnthropicMessages:
		return strings.Join([]string{
			anthropicSSE("message_start", `{"type":"message_start","message":{"id":"msg_a","type":"message","role":"assistant","model":"source-a","content":[]}}`),
			anthropicSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
			anthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"visible"}}`),
			anthropicSSE("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`),
			anthropicSSE("message_stop", `{"type":"message_stop"}`),
		}, "")
	case protocol.GeminiGenerateContent:
		return geminiSSE(`{"candidates":[{"content":{"role":"model","parts":[{"text":"visible"}]},"finishReason":"STOP"}],"modelVersion":"source-a"}`)
	default:
		panic("unsupported protocol surface")
	}
}
