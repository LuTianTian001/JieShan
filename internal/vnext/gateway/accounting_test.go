package gateway

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/pricing"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol/openai"
	"github.com/LuTianTian001/JieShan/internal/vnext/resolver"
	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

func TestExecuteReservesBeforeSendingRecordsEveryAttemptAndSettlesFrozenPrice(t *testing.T) {
	doer := &scriptedDoer{scripts: []responseScript{
		{status: http.StatusUnauthorized, body: `{"error":{"code":"invalid_api_key"}}`},
		{status: http.StatusServiceUnavailable, body: `{"error":{"code":"service_unavailable"}}`},
		{status: http.StatusOK, body: chatResponse("source-b", "ok")},
	}}
	service, _, _ := newGatewayFixture(t, doer)
	accounting := &fakeAccounting{}
	service.accounting = accounting
	thinkingBudget := int64(20)
	service.planner = &staticReservationPlanner{plan: ReservationPlan{
		MaximumUsage: pricing.Usage{
			pricing.TokenInput: 100, pricing.TokenOutput: 50, pricing.TokenReasoning: 20,
		},
		ReasoningEffort: "high", ThinkingBudgetTokens: &thinkingBudget,
	}}
	doer.onDo = func() {
		accounting.mu.Lock()
		defer accounting.mu.Unlock()
		if len(accounting.starts) != 1 {
			t.Errorf("upstream send happened before quota reservation: starts=%d", len(accounting.starts))
		}
	}

	result, err := service.Execute(context.Background(), Input{
		RequestID: "req-accounted", DownstreamKey: "js_test", PublicModel: "public-model",
		IngressProtocol: protocol.OpenAI, IngressSurface: protocol.OpenAIChatCompletions,
		Payload: []byte(`{"model":"public-model","messages":[{"role":"user","content":"hello"}]}`),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	accounting.mu.Lock()
	defer accounting.mu.Unlock()
	if len(accounting.starts) != 1 || len(accounting.attempts) != 3 || len(accounting.settlements) != 1 {
		t.Fatalf("accounting calls: starts=%d attempts=%d settlements=%d", len(accounting.starts), len(accounting.attempts), len(accounting.settlements))
	}
	start := accounting.starts[0]
	if start.ID != "req-accounted" || start.DownstreamKeyID != 7 || start.PublishedModelID != 70 ||
		start.PublishedModelRevision != 3 || start.EffectiveRoutingProfileID != 9 ||
		start.EffectiveRoutingProfileName != "Low latency" || start.SourceRoutingProfileID != 1 ||
		start.SourceRoutingProfileName != "Default" || start.PriceCatalogVersion != "prices-test" ||
		start.PriceSKU != "public-model" || start.ReservationNanoUSD != 240_000 ||
		start.ReasoningEffort != "high" || start.ThinkingBudgetTokens == nil || *start.ThinkingBudgetTokens != 20 {
		t.Fatalf("request start = %+v", start)
	}
	wantAttempts := []struct {
		status       string
		failure      string
		switchReason string
		httpStatus   int
	}{
		{status: "failed", failure: "credential_auth", switchReason: "next_credential", httpStatus: 401},
		{status: "failed", failure: "upstream_transient", switchReason: "next_target", httpStatus: 503},
		{status: "success", httpStatus: 200},
	}
	for index, want := range wantAttempts {
		attempt := accounting.attempts[index]
		if attempt.AttemptIndex != index || attempt.Status != want.status || attempt.FailureKind != want.failure ||
			attempt.SwitchReason != want.switchReason || attempt.HTTPStatus == nil || *attempt.HTTPStatus != want.httpStatus ||
			attempt.SiteName == "" || attempt.EndpointName == "" || attempt.CredentialName == "" ||
			attempt.ProviderModelTargetRevision != 1 || attempt.FinishedAt < attempt.StartedAt {
			t.Fatalf("attempt[%d] = %+v", index, attempt)
		}
	}
	settlement := accounting.settlements[0]
	if settlement.Status != "success" || settlement.FinalAttemptIndex == nil || *settlement.FinalAttemptIndex != 2 ||
		settlement.HTTPStatus == nil || *settlement.HTTPStatus != 200 || settlement.InputTokens == nil || *settlement.InputTokens != 2 ||
		settlement.OutputTokens == nil || *settlement.OutputTokens != 1 || settlement.CacheReadTokens == nil ||
		*settlement.CacheReadTokens != 0 || settlement.CacheWriteTokens == nil || *settlement.CacheWriteTokens != 0 ||
		settlement.ReasoningTokens == nil || *settlement.ReasoningTokens != 0 || settlement.OfficialCostNanoUSD != 4_000 {
		t.Fatalf("settlement = %+v", settlement)
	}
	if result.OfficialCostNanoUSD != 4_000 || result.ChargedNanoUSD != 4_000 || result.ReservationNanoUSD != 240_000 {
		t.Fatalf("result accounting = %+v", result)
	}
}

func TestGatewayAccountingRunsAgainstTheCanonicalStore(t *testing.T) {
	ctx := context.Background()
	storage, err := vnextstore.Open(ctx, filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	siteID, err := storage.CreateSite(ctx, vnextstore.SiteWrite{Name: "Stored Site", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	endpointID, err := storage.CreateSiteEndpoint(ctx, siteID, vnextstore.SiteEndpointWrite{
		Name: "Stored Endpoint", BaseURL: "https://stored.example/v1",
		WireProtocol: string(protocol.OpenAI), Surface: string(protocol.OpenAIChatCompletions),
		AuthScheme: string(protocol.AuthBearer), Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	credentialID, err := storage.CreateSiteCredential(ctx, siteID, vnextstore.SiteCredentialWrite{
		Name: "Stored Key", SecretCipher: []byte("sealed-for-test"), CipherVersion: 1, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.ReplaceEndpointCredentialBindings(ctx, siteID, endpointID, 1, []int64{credentialID}); err != nil {
		t.Fatal(err)
	}
	targetID, err := storage.CreateProviderModelTarget(ctx, vnextstore.ProviderModelTargetWrite{
		SiteID: siteID, EndpointID: endpointID, SourceModel: "stored-model", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	quota := int64(1_000_000_000)
	keyID, err := storage.ImportDigestOnlyDownstreamKey(ctx, vnextstore.DownstreamKeyWrite{
		Name: "Stored Client", KeyPrefix: "js_store", KeyDigest: vnextstore.DigestDownstreamKey("js_store"),
		Enabled: true, QuotaNanoUSD: &quota,
	})
	if err != nil {
		t.Fatal(err)
	}
	model, err := storage.CreatePublishedModel(ctx, vnextstore.PublishedModelWrite{
		PublicName: "public-model", OfficialPriceSKU: "public-model", Enabled: true,
	}, []int64{targetID})
	if err != nil {
		t.Fatal(err)
	}
	if len(model.Targets) != 1 {
		t.Fatalf("published model = %+v", model)
	}
	profile, err := storage.GetDefaultRoutingProfile(ctx)
	if err != nil {
		t.Fatal(err)
	}

	plan, err := routing.CompilePlan([]routing.Target{{
		ID: routing.TargetID(targetID), Revision: 1, Enabled: true,
		Credentials: []routing.Credential{{ID: routing.CredentialID(credentialID), Enabled: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	resolution := resolver.Resolution{
		DownstreamKeyID: keyID, DownstreamKeyRevision: 1,
		PublishedModelID: model.ID, PublishedModelRevision: model.Revision,
		RoutingProfileID: profile.ID, RoutingProfileName: profile.Name,
		SourceProfileID: profile.ID, SourceProfileName: profile.Name, RouteRevision: model.Revision,
		PublicModel: "public-model", OfficialPriceSKU: "public-model", Plan: plan,
		Endpoints: map[routing.TargetID]resolver.EndpointMetadata{
			routing.TargetID(targetID): {
				TargetID: routing.TargetID(targetID), PublishedModelTargetID: model.Targets[0].ID,
				PublishedModelTargetRevision: model.Targets[0].Revision,
				SiteID:                       siteID, SiteName: "Stored Site", EndpointID: endpointID, EndpointName: "Stored Endpoint",
				BaseURL: "https://stored.example/v1", Protocol: protocol.OpenAI, Surface: protocol.OpenAIChatCompletions,
				AuthScheme: protocol.AuthBearer, SourceModel: "stored-model",
				CredentialNames: map[routing.CredentialID]string{routing.CredentialID(credentialID): "Stored Key"},
			},
		},
		Health: map[routing.TargetID]routing.HealthState{},
	}
	doer := &scriptedDoer{scripts: []responseScript{{status: http.StatusOK, body: chatResponse("stored-model", "ok")}}}
	registry := protocol.NewRegistry()
	adapter, err := openai.NewChatCompletionsAdapter(doer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Register(protocol.OpenAI, protocol.OpenAIChatCompletions, adapter); err != nil {
		t.Fatal(err)
	}
	service, err := New(
		staticResolver{resolution: resolution}, registry, newFakeHealth(),
		staticSecrets{secrets: map[routing.CredentialID]string{routing.CredentialID(credentialID): "stored-secret"}},
		&fakeEffects{}, storage, newGatewayPriceBook(t), NewConservativeJSONReservationPlanner(), doer,
		Options{
			Now:                    monotonicClock(time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)),
			DefaultMaxOutputTokens: 128,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Execute(ctx, Input{
		RequestID: "req-store-integration", DownstreamKey: "js_test", PublicModel: "public-model",
		IngressProtocol: protocol.OpenAI, IngressSurface: protocol.OpenAIChatCompletions,
		Payload: []byte(`{"model":"public-model","max_tokens":10,"messages":[{"role":"user","content":"hello"}]}`),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	requestLog, err := storage.GetRequestLog(ctx, result.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := storage.ListRequestAttempts(ctx, result.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := storage.ListQuotaLedger(ctx, result.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	key, err := storage.GetDownstreamKey(ctx, keyID)
	if err != nil {
		t.Fatal(err)
	}
	if requestLog.Status != "success" || requestLog.PublishedModelID != model.ID ||
		requestLog.EffectiveRoutingProfileID != profile.ID || requestLog.SourceRoutingProfileID != profile.ID ||
		requestLog.OfficialCostNanoUSD != 4_000 || requestLog.ChargedNanoUSD != 4_000 ||
		len(attempts) != 1 || attempts[0].Status != "success" ||
		attempts[0].PublishedModelTargetID != model.Targets[0].ID ||
		attempts[0].PublishedModelTargetRevision != model.Targets[0].Revision || len(ledger) != 2 ||
		ledger[0].EventType != "reserve" || ledger[1].EventType != "settle" ||
		key.UsedNanoUSD != 4_000 || key.ReservedNanoUSD != 0 {
		t.Fatalf("log=%+v attempts=%+v ledger=%+v key=%+v", requestLog, attempts, ledger, key)
	}
}

func TestTTLCacheWriteDetailsAreStoredButNotDoubleChargedWithTheGenericTotal(t *testing.T) {
	service, _, _ := newGatewayFixture(t, &scriptedDoer{})
	accounting := &fakeAccounting{}
	service.accounting = accounting
	inputTokens := int64(1)
	outputTokens := int64(1)
	cacheReadTokens := int64(0)
	cacheWriteTokens := int64(10)
	cacheWrite5MTokens := int64(4)
	cacheWrite1HTokens := int64(6)
	reasoningTokens := int64(0)
	result := Result{
		RequestID: "req-ttl-accounting",
		Usage: protocol.Usage{
			InputTokens: &inputTokens, OutputTokens: &outputTokens, CacheReadTokens: &cacheReadTokens,
			CacheWriteTokens: &cacheWriteTokens, CacheWrite5MTokens: &cacheWrite5MTokens,
			CacheWrite1HTokens: &cacheWrite1HTokens, ReasoningTokens: &reasoningTokens,
		},
	}
	state := &requestAccounting{
		startedAt: time.Date(2026, time.August, 6, 11, 59, 59, 0, time.UTC),
		quote:     pricing.Quote{CatalogVersion: "prices-test", SKU: "public-model", ReservationNanoUSD: 100_000},
	}
	if err := service.settleAccounting(context.Background(), state, &result, nil); err != nil {
		t.Fatal(err)
	}
	if len(accounting.settlements) != 1 {
		t.Fatalf("settlements = %+v", accounting.settlements)
	}
	settlement := accounting.settlements[0]
	if settlement.CacheWriteTokens == nil || *settlement.CacheWriteTokens != 10 ||
		settlement.CacheWrite5MTokens == nil || *settlement.CacheWrite5MTokens != 4 ||
		settlement.CacheWrite1HTokens == nil || *settlement.CacheWrite1HTokens != 6 {
		t.Fatalf("cache fields = %+v", settlement)
	}
	// 1 input + 1 output + 4 cache-write-5m + 6 cache-write-1h.
	// The generic total of 10 is persisted for audit, not charged again.
	if settlement.OfficialCostNanoUSD != 20_000 || result.OfficialCostNanoUSD != 20_000 {
		t.Fatalf("TTL charge was double counted: settlement=%d result=%d", settlement.OfficialCostNanoUSD, result.OfficialCostNanoUSD)
	}
}

func TestQuotaReservationFailureStopsBeforeAnyUpstreamSend(t *testing.T) {
	doer := &scriptedDoer{scripts: []responseScript{{status: http.StatusOK, body: chatResponse("source-a", "unused")}}}
	service, _, _ := newGatewayFixture(t, doer)
	accounting := &fakeAccounting{startErr: vnextstore.ErrQuotaExceeded}
	service.accounting = accounting

	_, err := service.Execute(context.Background(), Input{
		RequestID: "req-no-quota", DownstreamKey: "js_test", PublicModel: "public-model",
		IngressProtocol: protocol.OpenAI, IngressSurface: protocol.OpenAIChatCompletions,
		Payload: []byte(`{"model":"public-model","max_tokens":10,"messages":[{"role":"user","content":"hello"}]}`),
	}, nil)
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("error = %v", err)
	}
	if len(doer.requests) != 0 || len(accounting.attempts) != 0 || len(accounting.settlements) != 0 {
		t.Fatalf("work escaped failed reservation: requests=%d attempts=%d settlements=%d", len(doer.requests), len(accounting.attempts), len(accounting.settlements))
	}
}

func TestExhaustedRouteSettlesFailureAndReleasesTheReservation(t *testing.T) {
	doer := &scriptedDoer{scripts: []responseScript{
		{status: http.StatusUnauthorized, body: `{"error":{"code":"invalid_api_key"}}`},
		{status: http.StatusServiceUnavailable, body: `{"error":{"code":"service_unavailable"}}`},
		{status: http.StatusBadGateway, body: `{"error":{"code":"bad_gateway"}}`},
	}}
	service, _, _ := newGatewayFixture(t, doer)
	accounting := &fakeAccounting{}
	service.accounting = accounting

	_, err := service.Execute(context.Background(), Input{
		RequestID: "req-exhausted", DownstreamKey: "js_test", PublicModel: "public-model",
		IngressProtocol: protocol.OpenAI, IngressSurface: protocol.OpenAIChatCompletions,
		Payload: []byte(`{"model":"public-model","max_tokens":10,"messages":[{"role":"user","content":"hello"}]}`),
	}, nil)
	if !errors.Is(err, ErrNoAvailableUpstream) {
		t.Fatalf("error = %v", err)
	}
	if len(accounting.attempts) != 3 || len(accounting.settlements) != 1 {
		t.Fatalf("attempts=%d settlements=%d", len(accounting.attempts), len(accounting.settlements))
	}
	settlement := accounting.settlements[0]
	if settlement.Status != "failed" || settlement.FinalAttemptIndex == nil || *settlement.FinalAttemptIndex != 2 ||
		settlement.HTTPStatus == nil || *settlement.HTTPStatus != http.StatusBadGateway ||
		settlement.OfficialCostNanoUSD != 0 || settlement.ErrorCode != "http_502" {
		t.Fatalf("settlement = %+v", settlement)
	}
}

func TestCancellationRecordsAndSettlesWithDetachedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	doer := &scriptedDoer{scripts: []responseScript{{err: context.Canceled}}, onDo: cancel}
	service, _, _ := newGatewayFixture(t, doer)
	accounting := &fakeAccounting{}
	service.accounting = accounting

	_, err := service.Execute(ctx, Input{
		RequestID: "req-cancelled", DownstreamKey: "js_test", PublicModel: "public-model",
		IngressProtocol: protocol.OpenAI, IngressSurface: protocol.OpenAIChatCompletions,
		Payload: []byte(`{"model":"public-model","max_tokens":10,"messages":[{"role":"user","content":"hello"}]}`),
	}, nil)
	if !errors.Is(err, ErrDownstreamClosed) {
		t.Fatalf("error = %v", err)
	}
	accounting.mu.Lock()
	defer accounting.mu.Unlock()
	if len(accounting.attempts) != 1 || accounting.attempts[0].Status != "cancelled" ||
		len(accounting.recordContextErrors) != 1 || accounting.recordContextErrors[0] != nil {
		t.Fatalf("cancelled attempts = %+v contexts=%+v", accounting.attempts, accounting.recordContextErrors)
	}
	if len(accounting.settlements) != 1 || accounting.settlements[0].Status != "cancelled" ||
		len(accounting.settleContextErrors) != 1 || accounting.settleContextErrors[0] != nil {
		t.Fatalf("cancelled settlements = %+v contexts=%+v", accounting.settlements, accounting.settleContextErrors)
	}
}

func TestAttemptPersistenceFailureStillSettlesAndReleasesReservation(t *testing.T) {
	doer := &scriptedDoer{scripts: []responseScript{{status: http.StatusOK, body: chatResponse("source-a", "ok")}}}
	service, _, _ := newGatewayFixture(t, doer)
	accounting := &fakeAccounting{recordErr: errors.New("write unavailable")}
	service.accounting = accounting

	result, err := service.Execute(context.Background(), Input{
		RequestID: "req-attempt-write-failed", DownstreamKey: "js_test", PublicModel: "public-model",
		IngressProtocol: protocol.OpenAI, IngressSurface: protocol.OpenAIChatCompletions,
		Payload: []byte(`{"model":"public-model","max_tokens":10,"messages":[{"role":"user","content":"hello"}]}`),
	}, nil)
	if !errors.Is(err, ErrRuntimeUnavailable) {
		t.Fatalf("error = %v", err)
	}
	if len(doer.requests) != 1 || len(accounting.settlements) != 1 {
		t.Fatalf("requests=%d settlements=%d", len(doer.requests), len(accounting.settlements))
	}
	settlement := accounting.settlements[0]
	if settlement.Status != "failed" || settlement.FinalAttemptIndex != nil || settlement.OfficialCostNanoUSD != 4_000 {
		t.Fatalf("settlement = %+v", settlement)
	}
	if result.OfficialCostNanoUSD != 4_000 || result.ChargedNanoUSD != 4_000 {
		t.Fatalf("result = %+v", result)
	}
}

func TestFrozenPriceChargeFailureStillSettlesToReleaseReservation(t *testing.T) {
	doer := &scriptedDoer{scripts: []responseScript{{status: http.StatusOK, body: chatResponse("source-a", "ok")}}}
	service, _, _ := newGatewayFixture(t, doer)
	accounting := &fakeAccounting{}
	service.accounting = accounting
	service.prices = failingChargeBook{PriceBook: service.prices, err: errors.New("catalog damaged")}

	result, err := service.Execute(context.Background(), Input{
		RequestID: "req-price-failed", DownstreamKey: "js_test", PublicModel: "public-model",
		IngressProtocol: protocol.OpenAI, IngressSurface: protocol.OpenAIChatCompletions,
		Payload: []byte(`{"model":"public-model","max_tokens":10,"messages":[{"role":"user","content":"hello"}]}`),
	}, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if len(accounting.settlements) != 1 || accounting.settlements[0].Status != "success" ||
		accounting.settlements[0].MeteringStatus != meteringUnavailable ||
		accounting.settlements[0].MeteringErrorCode != "pricing_settlement_failed" ||
		accounting.settlements[0].ErrorCode != "" || accounting.settlements[0].OfficialCostNanoUSD != 0 {
		t.Fatalf("settlement = %+v", accounting.settlements)
	}
	if result.MeteringStatus != meteringUnavailable || result.MeteringErrorCode != "pricing_settlement_failed" ||
		result.OfficialCostNanoUSD != 0 || result.ChargedNanoUSD != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestDuplicateAdmittedRequestDoesNotReplayOrSettleAnotherExecution(t *testing.T) {
	doer := &scriptedDoer{scripts: []responseScript{{status: http.StatusOK, body: chatResponse("source-a", "unused")}}}
	service, _, _ := newGatewayFixture(t, doer)
	accounting := &fakeAccounting{startResult: vnextstore.RequestStartResult{AlreadyStarted: true}}
	service.accounting = accounting

	_, err := service.Execute(context.Background(), Input{
		RequestID: "req-duplicate", DownstreamKey: "js_test", PublicModel: "public-model",
		IngressProtocol: protocol.OpenAI, IngressSurface: protocol.OpenAIChatCompletions,
		Payload: []byte(`{"model":"public-model","messages":[{"role":"user","content":"hello"}]}`),
	}, nil)
	if !errors.Is(err, ErrRequestAlreadyStarted) {
		t.Fatalf("error = %v", err)
	}
	if len(doer.requests) != 0 || len(accounting.settlements) != 0 {
		t.Fatalf("duplicate execution escaped: requests=%d settlements=%d", len(doer.requests), len(accounting.settlements))
	}
}

func TestStreamingAccountingStoresFirstSemanticOutputLatency(t *testing.T) {
	stream := strings.Join([]string{
		"data: {\"model\":\"source-a\",\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n",
		"data: {\"model\":\"source-a\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n",
		"data: {\"model\":\"source-a\",\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\n",
		"data: [DONE]\n\n",
	}, "")
	doer := &scriptedDoer{scripts: []responseScript{{status: http.StatusOK, contentType: "text/event-stream", body: stream}}}
	service, _, _ := newGatewayFixture(t, doer)
	accounting := &fakeAccounting{}
	service.accounting = accounting

	_, err := service.Execute(context.Background(), Input{
		RequestID: "req-stream-timing", DownstreamKey: "js_test", PublicModel: "public-model",
		IngressProtocol: protocol.OpenAI, IngressSurface: protocol.OpenAIChatCompletions,
		Payload: []byte(`{"model":"public-model","stream":true,"max_tokens":10,"messages":[{"role":"user","content":"hello"}]}`),
		Stream:  true,
	}, &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	if len(accounting.attempts) != 1 || accounting.attempts[0].FirstTokenMS == nil ||
		len(accounting.settlements) != 1 || accounting.settlements[0].FirstTokenMS == nil {
		t.Fatalf("stream timings: attempts=%+v settlements=%+v", accounting.attempts, accounting.settlements)
	}
	if *accounting.settlements[0].FirstTokenMS < *accounting.attempts[0].FirstTokenMS {
		t.Fatalf("request TTFT %d precedes attempt TTFT %d", *accounting.settlements[0].FirstTokenMS, *accounting.attempts[0].FirstTokenMS)
	}
}

type staticReservationPlanner struct {
	mu     sync.Mutex
	plan   ReservationPlan
	err    error
	inputs []ReservationInput
}

func (planner *staticReservationPlanner) PlanReservation(_ context.Context, input ReservationInput) (ReservationPlan, error) {
	planner.mu.Lock()
	defer planner.mu.Unlock()
	planner.inputs = append(planner.inputs, input)
	return ReservationPlan{
		MaximumUsage: clonePricingUsage(planner.plan.MaximumUsage), ReasoningEffort: planner.plan.ReasoningEffort,
		ThinkingBudgetTokens: cloneInt64Pointer(planner.plan.ThinkingBudgetTokens),
	}, planner.err
}

type failingChargeBook struct {
	PriceBook
	err error
}

func (book failingChargeBook) Charge(string, string, pricing.Usage) (pricing.Charge, error) {
	return pricing.Charge{}, book.err
}
