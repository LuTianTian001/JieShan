package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/LuTianTian001/JieShan/internal/store"
)

func TestProbePublishedModelRotatesKeysAndAggregatesBySite(t *testing.T) {
	var mu sync.Mutex
	authOrder := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		mu.Lock()
		authOrder = append(authOrder, auth)
		mu.Unlock()
		if auth == "Bearer rejected" {
			writeOpenAIError(w, http.StatusUnauthorized, "invalid key", "invalid_api_key")
			return
		}
		successResponse(w, "provider-probe")
	}))
	defer server.Close()

	fixture := newV3GatewayFixture(t, v3SiteSpec{
		name: "Probe", baseURL: server.URL, keys: []string{"rejected", "accepted"},
	})
	model, err := fixture.store.GetPublishedModelByName(context.Background(), fixture.model)
	if err != nil {
		t.Fatal(err)
	}
	run, err := fixture.gateway.ProbePublishedModel(context.Background(), model.ID, nil, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "success" || run.TargetCount != 1 || run.SuccessCount != 1 || run.FailureCount != 0 {
		t.Fatalf("probe run = %+v", run)
	}
	attempts, err := fixture.store.ListProbeAttempts(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 || attempts[0].Status != "failed" || attempts[1].Status != "success" {
		t.Fatalf("probe attempts = %+v", attempts)
	}
	mu.Lock()
	gotOrder := strings.Join(authOrder, ",")
	mu.Unlock()
	if gotOrder != "Bearer rejected,Bearer accepted" {
		t.Fatalf("credential order = %q", gotOrder)
	}
	credential, err := fixture.store.GetInferenceCredential(context.Background(), fixture.credentialIDs[0][0])
	if err != nil || credential.RuntimeState != "invalid" {
		t.Fatalf("rejected credential = %+v, %v", credential, err)
	}
	healthState, err := fixture.store.GetRouteSiteTargetHealth(context.Background(), fixture.targetIDs[0])
	if err != nil || healthState.CapabilityState != "supported" || healthState.CircuitPhase != "closed" {
		t.Fatalf("target health = %+v, %v", healthState, err)
	}
}

func TestProbePublishedModelStopsKeyRotationOnSiteFailureButChecksOtherSites(t *testing.T) {
	firstHits := 0
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstHits++
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
	}))
	defer first.Close()
	secondHits := 0
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondHits++
		successResponse(w, "provider-second")
	}))
	defer second.Close()

	fixture := newV3GatewayFixture(t,
		v3SiteSpec{name: "First", baseURL: first.URL, keys: []string{"first-a", "first-b"}},
		v3SiteSpec{name: "Second", baseURL: second.URL, keys: []string{"second"}},
	)
	model, _ := fixture.store.GetPublishedModelByName(context.Background(), fixture.model)
	run, err := fixture.gateway.ProbePublishedModel(context.Background(), model.ID, nil, "scheduled")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "partial" || run.TargetCount != 2 || run.SuccessCount != 1 || run.FailureCount != 1 {
		t.Fatalf("probe run = %+v", run)
	}
	if firstHits != 1 || secondHits != 1 {
		t.Fatalf("site probe hits first=%d second=%d", firstHits, secondHits)
	}
	attempts, err := fixture.store.ListProbeAttempts(context.Background(), run.ID)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("probe attempts = %+v, %v", attempts, err)
	}
	firstHealth, err := fixture.store.GetRouteSiteTargetHealth(context.Background(), fixture.targetIDs[0])
	if err != nil || firstHealth.ConsecutiveFailures != 1 || firstHealth.CircuitPhase != "closed" {
		t.Fatalf("first target health = %+v, %v", firstHealth, err)
	}
}

func TestProbePublishedModelRequiresMonitoringSelection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		successResponse(w, "provider")
	}))
	defer server.Close()
	fixture := newV3GatewayFixture(t, v3SiteSpec{name: "Disabled", baseURL: server.URL, keys: []string{"key"}})
	model, _ := fixture.store.GetPublishedModelByName(context.Background(), fixture.model)
	if err := fixture.store.UpdatePublishedModel(context.Background(), model.ID, model.Revision, store.PublishedModelWrite{
		PublicName: model.PublicName, Enabled: true, MonitorEnabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.gateway.ProbePublishedModel(context.Background(), model.ID, nil, "manual")
	if !errors.Is(err, ErrPublishedModelMonitoringDisabled) {
		t.Fatalf("probe error = %v", err)
	}
}
