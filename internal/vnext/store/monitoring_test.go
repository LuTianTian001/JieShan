package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestModelMonitorClaimsOnlyExplicitEnabledRoutesAndPersistsGlobalInterval(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	_, enabledModel, targets := createMonitorTestModel(t, s, "enabled", 2)
	_, disabledModel, _ := createMonitorTestModel(t, s, "disabled", 1)
	_, implicitModel, _ := createMonitorTestModel(t, s, "implicit", 1)
	runtimeSettings, err := s.GetRuntimeSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	global := DefaultRuntimeSettingsWrite()
	global.ProbeInterval = 2 * time.Minute
	if _, err := s.UpdateRuntimeSettingsCAS(ctx, runtimeSettings.Revision, global, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	setting, err := s.PutModelMonitorSetting(ctx, enabledModel, ModelMonitorSettingWrite{
		Enabled: true, HistoryLimit: 24,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !setting.Enabled || setting.Interval != 2*time.Minute || setting.HistoryLimit != 24 ||
		!setting.NextProbeAt.Equal(now) {
		t.Fatalf("enabled monitor setting = %+v", setting)
	}
	if _, err := s.PutModelMonitorSetting(ctx, disabledModel, ModelMonitorSettingWrite{
		Enabled: false,
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetModelMonitorSetting(ctx, implicitModel); !errors.Is(err, ErrModelMonitorNotFound) {
		t.Fatalf("implicit model unexpectedly has a monitor: %v", err)
	}

	jobs, err := s.ClaimDueModelMonitors(ctx, "worker:first", now, time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Setting.PublishedModelID != enabledModel {
		t.Fatalf("claimed jobs = %+v", jobs)
	}
	if len(jobs[0].Targets) != 2 || jobs[0].Targets[0].ProviderModelTargetID != targets[0] ||
		jobs[0].Targets[1].ProviderModelTargetID != targets[1] {
		t.Fatalf("model-wide target order = %+v", jobs[0].Targets)
	}
	for _, target := range jobs[0].Targets {
		if len(target.CredentialIDs) != 1 {
			t.Fatalf("target credentials = %+v", target)
		}
	}
	duplicate, err := s.ClaimDueModelMonitors(ctx, "worker:second", now, time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(duplicate) != 0 {
		t.Fatalf("active lease was claimed twice: %+v", duplicate)
	}

	finishedAt := now.Add(10 * time.Second)
	if err := s.FinishModelMonitorClaim(ctx, enabledModel, "worker:first", finishedAt); err != nil {
		t.Fatal(err)
	}
	setting, err = s.GetModelMonitorSetting(ctx, enabledModel)
	if err != nil {
		t.Fatal(err)
	}
	wantNext := finishedAt.Add(2 * time.Minute)
	if setting.LeaseOwner != "" || setting.LeaseUntil != nil || setting.LastProbeFinishedAt == nil ||
		!setting.NextProbeAt.Equal(wantNext) {
		t.Fatalf("finished setting = %+v, want next %v", setting, wantNext)
	}
	before, err := s.ClaimDueModelMonitors(ctx, "worker:before", wantNext.Add(-time.Millisecond), time.Minute, 10)
	if err != nil || len(before) != 0 {
		t.Fatalf("claimed before configured interval: jobs=%+v err=%v", before, err)
	}
	after, err := s.ClaimDueModelMonitors(ctx, "worker:after", wantNext, time.Minute, 10)
	if err != nil || len(after) != 1 {
		t.Fatalf("did not claim at configured interval: jobs=%+v err=%v", after, err)
	}
}

func TestManualModelMonitorLeasePreventsConcurrentDuplicate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	_, modelID, _ := createMonitorTestModel(t, s, "manual", 1)
	if _, err := s.PutModelMonitorSetting(ctx, modelID, ModelMonitorSettingWrite{Enabled: true}, now); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimModelMonitor(ctx, modelID, "manual:first", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if job.Setting.LeaseOwner != "manual:first" {
		t.Fatalf("lease owner = %q", job.Setting.LeaseOwner)
	}
	if _, err := s.ClaimModelMonitor(ctx, modelID, "manual:second", now, time.Minute); !errors.Is(err, ErrModelMonitorBusy) {
		t.Fatalf("second manual claim error = %v", err)
	}
	if err := s.ReleaseModelMonitorClaim(ctx, modelID, "manual:first"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimModelMonitor(ctx, modelID, "manual:second", now, time.Minute); err != nil {
		t.Fatalf("claim after release: %v", err)
	}
}

func TestModelProbeHistoryStoresRealTargetMetricsAndPrunesPerModelTarget(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	_, modelID, targets := createMonitorTestModel(t, s, "history", 1)
	if _, err := s.PutModelMonitorSetting(ctx, modelID, ModelMonitorSettingWrite{
		Enabled: true, HistoryLimit: 2,
	}, base); err != nil {
		t.Fatal(err)
	}

	for index := 0; index < 3; index++ {
		now := base.Add(time.Duration(index) * time.Minute)
		owner := fmt.Sprintf("history:%d", index)
		job, err := s.ClaimModelMonitor(ctx, modelID, owner, now, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		runID := fmt.Sprintf("run-%d", index)
		if err := s.StartModelProbeRun(ctx, ModelProbeRunWrite{
			ID: runID, PublishedModelID: modelID, LeaseOwner: owner,
			TriggerKind: "manual", TargetCount: 1, StartedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		firstOutput := int64(40 + index)
		status := 200
		result := ModelProbeTargetWrite{
			RunID: runID, PublishedModelID: modelID,
			PublishedModelTargetID:       job.Targets[0].PublishedModelTargetID,
			PublishedModelTargetRevision: job.Targets[0].PublishedModelTargetRevision,
			ProviderModelTargetID:        targets[0], ProviderModelTargetRevision: 1,
			TargetPosition: 0, SiteID: job.Targets[0].SiteID, EndpointID: job.Targets[0].EndpointID,
			SiteName: job.Targets[0].SiteName, EndpointName: job.Targets[0].EndpointName,
			SourceModel: job.Targets[0].SourceModel, WireProtocol: job.Targets[0].WireProtocol,
			Surface: job.Targets[0].Surface, Outcome: "success", PermitMode: "normal",
			PermitReason: "granted", HTTPStatus: &status, LatencyMS: int64(100 + index),
			FirstOutputMS: &firstOutput, StartedAt: now, FinishedAt: now.Add(100 * time.Millisecond),
			HealthApplied: true, HealthApplyReason: "accepted",
		}
		if err := s.SaveModelProbeTargetResult(ctx, result); err != nil {
			t.Fatal(err)
		}
		finishedRun, err := s.FinishModelProbeRun(ctx, runID, "completed", now.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if finishedRun.SuccessCount != 1 || finishedRun.FailureCount != 0 || finishedRun.SkippedCount != 0 {
			t.Fatalf("run counters = %+v", finishedRun)
		}
		if err := s.FinishModelMonitorClaim(ctx, modelID, owner, now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}

	history, err := s.ListModelProbeTargetResults(ctx, modelID, targets[0], 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].RunID != "run-2" || history[1].RunID != "run-1" {
		t.Fatalf("pruned history = %+v", history)
	}
	if history[0].HTTPStatus == nil || *history[0].HTTPStatus != 200 || history[0].FirstOutputMS == nil ||
		*history[0].FirstOutputMS != 42 || history[0].LatencyMS != 102 || !history[0].HealthApplied {
		t.Fatalf("stored target metrics = %+v", history[0])
	}
	if _, err := s.GetModelProbeRun(ctx, "run-0"); !errors.Is(err, ErrModelProbeRunNotFound) {
		t.Fatalf("orphaned pruned run still exists: %v", err)
	}
}

func createMonitorTestModel(t *testing.T, s *Store, suffix string, targetCount int) (int64, int64, []int64) {
	t.Helper()
	ctx := context.Background()
	targets := make([]int64, 0, targetCount)
	for index := 0; index < targetCount; index++ {
		siteID := mustCreateSite(t, s, fmt.Sprintf("Monitor %s site %d", suffix, index))
		endpointID := mustCreateEndpoint(t, s, siteID, fmt.Sprintf("Endpoint %d", index),
			fmt.Sprintf("https://%s-%d.example/v1", suffix, index))
		credentialID, err := s.CreateSiteCredential(ctx, siteID, SiteCredentialWrite{
			Name: "Primary", SecretCipher: []byte{1, 2, 3}, CipherVersion: 1, Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		endpoint, err := s.GetSiteEndpoint(ctx, endpointID)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.ReplaceEndpointCredentialBindings(ctx, siteID, endpointID, endpoint.Revision, []int64{credentialID}); err != nil {
			t.Fatal(err)
		}
		targets = append(targets, mustCreateProviderTarget(t, s, siteID, endpointID, "model-"+suffix))
	}
	model, err := s.CreatePublishedModel(ctx, PublishedModelWrite{
		PublicName: "public-" + suffix, OfficialPriceSKU: "public-" + suffix, Enabled: true,
	}, targets)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := s.GetDefaultRoutingProfile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return profile.ID, model.ID, targets
}
