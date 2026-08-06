package store

import (
	"context"
	"testing"
)

func TestCompleteProbeRunAggregatesMultipleKeyAttemptsPerTarget(t *testing.T) {
	s, publishedID, targetID, _, _, credentialID := newV4TargetFixture(t)
	ctx := context.Background()
	model, _ := s.GetPublishedModel(ctx, publishedID)
	if err := s.InsertProbeRun(ctx, ProbeRun{
		ID: "probe-key-rotation", PublishedModelID: publishedID, PublishedModelRevision: model.Revision,
		TriggerKind: "manual", TargetCount: 1, StartedAt: 1_000,
	}); err != nil {
		t.Fatal(err)
	}
	for index, status := range []string{"failed", "success"} {
		if _, err := s.InsertProbeAttempt(ctx, ProbeAttempt{
			ProbeRunID: "probe-key-rotation", AttemptIndex: index, RouteSiteTargetID: copyInt64(targetID),
			InferenceCredentialID: copyInt64(credentialID), Status: status,
			StartedAt: int64(1_000 + index*100), FinishedAt: int64(1_050 + index*100),
		}); err != nil {
			t.Fatal(err)
		}
	}
	run, err := s.CompleteProbeRun(ctx, "probe-key-rotation", 1_300, "")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "success" || run.TargetCount != 1 || run.SuccessCount != 1 || run.FailureCount != 0 || run.SkippedCount != 0 {
		t.Fatalf("aggregated probe run = %+v", run)
	}
}

func TestListDuePublishedModelsUsesPerModelIntervalAndSuppressesRunning(t *testing.T) {
	s, publishedID, _, _, _, _ := newV4TargetFixture(t)
	ctx := context.Background()
	const startedAt = int64(1_000_000)
	model, _ := s.GetPublishedModel(ctx, publishedID)
	if err := s.InsertProbeRun(ctx, ProbeRun{
		ID: "probe-finished", PublishedModelID: publishedID, PublishedModelRevision: model.Revision,
		TriggerKind: "scheduled", TargetCount: 1, StartedAt: startedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteProbeRun(ctx, "probe-finished", startedAt+10, ""); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListDuePublishedModels(ctx, startedAt+299_999, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("model became due early: %+v", items)
	}
	items, err = s.ListDuePublishedModels(ctx, startedAt+300_000, 50)
	if err != nil || len(items) != 1 || items[0].ID != publishedID {
		t.Fatalf("due models = %+v, %v", items, err)
	}
	if err := s.InsertProbeRun(ctx, ProbeRun{
		ID: "probe-running", PublishedModelID: publishedID, PublishedModelRevision: model.Revision,
		TriggerKind: "scheduled", TargetCount: 1, StartedAt: startedAt + 300_000,
	}); err != nil {
		t.Fatal(err)
	}
	items, err = s.ListDuePublishedModels(ctx, startedAt+900_000, 50)
	if err != nil || len(items) != 0 {
		t.Fatalf("running probe was not suppressed: %+v, %v", items, err)
	}
	expired, err := s.ExpireStaleProbeRuns(ctx, startedAt+900_000)
	if err != nil || expired != 1 {
		t.Fatalf("expired stale probes = %d, %v", expired, err)
	}
	items, err = s.ListDuePublishedModels(ctx, startedAt+900_000, 50)
	if err != nil || len(items) != 1 || items[0].ID != publishedID {
		t.Fatalf("stale probe did not release model scheduling: %+v, %v", items, err)
	}
}

func TestPublishedModelMonitorMatrixIncludesHealthAndLatestProbe(t *testing.T) {
	s, publishedID, targetID, _, _, credentialID := newV4TargetFixture(t)
	ctx := context.Background()
	model, _ := s.GetPublishedModel(ctx, publishedID)
	if err := s.InsertProbeRun(ctx, ProbeRun{
		ID: "probe-matrix", PublishedModelID: publishedID, PublishedModelRevision: model.Revision,
		TriggerKind: "manual", TargetCount: 1, StartedAt: 2_000,
	}); err != nil {
		t.Fatal(err)
	}
	latency := int64(125)
	if _, err := s.InsertProbeAttempt(ctx, ProbeAttempt{
		ProbeRunID: "probe-matrix", AttemptIndex: 0, RouteSiteTargetID: copyInt64(targetID),
		InferenceCredentialID: copyInt64(credentialID), Status: "success", HTTPStatus: intValue(200),
		LatencyMS: &latency, StartedAt: 2_000, FinishedAt: 2_125,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteProbeRun(ctx, "probe-matrix", 2_200, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRouteSiteTargetSuccess(ctx, targetID, 2_125); err != nil {
		t.Fatal(err)
	}
	matrix, err := s.ListPublishedModelMonitorMatrix(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(matrix) != 1 || len(matrix[0].Targets) != 1 {
		t.Fatalf("monitor matrix = %+v", matrix)
	}
	target := matrix[0].Targets[0]
	if target.Health.CapabilityState != "supported" || target.LastProbe == nil || target.LastProbe.Status != "success" || target.LastProbe.LatencyMS == nil || *target.LastProbe.LatencyMS != latency {
		t.Fatalf("monitor target = %+v", target)
	}
}
