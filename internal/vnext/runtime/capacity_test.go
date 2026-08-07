package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/capacity"
	"github.com/LuTianTian001/JieShan/internal/vnext/runtimeconfig"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

func TestCapacityReloadServicePublishesSiteCASChanges(t *testing.T) {
	ctx := context.Background()
	storage, err := vnextstore.Open(ctx, filepath.Join(t.TempDir(), "capacity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	siteID, err := storage.CreateSite(ctx, vnextstore.SiteWrite{Name: "Relay", Enabled: true, MaxInFlight: 3})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := capacity.New(capacity.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	reloader, err := newCapacityReloadService(ctx, storage, manager, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	assertRuntimeSiteCapacity(t, manager, capacity.SiteID(siteID), 3)

	site, err := storage.GetSite(ctx, siteID)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := storage.UpdateSite(ctx, siteID, vnextstore.SiteUpdate{
		ExpectedRevision: site.Revision, Name: site.Name, DashboardURL: site.DashboardURL,
		Enabled: true, MaxInFlight: 6,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reloader.poller.Poll(ctx, func(ctx context.Context, _ runtimeconfig.RevisionEvent) error {
		return reloader.reload(ctx)
	}); err != nil {
		t.Fatal(err)
	}
	assertRuntimeSiteCapacity(t, manager, capacity.SiteID(siteID), 6)

	if _, err := storage.UpdateSite(ctx, siteID, vnextstore.SiteUpdate{
		ExpectedRevision: updated.Revision, Name: updated.Name, DashboardURL: updated.DashboardURL,
		Enabled: false, MaxInFlight: updated.MaxInFlight,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := reloader.poller.Poll(ctx, func(ctx context.Context, _ runtimeconfig.RevisionEvent) error {
		return reloader.reload(ctx)
	}); err != nil {
		t.Fatal(err)
	}
	if snapshot := manager.Snapshot(); len(snapshot.Sites) != 0 {
		t.Fatalf("disabled Site remains configured: %+v", snapshot)
	}
}

func assertRuntimeSiteCapacity(t *testing.T, manager *capacity.Manager, siteID capacity.SiteID, maximum int) {
	t.Helper()
	for _, site := range manager.Snapshot().Sites {
		if site.SiteID == siteID && site.MaxInFlight == maximum {
			return
		}
	}
	t.Fatalf("snapshot = %+v, want Site %d max %d", manager.Snapshot(), siteID, maximum)
}
