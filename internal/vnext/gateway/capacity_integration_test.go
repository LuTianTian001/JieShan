package gateway

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/capacity"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol"
)

func TestCapacityAdmissionUsesStrictEligibleTargetOrder(t *testing.T) {
	manager := testCapacityManager(t)
	holder, err := manager.Acquire(context.Background(), capacity.Request{
		KeyID: 99, Candidates: []capacity.Candidate{{TargetID: 99, SiteID: 101}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()

	doer := &scriptedDoer{scripts: []responseScript{{status: http.StatusOK, body: chatResponse("source-b", "ok")}}}
	service, _, _ := newGatewayFixture(t, doer)
	service.capacity = manager
	result, err := service.Execute(context.Background(), standardCapacityInput("req-capacity-order", false), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.TargetID != 2 || len(doer.requests) != 1 || doer.requests[0].url != "https://b.example/v1/chat/completions" {
		t.Fatalf("capacity order result = %+v, requests = %+v", result, doer.requests)
	}
}

func TestExplicitConcurrency429ThrottlesSiteWithoutOpeningHealth(t *testing.T) {
	manager := testCapacityManager(t)
	doer := &scriptedDoer{scripts: []responseScript{
		{
			status: http.StatusTooManyRequests,
			header: http.Header{"Retry-After": []string{"604800"}},
			body:   `{"error":{"code":"concurrency_limit_exceeded"}}`,
		},
		{status: http.StatusOK, body: chatResponse("source-b", "fallback")},
	}}
	service, health, _ := newGatewayFixture(t, doer)
	service.capacity = manager
	result, err := service.Execute(context.Background(), standardCapacityInput("req-concurrency-throttle", false), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.TargetID != 2 || len(result.Attempts) != 2 || result.Attempts[0].Outcome != "throttled" {
		t.Fatalf("result = %+v", result)
	}
	if health.failureCount(1) != 0 {
		t.Fatalf("concurrency throttle changed target health: %+v", health.events)
	}
	snapshot := manager.Snapshot()
	if len(snapshot.Sites) != 2 || snapshot.Sites[0].SiteID != 101 || snapshot.Sites[0].ThrottledUntil.IsZero() {
		t.Fatalf("capacity snapshot = %+v", snapshot)
	}
}

func TestExplicitConcurrency429WithoutRetryAfterStaysOutOfCredentialAndHealthState(t *testing.T) {
	manager := testCapacityManager(t)
	doer := &scriptedDoer{scripts: []responseScript{
		{
			status: http.StatusTooManyRequests,
			body:   `{"error":{"code":"concurrency_limit_exceeded"}}`,
		},
		{status: http.StatusOK, body: chatResponse("source-b", "fallback")},
	}}
	service, health, effects := newGatewayFixture(t, doer)
	service.capacity = manager
	result, err := service.Execute(context.Background(), standardCapacityInput("req-concurrency-no-retry-after", false), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.TargetID != 2 || len(result.Attempts) != 2 || result.Attempts[0].Outcome != "throttled" {
		t.Fatalf("result = %+v", result)
	}
	if health.failureCount(1) != 0 || len(effects.events) != 0 {
		t.Fatalf("concurrency response changed health or credential state: health=%+v effects=%+v", health.events, effects.events)
	}
	for _, site := range manager.Snapshot().Sites {
		if !site.ThrottledUntil.IsZero() {
			t.Fatalf("missing Retry-After created an arbitrary throttle deadline: %+v", site)
		}
	}
}

func TestGenericCredential429DoesNotThrottleSite(t *testing.T) {
	manager := testCapacityManager(t)
	doer := &scriptedDoer{scripts: []responseScript{
		{
			status: http.StatusTooManyRequests,
			header: http.Header{"Retry-After": []string{"1"}},
			body:   `{"error":{"code":"rate_limit_exceeded"}}`,
		},
		{status: http.StatusOK, body: chatResponse("source-a", "second credential")},
	}}
	service, health, effects := newGatewayFixture(t, doer)
	service.capacity = manager
	result, err := service.Execute(context.Background(), standardCapacityInput("req-generic-429", false), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.TargetID != 1 || len(result.Attempts) != 2 || health.failureCount(1) != 0 || len(effects.events) != 1 {
		t.Fatalf("result = %+v, health = %+v, effects = %+v", result, health.events, effects.events)
	}
	for _, site := range manager.Snapshot().Sites {
		if !site.ThrottledUntil.IsZero() {
			t.Fatalf("generic 429 throttled Site: %+v", site)
		}
	}
}

func TestCapacityPermitCoversCompleteBodyLifecycle(t *testing.T) {
	manager := testCapacityManager(t)
	doer := &scriptedDoer{scripts: []responseScript{{
		status: http.StatusOK,
		bodyFactory: func(request *http.Request) io.ReadCloser {
			return &contextBlockingBody{ctx: request.Context(), initial: []byte(chatResponse("source-a", "partial"))}
		},
	}}}
	service, _, _ := newGatewayFixture(t, doer)
	service.capacity = manager
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := service.Execute(ctx, standardCapacityInput("req-capacity-body", false), nil)
		result <- err
	}()
	waitForCapacityInFlight(t, manager, 101, 1)
	cancel()
	if err := <-result; err == nil {
		t.Fatal("canceled body request succeeded")
	}
	waitForCapacityInFlight(t, manager, 101, 0)
}

func TestCapacityPermitCoversCompleteStreamLifecycle(t *testing.T) {
	manager := testCapacityManager(t)
	doer := &scriptedDoer{scripts: []responseScript{{
		status:      http.StatusOK,
		contentType: "text/event-stream",
		bodyFactory: func(request *http.Request) io.ReadCloser {
			return &contextBlockingBody{
				ctx:     request.Context(),
				initial: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"),
			}
		},
	}}}
	service, _, _ := newGatewayFixture(t, doer)
	service.capacity = manager
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := service.Execute(ctx, standardCapacityInput("req-capacity-stream", true), &recordingSink{})
		result <- err
	}()
	waitForCapacityInFlight(t, manager, 101, 1)
	cancel()
	if err := <-result; err == nil {
		t.Fatal("canceled stream request succeeded")
	}
	waitForCapacityInFlight(t, manager, 101, 0)
}

func testCapacityManager(t *testing.T) *capacity.Manager {
	t.Helper()
	manager, err := capacity.New(capacity.Config{MaxQueued: 8, QueueTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ReplaceSites([]capacity.SiteConfig{
		{SiteID: 101, MaxInFlight: 1}, {SiteID: 102, MaxInFlight: 1},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func standardCapacityInput(requestID string, stream bool) Input {
	return Input{
		RequestID: requestID, DownstreamKey: "js_test", PublicModel: "public-model",
		IngressProtocol: protocol.OpenAI, IngressSurface: protocol.OpenAIChatCompletions,
		Payload: []byte(`{"model":"public-model","messages":[{"role":"user","content":"hello"}]}`), Stream: stream,
	}
}

func waitForCapacityInFlight(t *testing.T, manager *capacity.Manager, siteID capacity.SiteID, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, site := range manager.Snapshot().Sites {
			if site.SiteID == siteID && site.InFlight == want {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("capacity snapshot = %+v, want Site %d inflight %d", manager.Snapshot(), siteID, want)
}
