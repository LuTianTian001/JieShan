package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/capacity"
	"github.com/LuTianTian001/JieShan/internal/vnext/pricing"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol/openai"
	"github.com/LuTianTian001/JieShan/internal/vnext/resolver"
	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

func TestExecuteRotatesCredentialThenMovesToTheNextOrderedTarget(t *testing.T) {
	doer := &scriptedDoer{scripts: []responseScript{
		{status: http.StatusUnauthorized, body: `{"error":{"code":"invalid_api_key"}}`},
		{status: http.StatusServiceUnavailable, body: `{"error":{"code":"service_unavailable"}}`},
		{status: http.StatusOK, body: chatResponse("source-b", "ok")},
	}}
	service, health, effects := newGatewayFixture(t, doer)

	result, err := service.Execute(context.Background(), Input{
		RequestID: "req-order", DownstreamKey: "js_test", PublicModel: "public-model",
		IngressProtocol: protocol.OpenAI, IngressSurface: protocol.OpenAIChatCompletions,
		Payload: []byte(`{"model":"public-model","messages":[{"role":"user","content":"hello"}]}`),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.TargetID != 2 || result.CredentialID != 21 || result.UpstreamModel != "source-b" {
		t.Fatalf("result target = %+v", result)
	}
	if len(result.Attempts) != 3 {
		t.Fatalf("attempts = %+v", result.Attempts)
	}
	want := []struct {
		target routing.TargetID
		key    routing.CredentialID
		kind   routing.FailureKind
	}{
		{target: 1, key: 11, kind: routing.FailureCredentialAuth},
		{target: 1, key: 12, kind: routing.FailureUpstreamTransient},
		{target: 2, key: 21},
	}
	for index, expected := range want {
		attempt := result.Attempts[index]
		if attempt.TargetID != expected.target || attempt.CredentialID != expected.key || attempt.FailureKind != expected.kind {
			t.Fatalf("attempt[%d] = %+v", index, attempt)
		}
	}
	if len(effects.events) != 1 || effects.events[0].CredentialID != 11 || effects.events[0].Effect != routing.CredentialEffectInvalidate {
		t.Fatalf("credential effects = %+v", effects.events)
	}
	if got := health.failureCount(1); got != 1 {
		t.Fatalf("target 1 health failures = %d, want 1", got)
	}
	if got := health.successCount(2); got != 1 {
		t.Fatalf("target 2 health successes = %d, want 1", got)
	}
	if len(doer.requests) != 3 {
		t.Fatalf("request count = %d", len(doer.requests))
	}
	if doer.requests[0].authorization != "Bearer secret-11" ||
		doer.requests[1].authorization != "Bearer secret-12" ||
		doer.requests[2].authorization != "Bearer secret-21" {
		t.Fatalf("authorization order = %+v", doer.requests)
	}
	if !strings.HasSuffix(doer.requests[0].url, "/v1/chat/completions") ||
		!strings.Contains(doer.requests[0].body, `"model":"source-a"`) ||
		!strings.Contains(doer.requests[2].body, `"model":"source-b"`) {
		t.Fatalf("rewritten requests = %+v", doer.requests)
	}
}

func TestStreamTruncationBeforeSemanticOutputFailsOverWithoutLeakingTheFirstAttempt(t *testing.T) {
	first := "data: {\"model\":\"source-a\",\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"
	second := strings.Join([]string{
		"data: {\"model\":\"source-b\",\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n",
		"data: {\"model\":\"source-b\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n",
		"data: {\"model\":\"source-b\",\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\n",
		"data: [DONE]\n\n",
	}, "")
	doer := &scriptedDoer{scripts: []responseScript{
		{status: http.StatusOK, contentType: "text/event-stream", body: first},
		{status: http.StatusOK, contentType: "text/event-stream", body: second},
	}}
	service, health, _ := newGatewayFixture(t, doer)
	sink := &recordingSink{}

	result, err := service.Execute(context.Background(), Input{
		RequestID: "req-stream-failover", DownstreamKey: "js_test", PublicModel: "public-model",
		IngressProtocol: protocol.OpenAI, IngressSurface: protocol.OpenAIChatCompletions,
		Payload: []byte(`{"model":"public-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`),
		Stream:  true,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if sink.commits != 1 || len(result.Attempts) != 2 || result.TargetID != 2 {
		t.Fatalf("stream result = %+v, sink = %+v", result, sink)
	}
	emitted := strings.Join(sink.events, "")
	if strings.Contains(emitted, "source-a") || !strings.Contains(emitted, "source-b") || !strings.Contains(emitted, "hello") {
		t.Fatalf("emitted stream = %q", emitted)
	}
	if result.Attempts[0].FailureKind != routing.FailureStreamTruncated || result.Attempts[0].ResponseCommitted {
		t.Fatalf("first attempt = %+v", result.Attempts[0])
	}
	if health.failureCount(1) != 1 || health.successCount(2) != 1 {
		t.Fatalf("health events = %+v", health.events)
	}
}

func TestStreamTruncationAfterSemanticOutputNeverReplaysOnAnotherTarget(t *testing.T) {
	truncated := strings.Join([]string{
		"data: {\"model\":\"source-a\",\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n",
		"data: {\"model\":\"source-a\",\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n",
	}, "")
	doer := &scriptedDoer{scripts: []responseScript{
		{status: http.StatusOK, contentType: "text/event-stream", body: truncated},
		{status: http.StatusOK, contentType: "text/event-stream", body: "should-not-run"},
	}}
	service, _, _ := newGatewayFixture(t, doer)
	sink := &recordingSink{}

	result, err := service.Execute(context.Background(), Input{
		RequestID: "req-stream-committed", DownstreamKey: "js_test", PublicModel: "public-model",
		IngressProtocol: protocol.OpenAI, IngressSurface: protocol.OpenAIChatCompletions,
		Payload: []byte(`{"model":"public-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`),
		Stream:  true,
	}, sink)
	if !errors.Is(err, ErrCommittedStreamFailed) {
		t.Fatalf("error = %v", err)
	}
	if len(doer.requests) != 1 || sink.commits != 1 || !strings.Contains(strings.Join(sink.events, ""), "partial") {
		t.Fatalf("requests = %+v, sink = %+v", doer.requests, sink)
	}
	if len(result.Attempts) != 1 || !result.Attempts[0].ResponseCommitted {
		t.Fatalf("attempts = %+v", result.Attempts)
	}
}

func TestFirstOutputTimeoutFailsOverBeforeStreamCommit(t *testing.T) {
	second := strings.Join([]string{
		"data: {\"model\":\"source-b\",\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n",
		"data: {\"model\":\"source-b\",\"choices\":[{\"delta\":{\"content\":\"recovered\"}}]}\n\n",
		"data: {\"model\":\"source-b\",\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\n",
		"data: [DONE]\n\n",
	}, "")
	doer := &scriptedDoer{scripts: []responseScript{
		{
			status: http.StatusOK, contentType: "text/event-stream",
			bodyFactory: func(request *http.Request) io.ReadCloser {
				return &contextBlockingBody{ctx: request.Context()}
			},
		},
		{status: http.StatusOK, contentType: "text/event-stream", body: second},
	}}
	service, health, _ := newGatewayFixture(t, doer)
	service.policyProvider = StaticRuntimePolicyProvider{Policy: RuntimePolicy{
		HealthPolicy: routing.DefaultHealthPolicy(), FirstOutputTimeout: 30 * time.Millisecond,
		StreamIdleTimeout: time.Second, RequestTimeout: 2 * time.Second, MaxAttempts: 4,
	}}
	sink := &recordingSink{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	result, err := service.Execute(ctx, Input{
		RequestID: "req-first-output-timeout", DownstreamKey: "js_test", PublicModel: "public-model",
		IngressProtocol: protocol.OpenAI, IngressSurface: protocol.OpenAIChatCompletions,
		Payload: []byte(`{"model":"public-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`),
		Stream:  true,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if len(doer.requests) != 2 || result.TargetID != 2 || len(result.Attempts) != 2 {
		t.Fatalf("result = %+v, requests = %+v", result, doer.requests)
	}
	if sink.commits != 1 || strings.Contains(strings.Join(sink.events, ""), "source-a") ||
		!strings.Contains(strings.Join(sink.events, ""), "recovered") {
		t.Fatalf("first attempt leaked before failover: %+v", sink)
	}
	first := result.Attempts[0]
	if first.FailureKind != routing.FailureFirstOutputTimeout || first.ResponseCommitted ||
		first.SwitchReason != string(routing.RetryNextTarget) {
		t.Fatalf("first attempt = %+v", first)
	}
	state := health.snapshot(1)
	if state.Phase != routing.CircuitOpen || state.ConsecutiveFailures != 1 || state.CooldownUntil.IsZero() ||
		state.LastFailureKind != routing.FailureFirstOutputTimeout {
		t.Fatalf("target health = %+v", state)
	}
}

func TestHalfOpenFirstOutputTimeoutReopensAndFailsOver(t *testing.T) {
	second := strings.Join([]string{
		"data: {\"model\":\"source-b\",\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n",
		"data: {\"model\":\"source-b\",\"choices\":[{\"delta\":{\"content\":\"recovered\"}}]}\n\n",
		"data: {\"model\":\"source-b\",\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\n",
		"data: [DONE]\n\n",
	}, "")
	doer := &scriptedDoer{scripts: []responseScript{
		{
			status: http.StatusOK, contentType: "text/event-stream",
			bodyFactory: func(request *http.Request) io.ReadCloser {
				return &contextBlockingBody{ctx: request.Context()}
			},
		},
		{status: http.StatusOK, contentType: "text/event-stream", body: second},
	}}
	service, health, _ := newGatewayFixture(t, doer)
	service.policyProvider = StaticRuntimePolicyProvider{Policy: RuntimePolicy{
		HealthPolicy: routing.DefaultHealthPolicy(), FirstOutputTimeout: 30 * time.Millisecond,
		StreamIdleTimeout: time.Second, RequestTimeout: 2 * time.Second, MaxAttempts: 4,
	}}
	base := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	health.mu.Lock()
	health.sequences[1] = 2
	health.states[1] = routing.HealthState{
		Revision: 1, Phase: routing.CircuitOpen, Capability: routing.CapabilityUnknown,
		ConsecutiveFailures: 2, CooldownUntil: base.Add(-time.Second), LastEventSequence: 2,
	}
	health.mu.Unlock()

	result, err := service.Execute(context.Background(), Input{
		RequestID: "req-half-open-first-output-timeout", DownstreamKey: "js_test", PublicModel: "public-model",
		IngressProtocol: protocol.OpenAI, IngressSurface: protocol.OpenAIChatCompletions,
		Payload: []byte(`{"model":"public-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`),
		Stream:  true,
	}, &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Attempts) != 2 || result.Attempts[0].PermitMode != routing.PermitHalfOpen ||
		result.Attempts[0].FailureKind != routing.FailureFirstOutputTimeout || result.TargetID != 2 {
		t.Fatalf("half-open timeout failover = %+v", result)
	}
	state := health.snapshot(1)
	if state.Phase != routing.CircuitOpen || state.ConsecutiveFailures != 1 || !state.CooldownUntil.After(base) ||
		!state.HalfOpenLeaseUntil.IsZero() {
		t.Fatalf("failed half-open timeout did not reopen target: %+v", state)
	}
}

func TestStreamIdleTimeoutAfterCommitNeverReplays(t *testing.T) {
	semantic := "data: {\"model\":\"source-a\",\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"
	doer := &scriptedDoer{scripts: []responseScript{
		{
			status: http.StatusOK, contentType: "text/event-stream",
			bodyFactory: func(request *http.Request) io.ReadCloser {
				return &contextBlockingBody{ctx: request.Context(), initial: []byte(semantic)}
			},
		},
		{status: http.StatusOK, contentType: "text/event-stream", body: "should-not-run"},
	}}
	service, health, _ := newGatewayFixture(t, doer)
	service.policyProvider = StaticRuntimePolicyProvider{Policy: RuntimePolicy{
		HealthPolicy: routing.DefaultHealthPolicy(), FirstOutputTimeout: time.Second,
		StreamIdleTimeout: 30 * time.Millisecond, RequestTimeout: 2 * time.Second, MaxAttempts: 4,
	}}
	sink := &recordingSink{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	result, err := service.Execute(ctx, Input{
		RequestID: "req-stream-idle-timeout", DownstreamKey: "js_test", PublicModel: "public-model",
		IngressProtocol: protocol.OpenAI, IngressSurface: protocol.OpenAIChatCompletions,
		Payload: []byte(`{"model":"public-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`),
		Stream:  true,
	}, sink)
	if !errors.Is(err, ErrStreamIdleTimeout) {
		t.Fatalf("error = %v", err)
	}
	if len(doer.requests) != 1 || sink.commits != 1 || !strings.Contains(strings.Join(sink.events, ""), "partial") {
		t.Fatalf("committed stream was replayed or lost: requests = %+v, sink = %+v", doer.requests, sink)
	}
	if len(result.Attempts) != 1 || result.Attempts[0].FailureKind != routing.FailureStreamIdleTimeout ||
		!result.Attempts[0].ResponseCommitted {
		t.Fatalf("attempts = %+v", result.Attempts)
	}
	state := health.snapshot(1)
	if state.Phase != routing.CircuitSuspect || state.LastFailureKind != routing.FailureStreamIdleTimeout {
		t.Fatalf("target health = %+v", state)
	}
}

func TestExecuteEnforcesMaximumStartedAttempts(t *testing.T) {
	doer := &scriptedDoer{scripts: []responseScript{
		{status: http.StatusUnauthorized, body: `{"error":{"code":"invalid_api_key"}}`},
		{status: http.StatusServiceUnavailable, body: `{"error":{"code":"service_unavailable"}}`},
		{status: http.StatusOK, body: chatResponse("source-b", "should-not-run")},
	}}
	service, health, _ := newGatewayFixture(t, doer)
	service.policyProvider = StaticRuntimePolicyProvider{Policy: RuntimePolicy{
		HealthPolicy: routing.DefaultHealthPolicy(), MaxAttempts: 2,
	}}

	result, err := service.Execute(context.Background(), Input{
		RequestID: "req-max-attempts", DownstreamKey: "js_test", PublicModel: "public-model",
		IngressProtocol: protocol.OpenAI, IngressSurface: protocol.OpenAIChatCompletions,
		Payload: []byte(`{"model":"public-model","messages":[{"role":"user","content":"hello"}]}`),
	}, nil)
	if !errors.Is(err, ErrNoAvailableUpstream) {
		t.Fatalf("error = %v", err)
	}
	if len(doer.requests) != 2 || len(result.Attempts) != 2 {
		t.Fatalf("maximum attempts were not enforced: result = %+v, requests = %+v", result, doer.requests)
	}
	if result.Attempts[0].FailureKind != routing.FailureCredentialAuth ||
		result.Attempts[1].FailureKind != routing.FailureUpstreamTransient || health.successCount(2) != 0 {
		t.Fatalf("attempts = %+v, health = %+v", result.Attempts, health.events)
	}
}

func TestLiveFailuresDriveTheSameSuspectOpenAndCooldownSemantics(t *testing.T) {
	doer := &scriptedDoer{scripts: []responseScript{
		{status: http.StatusServiceUnavailable, body: `{"error":{"code":"service_unavailable"}}`},
		{status: http.StatusOK, body: chatResponse("source-b", "fallback-one")},
		{status: http.StatusServiceUnavailable, body: `{"error":{"code":"service_unavailable"}}`},
		{status: http.StatusOK, body: chatResponse("source-b", "fallback-two")},
		{status: http.StatusOK, body: chatResponse("source-b", "cooldown-skip")},
	}}
	service, health, _ := newGatewayFixture(t, doer)

	execute := func(requestID string) Result {
		result, err := service.Execute(context.Background(), Input{
			RequestID: requestID, DownstreamKey: "js_test", PublicModel: "public-model",
			IngressProtocol: protocol.OpenAI, IngressSurface: protocol.OpenAIChatCompletions,
			Payload: []byte(`{"model":"public-model","messages":[{"role":"user","content":"hello"}]}`),
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	first := execute("req-health-one")
	state := health.snapshot(1)
	if state.Phase != routing.CircuitSuspect || state.ConsecutiveFailures != 1 ||
		first.Attempts[0].FailureKind != state.LastFailureKind {
		t.Fatalf("first live failure did not produce suspect state: result = %+v, health = %+v", first, state)
	}
	second := execute("req-health-two")
	state = health.snapshot(1)
	if state.Phase != routing.CircuitOpen || state.ConsecutiveFailures != 2 || state.CooldownUntil.IsZero() ||
		second.Attempts[0].FailureKind != state.LastFailureKind {
		t.Fatalf("second live failure did not open cooldown: result = %+v, health = %+v", second, state)
	}
	third := execute("req-health-cooling")
	if len(doer.requests) != 5 || len(third.Attempts) != 2 || third.Attempts[0].Outcome != "skipped" ||
		third.Attempts[0].ErrorCode != string(routing.PermitCooling) || third.Attempts[1].TargetID != 2 {
		t.Fatalf("cooling target was not skipped in strict order: result = %+v, requests = %+v", third, doer.requests)
	}
}

func newGatewayFixture(t *testing.T, doer *scriptedDoer) (*Service, *fakeHealth, *fakeEffects) {
	t.Helper()
	registry := protocol.NewRegistry()
	adapter, err := openai.NewChatCompletionsAdapter(doer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Register(protocol.OpenAI, protocol.OpenAIChatCompletions, adapter); err != nil {
		t.Fatal(err)
	}
	plan, err := routing.CompilePlan([]routing.Target{
		{ID: 1, Revision: 1, Position: 0, Enabled: true, Credentials: []routing.Credential{
			{ID: 11, Position: 0, Enabled: true}, {ID: 12, Position: 1, Enabled: true},
		}},
		{ID: 2, Revision: 1, Position: 1, Enabled: true, Credentials: []routing.Credential{
			{ID: 21, Position: 0, Enabled: true},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolution := resolver.Resolution{
		DownstreamKeyID:  7,
		PublishedModelID: 70, PublishedModelRevision: 3,
		RoutingProfileID: 9, RoutingProfileName: "Low latency",
		SourceProfileID: 1, SourceProfileName: "Default", RouteRevision: 3,
		PublicModel: "public-model", OfficialPriceSKU: "public-model", Plan: plan,
		Endpoints: map[routing.TargetID]resolver.EndpointMetadata{
			1: {
				TargetID: 1, PublishedModelTargetID: 701, PublishedModelTargetRevision: 5,
				SiteID: 101, SiteName: "Site A",
				EndpointID: 1001, EndpointName: "Endpoint A", BaseURL: "https://a.example/v1",
				Protocol: protocol.OpenAI, Surface: protocol.OpenAIChatCompletions,
				AuthScheme: protocol.AuthBearer, SourceModel: "source-a",
				CredentialNames: map[routing.CredentialID]string{11: "Key A1", 12: "Key A2"},
			},
			2: {
				TargetID: 2, PublishedModelTargetID: 702, PublishedModelTargetRevision: 6,
				SiteID: 102, SiteName: "Site B",
				EndpointID: 1002, EndpointName: "Endpoint B", BaseURL: "https://b.example/v1",
				Protocol: protocol.OpenAI, Surface: protocol.OpenAIChatCompletions,
				AuthScheme: protocol.AuthBearer, SourceModel: "source-b",
				CredentialNames: map[routing.CredentialID]string{21: "Key B1"},
			},
		},
		Health: map[routing.TargetID]routing.HealthState{},
	}
	health := newFakeHealth()
	effects := &fakeEffects{}
	clock := monotonicClock(time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC))
	accounting := &fakeAccounting{}
	book := newGatewayPriceBook(t)
	service, err := New(
		staticResolver{resolution: resolution},
		registry,
		health,
		staticSecrets{secrets: map[routing.CredentialID]string{11: "secret-11", 12: "secret-12", 21: "secret-21"}},
		effects,
		accounting,
		book,
		NewConservativeJSONReservationPlanner(),
		doer,
		Options{Now: clock, DefaultMaxOutputTokens: 128, Capacity: directCapacity{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return service, health, effects
}

type directCapacity struct {
	onAcquire func()
	err       error
}

func (item directCapacity) Acquire(_ context.Context, request capacity.Request) (*capacity.Permit, error) {
	if item.onAcquire != nil {
		item.onAcquire()
	}
	if item.err != nil {
		return nil, item.err
	}
	candidate := request.Candidates[0]
	return &capacity.Permit{SiteID: candidate.SiteID, TargetID: candidate.TargetID}, nil
}

func (directCapacity) ReportThrottle(capacity.ThrottleSignal) error { return nil }

func newGatewayPriceBook(t *testing.T) *pricing.Book {
	t.Helper()
	now := time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)
	book, err := pricing.NewBook(pricing.Catalog{
		Version: "prices-test", Source: "test", SourceDigest: "test-digest", FXVersion: "usd",
		FetchedAt: now, EffectiveAt: now,
		Entries: []pricing.Entry{{
			SKU: "public-model", Provider: "test", ModelPattern: "public-model",
			Rates: []pricing.Rate{
				{Class: pricing.TokenInput, NanoUSDPerMillion: 1_000_000_000},
				{Class: pricing.TokenOutput, NanoUSDPerMillion: 2_000_000_000},
				{Class: pricing.TokenCacheRead, NanoUSDPerMillion: 500_000_000},
				{Class: pricing.TokenCacheWrite, NanoUSDPerMillion: 1_250_000_000},
				{Class: pricing.TokenCacheWrite5m, NanoUSDPerMillion: 1_250_000_000},
				{Class: pricing.TokenCacheWrite1h, NanoUSDPerMillion: 2_000_000_000},
				{Class: pricing.TokenReasoning, NanoUSDPerMillion: 2_000_000_000},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return book
}

type fakeAccounting struct {
	mu                  sync.Mutex
	onStart             func()
	startResult         vnextstore.RequestStartResult
	startErr            error
	recordErr           error
	settleErr           error
	starts              []vnextstore.RequestStart
	attempts            []vnextstore.RequestAttemptWrite
	settlements         []vnextstore.RequestSettlement
	recordContextErrors []error
	settleContextErrors []error
}

func (accounting *fakeAccounting) StartRequestWithQuotaReservation(_ context.Context, input vnextstore.RequestStart) (vnextstore.RequestStartResult, error) {
	if accounting.onStart != nil {
		accounting.onStart()
	}
	accounting.mu.Lock()
	defer accounting.mu.Unlock()
	accounting.starts = append(accounting.starts, input)
	result := accounting.startResult
	if accounting.startErr == nil && !result.AlreadyStarted && result.BillingMultiplierBPS == 0 && result.ReservationNanoUSD == 0 {
		result.BillingMultiplierBPS = vnextstore.DefaultBillingMultiplierBPS
		result.ReservationNanoUSD = input.ReservationNanoUSD
	}
	return result, accounting.startErr
}

func (accounting *fakeAccounting) RecordRequestAttempt(ctx context.Context, input vnextstore.RequestAttemptWrite) error {
	accounting.mu.Lock()
	defer accounting.mu.Unlock()
	accounting.attempts = append(accounting.attempts, input)
	accounting.recordContextErrors = append(accounting.recordContextErrors, ctx.Err())
	return accounting.recordErr
}

func (accounting *fakeAccounting) MarkRequestRouteCandidateSkipped(context.Context, string, int64, string) error {
	return nil
}

func (accounting *fakeAccounting) SettleRequest(ctx context.Context, _ string, input vnextstore.RequestSettlement) (vnextstore.RequestSettlementResult, error) {
	accounting.mu.Lock()
	defer accounting.mu.Unlock()
	accounting.settlements = append(accounting.settlements, input)
	accounting.settleContextErrors = append(accounting.settleContextErrors, ctx.Err())
	return vnextstore.RequestSettlementResult{ChargedNanoUSD: input.OfficialCostNanoUSD}, accounting.settleErr
}

type staticResolver struct {
	resolution resolver.Resolution
}

func (item staticResolver) Resolve(_ context.Context, rawKey, model string, wire protocol.Protocol, surface protocol.Surface) (resolver.Resolution, error) {
	if rawKey != "js_test" || model != item.resolution.PublicModel || wire != protocol.OpenAI || surface != protocol.OpenAIChatCompletions {
		return resolver.Resolution{}, resolver.ErrModelNotFound
	}
	return item.resolution, nil
}

type staticSecrets struct {
	secrets map[routing.CredentialID]string
}

func (provider staticSecrets) Materialize(_ context.Context, _ resolver.EndpointMetadata, credentialID routing.CredentialID) (SecretMaterial, error) {
	secret, exists := provider.secrets[credentialID]
	if !exists {
		return SecretMaterial{}, errors.New("missing test secret")
	}
	return SecretMaterial{Credential: secret, Headers: http.Header{"X-Jieshan-Upstream": []string{"test"}}}, nil
}

type fakeEffects struct {
	mu     sync.Mutex
	events []CredentialEffectEvent
}

func (effects *fakeEffects) ApplyCredentialEffect(_ context.Context, event CredentialEffectEvent) error {
	effects.mu.Lock()
	defer effects.mu.Unlock()
	effects.events = append(effects.events, event)
	return nil
}

type fakeHealth struct {
	mu        sync.Mutex
	sequences map[int64]uint64
	states    map[int64]routing.HealthState
	events    map[int64][]routing.HealthEvent
}

func newFakeHealth() *fakeHealth {
	return &fakeHealth{
		sequences: make(map[int64]uint64),
		states:    make(map[int64]routing.HealthState),
		events:    make(map[int64][]routing.HealthEvent),
	}
}

func (health *fakeHealth) AcquireTargetAttempt(_ context.Context, targetID int64, revision routing.Revision, policy routing.HealthPolicy, now time.Time) (vnextstore.TargetAttemptPermit, error) {
	health.mu.Lock()
	defer health.mu.Unlock()
	health.sequences[targetID]++
	state, exists := health.states[targetID]
	if !exists {
		var err error
		state, err = routing.NewHealthState(revision)
		if err != nil {
			return vnextstore.TargetAttemptPermit{}, err
		}
	}
	next, permit, err := routing.AcquirePermit(state, policy, revision, health.sequences[targetID], now)
	if err != nil {
		return vnextstore.TargetAttemptPermit{}, err
	}
	health.states[targetID] = next
	return vnextstore.TargetAttemptPermit{
		ProviderModelTargetID: targetID,
		Sequence:              health.sequences[targetID],
		Permit:                permit,
		Health:                vnextstore.TargetHealthSnapshot{ProviderModelTargetID: targetID, State: next, StateVersion: 1},
	}, nil
}

func (health *fakeHealth) ApplyTargetHealthEvent(_ context.Context, targetID int64, policy routing.HealthPolicy, event routing.HealthEvent) (vnextstore.TargetHealthSnapshot, routing.ApplyResult, error) {
	health.mu.Lock()
	defer health.mu.Unlock()
	state := health.states[targetID]
	next, applied, err := routing.ApplyHealthEvent(state, policy, event)
	if err != nil {
		return vnextstore.TargetHealthSnapshot{}, routing.ApplyResult{}, err
	}
	health.states[targetID] = next
	health.events[targetID] = append(health.events[targetID], event)
	return vnextstore.TargetHealthSnapshot{ProviderModelTargetID: targetID, State: next, StateVersion: 1}, applied, nil
}

func (health *fakeHealth) failureCount(targetID int64) int {
	count := 0
	for _, event := range health.events[targetID] {
		if event.Outcome == routing.HealthFailure {
			count++
		}
	}
	return count
}

func (health *fakeHealth) successCount(targetID int64) int {
	count := 0
	for _, event := range health.events[targetID] {
		if event.Outcome == routing.HealthSuccess {
			count++
		}
	}
	return count
}

func (health *fakeHealth) snapshot(targetID int64) routing.HealthState {
	health.mu.Lock()
	defer health.mu.Unlock()
	return health.states[targetID]
}

type responseScript struct {
	status      int
	contentType string
	header      http.Header
	body        string
	bodyFactory func(*http.Request) io.ReadCloser
	err         error
}

type recordedRequest struct {
	url           string
	authorization string
	body          string
}

type scriptedDoer struct {
	mu       sync.Mutex
	scripts  []responseScript
	requests []recordedRequest
	onDo     func()
}

func (doer *scriptedDoer) Do(request *http.Request) (*http.Response, error) {
	doer.mu.Lock()
	defer doer.mu.Unlock()
	body, _ := io.ReadAll(request.Body)
	doer.requests = append(doer.requests, recordedRequest{
		url: request.URL.String(), authorization: request.Header.Get("Authorization"), body: string(body),
	})
	if doer.onDo != nil {
		doer.onDo()
	}
	if len(doer.scripts) == 0 {
		return nil, errors.New("no scripted response")
	}
	script := doer.scripts[0]
	doer.scripts = doer.scripts[1:]
	if script.err != nil {
		return nil, script.err
	}
	header := make(http.Header)
	for name, values := range script.header {
		for _, value := range values {
			header.Add(name, value)
		}
	}
	if script.contentType != "" {
		header.Set("Content-Type", script.contentType)
	} else {
		header.Set("Content-Type", "application/json")
	}
	responseBody := io.ReadCloser(io.NopCloser(strings.NewReader(script.body)))
	if script.bodyFactory != nil {
		responseBody = script.bodyFactory(request)
	}
	return &http.Response{
		StatusCode: script.status,
		Header:     header,
		Body:       responseBody,
		Request:    request,
	}, nil
}

type contextBlockingBody struct {
	ctx     context.Context
	initial []byte
}

func (body *contextBlockingBody) Read(buffer []byte) (int, error) {
	if len(body.initial) > 0 {
		count := copy(buffer, body.initial)
		body.initial = body.initial[count:]
		return count, nil
	}
	<-body.ctx.Done()
	return 0, context.Cause(body.ctx)
}

func (body *contextBlockingBody) Close() error { return nil }

type recordingSink struct {
	commits int
	header  http.Header
	events  []string
}

func (sink *recordingSink) Commit(header http.Header) error {
	sink.commits++
	sink.header = header.Clone()
	return nil
}

func (sink *recordingSink) Write(body []byte) error {
	sink.events = append(sink.events, string(body))
	return nil
}

func chatResponse(model, content string) string {
	return `{"id":"chatcmpl_test","object":"chat.completion","model":"` + model +
		`","choices":[{"index":0,"message":{"role":"assistant","content":"` + content +
		`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`
}

func monotonicClock(start time.Time) func() time.Time {
	var mu sync.Mutex
	current := start
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		value := current
		current = current.Add(time.Millisecond)
		return value
	}
}
