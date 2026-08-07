package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMonitorRouteViewsAndCASSettingsUseGlobalInterval(t *testing.T) {
	storage := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	_, selectedModel, targets := createMonitorTestModel(t, storage, "matrix-selected", 2)
	_, unselectedModel, _ := createMonitorTestModel(t, storage, "matrix-unselected", 1)
	setting, err := storage.CreateModelMonitorSetting(ctx, selectedModel, ModelMonitorSettingWrite{
		Enabled: true, HistoryLimit: 48,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.CreateModelMonitorSetting(ctx, selectedModel, ModelMonitorSettingWrite{Enabled: true}, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate monitor create = %v", err)
	}
	updated, err := storage.UpdateModelMonitorSettingCAS(ctx, selectedModel, setting.Revision, ModelMonitorSettingWrite{
		Enabled: true, HistoryLimit: 24,
	}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Interval != DefaultProbeInterval || updated.HistoryLimit != 24 {
		t.Fatalf("updated setting = %+v", updated)
	}
	if _, err := storage.UpdateModelMonitorSettingCAS(ctx, selectedModel, setting.Revision, ModelMonitorSettingWrite{Enabled: false}, now); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale monitor update = %v", err)
	}
	views, err := storage.ListMonitorRouteViews(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Setting.PublishedModelID != selectedModel || len(views[0].Targets) != 2 {
		t.Fatalf("monitor views = %+v", views)
	}
	if views[0].Targets[0].ProviderModelTargetID != targets[0] || views[0].Targets[1].ProviderModelTargetID != targets[1] {
		t.Fatalf("target order = %+v", views[0].Targets)
	}
	if _, err := storage.GetModelMonitorSetting(ctx, unselectedModel); !errors.Is(err, ErrModelMonitorNotFound) {
		t.Fatalf("unselected model acquired monitor = %v", err)
	}
}
