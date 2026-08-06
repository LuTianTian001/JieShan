package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/health"
)

func TestV4PublishedModelRuntimeDefaultsAndPreservation(t *testing.T) {
	s := newV3TestStore(t)
	ctx := context.Background()
	settings, err := s.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.FirstOutputTimeoutSeconds != 30 || settings.StreamIdleTimeoutSeconds != 60 {
		t.Fatalf("settings timeout defaults = %+v", settings)
	}

	id, err := s.CreatePublishedModel(ctx, PublishedModelWrite{
		PublicName: "runtime-model", Enabled: true, MonitorEnabled: true,
		MonitorIntervalSeconds: 90, CooldownSeconds: 45, FailureThreshold: 3,
		FailureWindowSeconds: 120, FirstOutputTimeoutSeconds: 20, StreamIdleTimeoutSeconds: 40,
		RequestDeadlineSeconds: 180, MaxAttempts: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	item, err := s.GetPublishedModel(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if item.MonitorIntervalSeconds != 90 || item.CooldownSeconds != 45 || item.FailureThreshold != 3 ||
		item.FailureWindowSeconds != 120 || item.FirstOutputTimeoutSeconds != 20 ||
		item.StreamIdleTimeoutSeconds != 40 || item.RequestDeadlineSeconds != 180 || item.MaxAttempts != 5 {
		t.Fatalf("published runtime settings = %+v", item)
	}
	if err := s.UpdatePublishedModel(ctx, id, item.Revision, PublishedModelWrite{
		PublicName: item.PublicName, DisplayName: "Renamed", Enabled: true, MonitorEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := s.GetPublishedModel(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if updated.FirstOutputTimeoutSeconds != 20 || updated.StreamIdleTimeoutSeconds != 40 || updated.MaxAttempts != 5 {
		t.Fatalf("runtime settings were reset by metadata update: %+v", updated)
	}
}

func TestV4FailureThresholdCannotCoolOnTheFirstOrdinaryFailure(t *testing.T) {
	s := newV3TestStore(t)
	ctx := context.Background()
	settings, err := s.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.FailureThreshold = 1
	settings, err = s.UpdateSettings(ctx, settings)
	if err != nil || settings.FailureThreshold != 2 {
		t.Fatalf("UpdateSettings() threshold = %d, %v", settings.FailureThreshold, err)
	}
	id, err := s.CreatePublishedModel(ctx, PublishedModelWrite{
		PublicName: "threshold-model", Enabled: true, FailureThreshold: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	model, err := s.GetPublishedModel(ctx, id)
	if err != nil || model.FailureThreshold != 2 {
		t.Fatalf("published threshold = %d, %v", model.FailureThreshold, err)
	}
}

func TestV4RouteSiteTargetHealthUsesThresholdBeforeCooldown(t *testing.T) {
	s, publishedID, targetID, _, _, _ := newV4TargetFixture(t)
	ctx := context.Background()
	model, err := s.GetPublishedModel(ctx, publishedID)
	if err != nil {
		t.Fatal(err)
	}
	model.CooldownSeconds = 60
	model.FailureThreshold = 2
	model.FailureWindowSeconds = 300
	if err := s.UpdatePublishedModel(ctx, model.ID, model.Revision, PublishedModelWrite{
		PublicName: model.PublicName, Enabled: true, MonitorEnabled: true,
		CooldownSeconds: model.CooldownSeconds, FailureThreshold: model.FailureThreshold,
		FailureWindowSeconds: model.FailureWindowSeconds,
	}); err != nil {
		t.Fatal(err)
	}

	decision := health.Decision{Class: health.ClassUpstreamTransient, Failover: true, PenalizeTarget: true}
	if err := s.RecordRouteSiteTargetFailure(ctx, targetID, decision, "incident-one", "temporary failure", 1_000, 0); err != nil {
		t.Fatal(err)
	}
	state, err := s.GetRouteSiteTargetHealth(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if state.CircuitPhase != "closed" || state.ConsecutiveFailures != 1 || state.CooldownUntil != nil {
		t.Fatalf("first failure cooled the target: %+v", state)
	}
	if err := s.RecordRouteSiteTargetFailure(ctx, targetID, decision, "incident-one", "duplicate", 1_100, 0); err != nil {
		t.Fatal(err)
	}
	state, _ = s.GetRouteSiteTargetHealth(ctx, targetID)
	if state.ConsecutiveFailures != 1 {
		t.Fatalf("duplicate incident was counted: %+v", state)
	}
	if err := s.RecordRouteSiteTargetFailure(ctx, targetID, decision, "incident-two", "temporary failure", 2_000, 0); err != nil {
		t.Fatal(err)
	}
	state, _ = s.GetRouteSiteTargetHealth(ctx, targetID)
	if state.CircuitPhase != "open" || state.CooldownUntil == nil || *state.CooldownUntil != 62_000 {
		t.Fatalf("threshold failure did not open expected cooldown: %+v", state)
	}
	if permitted, err := s.AcquireRouteSiteTargetPermit(ctx, targetID, 10_000, 10*time.Second, false); err != nil || permitted {
		t.Fatalf("permit during cooldown = %v, %v", permitted, err)
	}
	if permitted, err := s.AcquireRouteSiteTargetPermit(ctx, targetID, 63_000, 10*time.Second, false); err != nil || !permitted {
		t.Fatalf("half-open permit after cooldown = %v, %v", permitted, err)
	}
	if err := s.RecordRouteSiteTargetSuccess(ctx, targetID, 64_000); err != nil {
		t.Fatal(err)
	}
	state, _ = s.GetRouteSiteTargetHealth(ctx, targetID)
	if state.CircuitPhase != "closed" || state.CapabilityState != "supported" || state.CooldownUntil != nil {
		t.Fatalf("success did not reset health: %+v", state)
	}

	unsupported := health.Decision{Class: health.ClassModelUnsupported, Failover: true, UnsupportedModel: true}
	if err := s.RecordRouteSiteTargetFailure(ctx, targetID, unsupported, "unsupported", "model not found", 65_000, 0); err != nil {
		t.Fatal(err)
	}
	if permitted, err := s.AcquireRouteSiteTargetPermit(ctx, targetID, 66_000, time.Second, false); err != nil || permitted {
		t.Fatalf("unsupported runtime permit = %v, %v", permitted, err)
	}
	if permitted, err := s.AcquireRouteSiteTargetPermit(ctx, targetID, 66_000, time.Second, true); err != nil || !permitted {
		t.Fatalf("unsupported probe permit = %v, %v", permitted, err)
	}
}

func TestV4ProbeAndDiscoveryRunsPersistQueryableSnapshots(t *testing.T) {
	s, publishedID, targetID, siteID, endpointID, credentialID := newV4TargetFixture(t)
	ctx := context.Background()
	model, _ := s.GetPublishedModel(ctx, publishedID)
	if err := s.InsertProbeRun(ctx, ProbeRun{
		ID: "probe-one", PublishedModelID: publishedID, PublishedModelRevision: model.Revision,
		TriggerKind: "manual", StartedAt: 1_000,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertProbeAttempt(ctx, ProbeAttempt{
		ProbeRunID: "probe-one", AttemptIndex: 0, RouteSiteTargetID: copyInt64(targetID),
		InferenceCredentialID: copyInt64(credentialID), Status: "success", HTTPStatus: intValue(200),
		LatencyMS: copyInt64(420), FirstOutputMS: copyInt64(180), StartedAt: 1_000, FinishedAt: 1_420,
	}); err != nil {
		t.Fatal(err)
	}
	run, err := s.CompleteProbeRun(ctx, "probe-one", 1_500, "")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "success" || run.TargetCount != 1 || run.SuccessCount != 1 {
		t.Fatalf("probe run = %+v", run)
	}
	attempts, err := s.ListProbeAttempts(ctx, run.ID)
	if err != nil || len(attempts) != 1 || attempts[0].SiteName == "" || attempts[0].CredentialName == "" {
		t.Fatalf("probe attempts = %+v, %v", attempts, err)
	}

	if err := s.InsertModelDiscoveryRun(ctx, ModelDiscoveryRun{
		ID: "discover-one", SiteID: siteID, EndpointID: endpointID, Mode: "selected", StartedAt: 2_000,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertModelDiscoveryAttempt(ctx, ModelDiscoveryAttempt{
		DiscoveryRunID: "discover-one", AttemptIndex: 0, InferenceCredentialID: copyInt64(credentialID),
		Status: "success", ModelCount: 7, Complete: true, PagesFetched: 1, StartedAt: 2_000, FinishedAt: 2_300,
	}); err != nil {
		t.Fatal(err)
	}
	discovery, err := s.CompleteModelDiscoveryRun(ctx, "discover-one", 7, json.RawMessage(`{"added":7}`), "", 2_400)
	if err != nil {
		t.Fatal(err)
	}
	if discovery.Status != "success" || discovery.SuccessCount != 1 || discovery.ModelCount != 7 ||
		!strings.Contains(string(discovery.Summary), `"added":7`) {
		t.Fatalf("discovery run = %+v", discovery)
	}
	discoveryAttempts, err := s.ListModelDiscoveryAttempts(ctx, discovery.ID)
	if err != nil || len(discoveryAttempts) != 1 || discoveryAttempts[0].CredentialName == "" {
		t.Fatalf("discovery attempts = %+v, %v", discoveryAttempts, err)
	}
}

func newV4TargetFixture(t *testing.T) (*Store, int64, int64, int64, int64, int64) {
	t.Helper()
	s := newV3TestStore(t)
	ctx := context.Background()
	siteID, endpointID, credentials := mustCreateSiteResources(t, s, "Runtime", 1)
	modelID, err := s.CreateSiteModel(ctx, SiteModelWrite{
		SiteID: siteID, EndpointID: endpointID, ModelName: "source-model", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishedID, err := s.CreatePublishedModel(ctx, PublishedModelWrite{
		PublicName: "public-runtime", Enabled: true, MonitorEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	published, _ := s.GetPublishedModel(ctx, publishedID)
	targetID, err := s.CreateRouteSiteTarget(ctx, publishedID, published.Revision, RouteSiteTargetWrite{
		SiteID: siteID, EndpointID: endpointID, SiteModelID: modelID, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, publishedID, targetID, siteID, endpointID, credentials[0]
}

func intValue(value int) *int {
	copy := value
	return &copy
}
