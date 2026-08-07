package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestDeletePublishedModelUsesRevisionAndCascadesSparseRoutes(t *testing.T) {
	storage := newTestStore(t)
	ctx := context.Background()
	siteID := mustCreateSite(t, storage, "Delete model upstream")
	endpointID := mustCreateEndpoint(t, storage, siteID, "Delete endpoint", "https://delete.example/v1")
	targetID := mustCreateProviderTarget(t, storage, siteID, endpointID, "delete-model")
	model, err := storage.CreatePublishedModel(ctx, PublishedModelWrite{
		PublicName: "delete-model", OfficialPriceSKU: "delete-model", Enabled: true,
	}, []int64{targetID})
	if err != nil {
		t.Fatal(err)
	}
	custom, err := storage.CreateRoutingProfile(ctx, "Delete override")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.CreateRoutingProfileRoute(ctx, custom.ID, custom.Revision, RoutingProfileRouteWrite{
		PublishedModelID: model.ID, Enabled: false, TargetIDs: []int64{model.Targets[0].ID},
	}); err != nil {
		t.Fatal(err)
	}
	defaultBefore, err := storage.GetDefaultRoutingProfile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.DeletePublishedModel(ctx, model.ID, model.Revision+1); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale delete error = %v", err)
	}
	if _, err := storage.GetPublishedModel(ctx, model.ID); err != nil {
		t.Fatalf("stale delete removed model: %v", err)
	}

	if err := storage.DeletePublishedModel(ctx, model.ID, model.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.GetPublishedModel(ctx, model.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted model lookup = %v", err)
	}
	if _, err := storage.GetRoutingProfileRoute(ctx, custom.ID, model.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted sparse route lookup = %v", err)
	}
	defaultAfter, err := storage.GetDefaultRoutingProfile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if defaultAfter.Revision != defaultBefore.Revision+1 || defaultAfter.ModelCount != 0 {
		t.Fatalf("default profile after delete = %+v", defaultAfter)
	}
	customAfter, err := storage.GetRoutingProfile(ctx, custom.ID)
	if err != nil {
		t.Fatal(err)
	}
	if customAfter.ModelCount != 0 || customAfter.LocalModelCount != 0 || customAfter.InheritedModelCount != 0 {
		t.Fatalf("custom profile after model delete = %+v", customAfter)
	}
	assertNoForeignKeyViolations(t, storage)
}
