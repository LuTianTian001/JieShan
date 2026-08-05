package app

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/config"
	"github.com/LuTianTian001/JieShan/internal/store"
)

func TestNewRecoversInterruptedRequestsBeforeServing(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	databasePath := filepath.Join(dataDir, "jieshan.db")
	database, err := store.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	now := store.NowMS()
	result, err := database.DB.ExecContext(ctx, `INSERT INTO routes(public_model,created_at,updated_at) VALUES (?,?,?)`, "test-model", now, now)
	if err != nil {
		t.Fatal(err)
	}
	routeID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	quota := int64(100)
	keyID, err := database.CreateDownstreamKey(ctx, store.DownstreamKeyWrite{Name: "test", Enabled: true, QuotaMicroUSD: &quota}, "js_test", "js_test_app_recovery")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertAdmin(ctx, "admin", "preexisting-test-hash"); err != nil {
		t.Fatal(err)
	}
	if err := database.StartRequestWithReservation(ctx, store.RequestStart{
		ID:              "interrupted-at-startup",
		DownstreamKeyID: keyID,
		RouteID:         routeID,
		RouteRevision:   1,
		RequestedModel:  "test-model",
		StartedAt:       now,
	}, 60, true); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	application, err := New(ctx, config.Config{
		ListenAddr:      "127.0.0.1:0",
		DataDir:         dataDir,
		DatabasePath:    databasePath,
		WebDir:          filepath.Join(dataDir, "web"),
		SessionTTL:      time.Hour,
		UpstreamTimeout: time.Second,
	}, logger)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = application.store.Close() })

	key, err := application.store.GetDownstreamKey(ctx, keyID)
	if err != nil {
		t.Fatal(err)
	}
	if key.ReservedMicroUSD != 0 {
		t.Fatalf("reserved quota after App.New = %d, want 0", key.ReservedMicroUSD)
	}
	item, _, err := application.store.GetRequestLog(ctx, "interrupted-at-startup")
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != "failed" || item.FinishedAt == nil {
		t.Fatalf("request after App.New = %+v", item)
	}
}
