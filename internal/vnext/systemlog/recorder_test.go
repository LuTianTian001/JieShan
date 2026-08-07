package systemlog

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecorderRedactsSensitiveFieldsAndFiltersEvents(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	recorder, err := New(Options{MemoryEntries: 8, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
	logger := slog.New(recorder)
	logger.Info("probe completed", "module", "monitor", "code", "probe_ok", "task_id", "probe-7",
		"authorization", "Bearer exposed", "input_tokens", 3)
	logger.Error("route failed", "module", "routing", "code", "upstream_timeout", "request_id", "req-9")

	page, err := recorder.List(Filter{Module: "monitor", Search: "probe", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].TaskID != "probe-7" || page.Items[0].Code != "probe_ok" {
		t.Fatalf("filtered events = %+v", page.Items)
	}
	if page.Items[0].Fields["authorization"] != redactedValue {
		t.Fatalf("authorization was not redacted: %+v", page.Items[0].Fields)
	}
	if page.Items[0].Fields["input_tokens"] != int64(3) {
		t.Fatalf("non-sensitive token count was altered: %+v", page.Items[0].Fields)
	}

	latest, err := recorder.List(Filter{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(latest.Items) != 1 || latest.Items[0].RequestID != "req-9" || !latest.HasMore || latest.NextBefore == 0 {
		t.Fatalf("latest page = %+v", latest)
	}
	older, err := recorder.List(Filter{Limit: 10, Before: latest.NextBefore})
	if err != nil {
		t.Fatal(err)
	}
	if len(older.Items) != 1 || older.Items[0].TaskID != "probe-7" {
		t.Fatalf("older page = %+v", older)
	}
}

func TestRecorderRotatesJSONLAndKeepsMemoryAvailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "system.jsonl")
	recorder, err := New(Options{Path: path, MemoryEntries: 4, MaxFileBytes: 180, MaxFiles: 2})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(recorder)
	for index := 0; index < 8; index++ {
		logger.Info("rotation payload "+strings.Repeat("x", 40), "module", "runtime", "index", index)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("active JSONL log is missing: %v", err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("rotated JSONL log is missing: %v", err)
	}
	page, err := recorder.List(Filter{Limit: 4})
	if err != nil || len(page.Items) != 4 {
		t.Fatalf("closed recorder memory page = %+v, %v", page, err)
	}
}

func TestTeeForwardsRecordsToEveryEnabledHandler(t *testing.T) {
	left, _ := New(Options{MemoryEntries: 2})
	right, _ := New(Options{MemoryEntries: 2})
	t.Cleanup(func() { _ = left.Close(); _ = right.Close() })
	logger := slog.New(Tee(left, right))
	logger.Log(context.Background(), slog.LevelWarn, "shared", "module", "runtime")
	for name, recorder := range map[string]*Recorder{"left": left, "right": right} {
		page, err := recorder.List(Filter{Limit: 2})
		if err != nil || len(page.Items) != 1 || page.Items[0].Level != "warn" {
			t.Fatalf("%s recorder = %+v, %v", name, page, err)
		}
	}
}
