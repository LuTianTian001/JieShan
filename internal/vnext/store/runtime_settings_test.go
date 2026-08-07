package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeSettingsCASPropagatesProbeIntervalAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "jieshan.sqlite")
	storage := openTestStoreAt(t, path)
	initializedAt := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	seed, err := storage.GetRuntimeSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defaults := DefaultRuntimeSettingsWrite()
	if seed.FailureThreshold != defaults.FailureThreshold || seed.FailureWindow != defaults.FailureWindow ||
		seed.Cooldown != defaults.Cooldown || seed.ProbeInterval != defaults.ProbeInterval ||
		seed.FirstOutputTimeout != defaults.FirstOutputTimeout || seed.StreamIdleTimeout != defaults.StreamIdleTimeout ||
		seed.RequestTimeout != defaults.RequestTimeout || seed.MaxAttempts != defaults.MaxAttempts ||
		seed.LogRetentionDays != defaults.LogRetentionDays || seed.Revision != 1 || !seed.UpdatedAt.IsZero() {
		t.Fatalf("migration seed drifted from Go defaults: %+v", seed)
	}

	initial, err := storage.InitializeRuntimeSettings(ctx, defaults, initializedAt)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Revision != 1 || initial.FailureThreshold != 2 || initial.FailureWindow != 5*time.Minute ||
		initial.Cooldown != 15*time.Minute || initial.ProbeInterval != 15*time.Minute ||
		initial.FirstOutputTimeout != 15*time.Second || initial.StreamIdleTimeout != time.Minute ||
		initial.RequestTimeout != 5*time.Minute || initial.MaxAttempts != 4 || initial.LogRetentionDays != 30 {
		t.Fatalf("initial settings = %+v", initial)
	}

	result, err := storage.DB.ExecContext(ctx, `INSERT INTO published_models(
public_name,official_price_sku,enabled,revision,created_at,updated_at) VALUES (?,?,?,?,?,?)`,
		"settings-test", "openai:gpt-5", 1, 1, initializedAt.UnixMilli(), initializedAt.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	modelID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	monitor, err := storage.CreateModelMonitorSetting(ctx, modelID, ModelMonitorSettingWrite{Enabled: true}, initializedAt)
	if err != nil {
		t.Fatal(err)
	}
	if monitor.Interval != 15*time.Minute {
		t.Fatalf("monitor default interval = %v", monitor.Interval)
	}

	write := DefaultRuntimeSettingsWrite()
	write.FailureThreshold = 3
	write.FailureWindow = 8 * time.Minute
	write.Cooldown = 2 * time.Minute
	write.ProbeInterval = 2 * time.Minute
	write.FirstOutputTimeout = 20 * time.Second
	write.StreamIdleTimeout = 45 * time.Second
	write.RequestTimeout = 4 * time.Minute
	write.MaxAttempts = 6
	write.LogRetentionDays = 45
	updatedAt := initializedAt.Add(time.Minute)
	updated, err := storage.UpdateRuntimeSettingsCAS(ctx, initial.Revision, write, updatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.FailureThreshold != 3 || updated.ProbeInterval != 2*time.Minute ||
		updated.UpdatedAt != updatedAt {
		t.Fatalf("updated settings = %+v", updated)
	}
	monitor, err = storage.GetModelMonitorSetting(ctx, modelID)
	if err != nil {
		t.Fatal(err)
	}
	if monitor.Interval != 2*time.Minute || monitor.Revision != 2 ||
		!monitor.NextProbeAt.Equal(updatedAt.Add(2*time.Minute)) {
		t.Fatalf("monitor after global settings update = %+v", monitor)
	}
	if _, err := storage.DB.ExecContext(ctx, `UPDATE model_monitor_settings SET interval_ms=60000 WHERE published_model_id=?`, modelID); err != nil {
		t.Fatal(err)
	}
	monitor, err = storage.GetModelMonitorSetting(ctx, modelID)
	if err != nil {
		t.Fatal(err)
	}
	if monitor.Interval != write.ProbeInterval {
		t.Fatalf("monitor read exposed stale materialized interval = %v", monitor.Interval)
	}
	if _, err := storage.UpdateRuntimeSettingsCAS(ctx, 1, write, updatedAt.Add(time.Minute)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale settings update error = %v", err)
	}

	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	persisted, err := reopened.GetRuntimeSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Revision != updated.Revision || persisted.FailureWindow != write.FailureWindow ||
		persisted.ProbeInterval != write.ProbeInterval || persisted.LogRetentionDays != write.LogRetentionDays {
		t.Fatalf("persisted settings = %+v", persisted)
	}
	var materializedIntervalMS int64
	if err := reopened.DB.QueryRowContext(ctx, `SELECT interval_ms FROM model_monitor_settings WHERE published_model_id=?`, modelID).Scan(&materializedIntervalMS); err != nil {
		t.Fatal(err)
	}
	if materializedIntervalMS != write.ProbeInterval.Milliseconds() {
		t.Fatalf("reopened materialized interval = %d", materializedIntervalMS)
	}
}

func TestRuntimeSettingsValidationAndSQLiteConstraintsAgree(t *testing.T) {
	storage := newTestStore(t)
	ctx := context.Background()
	valid := DefaultRuntimeSettingsWrite()

	tests := []struct {
		name   string
		mutate func(*RuntimeSettingsWrite)
	}{
		{"single failure cooldown", func(value *RuntimeSettingsWrite) { value.FailureThreshold = 1 }},
		{"short failure window", func(value *RuntimeSettingsWrite) { value.FailureWindow = time.Second }},
		{"short probe interval", func(value *RuntimeSettingsWrite) { value.ProbeInterval = 59 * time.Second }},
		{"request shorter than stream idle", func(value *RuntimeSettingsWrite) { value.RequestTimeout = 30 * time.Second }},
		{"too many attempts", func(value *RuntimeSettingsWrite) { value.MaxAttempts = 21 }},
		{"excessive retention", func(value *RuntimeSettingsWrite) { value.LogRetentionDays = 366 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			if err := ValidateRuntimeSettingsWrite(input); err == nil {
				t.Fatal("validation accepted invalid settings")
			}
		})
	}

	if _, err := storage.DB.ExecContext(ctx, `UPDATE runtime_settings SET request_timeout_ms=1000 WHERE singleton_id=1`); err == nil {
		t.Fatal("SQLite accepted a request timeout shorter than its component watchdogs")
	}
}
