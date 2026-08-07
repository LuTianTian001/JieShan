package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
)

func TestInventoryAdminCRUDUsesCASAndPreservesSecrets(t *testing.T) {
	ctx := context.Background()
	storage := newTestStore(t)
	siteID, err := storage.CreateSite(ctx, SiteWrite{Name: "Relay", DashboardURL: "https://relay.example/", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	sites, err := storage.ListSites(ctx)
	if err != nil || len(sites) != 1 || sites[0].DashboardURL != "https://relay.example" {
		t.Fatalf("ListSites() = %#v, %v", sites, err)
	}
	updatedSite, err := storage.UpdateSite(ctx, siteID, SiteUpdate{
		ExpectedRevision: sites[0].Revision, Name: "Relay Main", DashboardURL: "https://relay.example/panel/",
		Enabled: true, MaxInFlight: 7,
	})
	if err != nil || updatedSite.Revision != 2 || updatedSite.DashboardURL != "https://relay.example/panel" || updatedSite.MaxInFlight != 7 {
		t.Fatalf("UpdateSite() = %#v, %v", updatedSite, err)
	}
	if _, err := storage.UpdateSite(ctx, siteID, SiteUpdate{
		ExpectedRevision: 1, Name: "stale", Enabled: true, MaxInFlight: updatedSite.MaxInFlight,
	}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale UpdateSite() error = %v", err)
	}

	endpointID, err := storage.CreateSiteEndpoint(ctx, siteID, SiteEndpointWrite{
		Name: "Anthropic", BaseURL: "https://relay.example/anthropic", WireProtocol: "anthropic",
		Surface: "anthropic.messages", HeaderTemplate: []byte(`{"x-tenant":"one"}`), Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	targetID := mustCreateProviderTarget(t, storage, siteID, endpointID, "claude-source")
	endpoints, err := storage.ListSiteEndpoints(ctx, siteID)
	if err != nil || len(endpoints) != 1 || endpoints[0].AuthScheme != "x-api-key" {
		t.Fatalf("ListSiteEndpoints() = %#v, %v", endpoints, err)
	}
	updatedEndpoint, err := storage.UpdateSiteEndpoint(ctx, siteID, endpointID, SiteEndpointUpdate{
		ExpectedRevision: endpoints[0].Revision,
		Name:             "Anthropic primary",
		BaseURL:          endpoints[0].BaseURL,
		WireProtocol:     endpoints[0].WireProtocol,
		Surface:          endpoints[0].Surface,
		AdapterKind:      endpoints[0].AdapterKind,
		AuthScheme:       endpoints[0].AuthScheme,
		HeaderTemplate:   []byte(`{"x-tenant":"two"}`),
		Enabled:          true,
	})
	if err != nil || updatedEndpoint.Revision != 2 || string(updatedEndpoint.HeaderTemplate) != `{"x-tenant":"two"}` {
		t.Fatalf("UpdateSiteEndpoint() = %#v, %v", updatedEndpoint, err)
	}
	targetAfterEndpointUpdate, err := storage.GetProviderModelTarget(ctx, targetID)
	if err != nil || targetAfterEndpointUpdate.Revision != 2 {
		t.Fatalf("target revision after endpoint update = %#v, %v", targetAfterEndpointUpdate, err)
	}

	credentialID := mustCreateCredential(t, storage, siteID, "key-a")
	credentials, err := storage.ListSiteCredentials(ctx, siteID)
	if err != nil || len(credentials) != 1 || !credentials[0].SecretConfigured {
		t.Fatalf("ListSiteCredentials() = %#v, %v", credentials, err)
	}
	updatedCredential, err := storage.UpdateSiteCredential(ctx, siteID, credentialID, SiteCredentialUpdate{
		ExpectedRevision: credentials[0].Revision, Name: "key-primary", Enabled: true,
	})
	if err != nil || updatedCredential.Revision != 2 {
		t.Fatalf("UpdateSiteCredential() = %#v, %v", updatedCredential, err)
	}
	runtimeBeforeRotation, err := storage.GetCredentialRuntimeState(ctx, credentialID)
	if err != nil || runtimeBeforeRotation.Revision != 1 {
		t.Fatalf("metadata update changed runtime state = %#v, %v", runtimeBeforeRotation, err)
	}
	runtimeBeforeRotation, err = storage.UpdateCredentialRuntimeState(ctx, CredentialRuntimeUpdate{
		CredentialID: credentialID, ExpectedRevision: runtimeBeforeRotation.Revision, State: "invalid",
		LastHTTPStatus: intPointer(401), UpdatedAt: NowMS(),
	})
	if err != nil || runtimeBeforeRotation.State != "invalid" {
		t.Fatalf("mark credential invalid = %#v, %v", runtimeBeforeRotation, err)
	}
	rotatedCredential, err := storage.ReplaceSealedSiteCredentialSecret(ctx, siteID, credentialID, updatedCredential.Revision, 1, []byte("new-ciphertext"))
	if err != nil || rotatedCredential.Revision != 3 || !rotatedCredential.SecretConfigured {
		t.Fatalf("ReplaceSealedSiteCredentialSecret() = %#v, %v", rotatedCredential, err)
	}
	var storedCipher []byte
	if err := storage.DB.QueryRowContext(ctx, `SELECT secret_cipher FROM site_credentials WHERE id=?`, credentialID).Scan(&storedCipher); err != nil {
		t.Fatal(err)
	}
	if string(storedCipher) != "new-ciphertext" {
		t.Fatalf("stored cipher = %q", storedCipher)
	}
	runtimeState, err := storage.GetCredentialRuntimeState(ctx, credentialID)
	if err != nil || runtimeState.State != "active" || runtimeState.Revision != 3 {
		t.Fatalf("runtime after secret rotation = %#v, %v", runtimeState, err)
	}
}

func TestInventoryAdminImportsCatalogAndPersistsStrictRouteOrder(t *testing.T) {
	ctx := context.Background()
	storage := newTestStore(t)
	siteID := mustCreateSite(t, storage, "Relay")
	endpointID := mustCreateEndpoint(t, storage, siteID, "OpenAI", "https://relay.example/v1")
	credentialA := mustCreateCredential(t, storage, siteID, "a")
	credentialB := mustCreateCredential(t, storage, siteID, "b")
	endpoint, err := storage.GetSiteEndpoint(ctx, endpointID)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.ReplaceEndpointCredentialBindings(ctx, siteID, endpointID, endpoint.Revision, []int64{credentialB, credentialA}); err != nil {
		t.Fatal(err)
	}
	bindings, err := storage.ListEndpointCredentialBindings(ctx, endpointID)
	if err != nil || !reflect.DeepEqual(bindingCredentialIDs(bindings), []int64{credentialB, credentialA}) {
		t.Fatalf("bindings = %#v, %v", bindings, err)
	}

	imported, err := storage.ImportProviderModelTargets(ctx, siteID, endpointID, credentialB, []string{"model-b", "model-a", "model-b"}, 1234)
	if err != nil || len(imported) != 2 || imported[0].SourceModel != "model-b" || imported[1].SourceModel != "model-a" {
		t.Fatalf("ImportProviderModelTargets() = %#v, %v", imported, err)
	}
	if err := storage.ApplyCredentialModelDiscovery(ctx, siteID, endpointID, credentialA, nil, false, 2000); err != nil {
		t.Fatal(err)
	}
	modelA := imported[1]
	updatedTarget, err := storage.UpdateProviderModelTarget(ctx, siteID, endpointID, modelA.ID, ProviderModelTargetUpdate{
		ExpectedRevision: modelA.Revision, SourceModel: modelA.SourceModel, DisplayName: "Model A", Enabled: true,
	})
	if err != nil || updatedTarget.Revision != 2 || updatedTarget.DisplayName != "Model A" {
		t.Fatalf("UpdateProviderModelTarget() = %#v, %v", updatedTarget, err)
	}
	catalog, err := storage.ListProviderModelTargetInventory(ctx)
	if err != nil || len(catalog) != 2 {
		t.Fatalf("ListProviderModelTargetInventory() = %#v, %v", catalog, err)
	}
	for _, item := range catalog {
		if item.SiteName != "Relay" || item.EndpointName != "OpenAI" || item.BoundCredentialCount != 2 ||
			item.UsableCredentialCount != 2 || item.UnknownCredentialCount != 1 {
			t.Fatalf("catalog item = %#v", item)
		}
	}
	if err := storage.ApplyCredentialModelDiscovery(ctx, siteID, endpointID, credentialA, []string{"model-a"}, true, 3000); err != nil {
		t.Fatal(err)
	}
	catalog, err = storage.ListProviderModelTargetInventory(ctx)
	if err != nil || len(catalog) != 2 {
		t.Fatalf("ListProviderModelTargetInventory() after discovery = %#v, %v", catalog, err)
	}
	for _, item := range catalog {
		wantUsable := 1
		if item.SourceModel == "model-a" {
			wantUsable = 2
		}
		if item.UsableCredentialCount != wantUsable || item.UnknownCredentialCount != 0 {
			t.Fatalf("catalog item after discovery = %#v", item)
		}
	}

	model, err := storage.CreatePublishedModel(ctx, PublishedModelWrite{
		PublicName: "public-model", OfficialPriceSKU: "official-model", Enabled: true,
	}, []int64{imported[0].ID, imported[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(publishedTargetProviderIDs(model.Targets), []int64{imported[0].ID, imported[1].ID}) {
		t.Fatalf("initial published model = %#v", model)
	}
	updatedModel, err := storage.UpdatePublishedModel(ctx, model.ID, PublishedModelUpdate{
		ExpectedRevision: model.Revision, PublicName: "public-model-v2", OfficialPriceSKU: "official-model-v2", Enabled: true,
	})
	if err != nil || updatedModel.Revision != model.Revision+1 || updatedModel.PublicName != "public-model-v2" {
		t.Fatalf("UpdatePublishedModel() = %#v, %v", updatedModel, err)
	}
	reordered, err := storage.ReplacePublishedModelTargets(ctx, model.ID, updatedModel.Revision, []int64{imported[1].ID, imported[0].ID})
	if err != nil || !reflect.DeepEqual(publishedTargetProviderIDs(reordered.Targets), []int64{imported[1].ID, imported[0].ID}) {
		t.Fatalf("reordered published model = %#v, %v", reordered, err)
	}
	if _, err := storage.UpdatePublishedModel(ctx, model.ID, PublishedModelUpdate{
		ExpectedRevision: updatedModel.Revision, PublicName: "stale", OfficialPriceSKU: "stale", Enabled: true,
	}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale published model update error = %v", err)
	}
}

func TestDeleteSiteUsesCASAndRemovesOnlyItsRoutedTargets(t *testing.T) {
	ctx := context.Background()
	storage := newTestStore(t)
	siteA := mustCreateSite(t, storage, "Delete source A")
	endpointA := mustCreateEndpoint(t, storage, siteA, "A", "https://a.example/v1")
	credentialA := mustCreateCredential(t, storage, siteA, "key-a")
	endpointRecordA, err := storage.GetSiteEndpoint(ctx, endpointA)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.ReplaceEndpointCredentialBindings(ctx, siteA, endpointA, endpointRecordA.Revision, []int64{credentialA}); err != nil {
		t.Fatal(err)
	}
	targetA := mustCreateProviderTarget(t, storage, siteA, endpointA, "shared-model")

	siteB := mustCreateSite(t, storage, "Delete source B")
	endpointB := mustCreateEndpoint(t, storage, siteB, "B", "https://b.example/v1")
	targetB := mustCreateProviderTarget(t, storage, siteB, endpointB, "shared-model")

	model, err := storage.CreatePublishedModel(ctx, PublishedModelWrite{
		PublicName: "shared-model", OfficialPriceSKU: "shared-model", Enabled: true,
	}, []int64{targetA, targetB})
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := storage.CreatePublishedModel(ctx, PublishedModelWrite{
		PublicName: "orphan-model", OfficialPriceSKU: "orphan-model", Enabled: true,
	}, []int64{targetA})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := storage.CreateRoutingProfile(ctx, "Delete source override")
	if err != nil {
		t.Fatal(err)
	}
	route, err := storage.CreateRoutingProfileRoute(ctx, profile.ID, profile.Revision, RoutingProfileRouteWrite{
		PublishedModelID: model.ID,
		Enabled:          true,
		TargetIDs:        []int64{model.Targets[0].ID, model.Targets[1].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	profileAfterSharedRoute, err := storage.GetRoutingProfile(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	orphanRoute, err := storage.CreateRoutingProfileRoute(ctx, profile.ID, profileAfterSharedRoute.Revision, RoutingProfileRouteWrite{
		PublishedModelID: orphan.ID,
		Enabled:          true,
		TargetIDs:        []int64{orphan.Targets[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	profileBefore, err := storage.GetRoutingProfile(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	defaultBefore, err := storage.GetDefaultRoutingProfile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	site, err := storage.GetSite(ctx, siteA)
	if err != nil {
		t.Fatal(err)
	}

	if err := storage.DeleteSite(ctx, siteA, site.Revision+1); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale DeleteSite() error = %v", err)
	}
	if _, err := storage.GetSite(ctx, siteA); err != nil {
		t.Fatalf("stale delete removed site: %v", err)
	}
	if err := storage.DeleteSite(ctx, siteA, site.Revision); err != nil {
		t.Fatal(err)
	}

	if _, err := storage.GetSite(ctx, siteA); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted site lookup = %v", err)
	}
	for query, args := range map[string][]any{
		`SELECT 1 FROM site_endpoints WHERE site_id=?`:                 {siteA},
		`SELECT 1 FROM site_credentials WHERE site_id=?`:               {siteA},
		`SELECT 1 FROM provider_model_targets WHERE site_id=?`:         {siteA},
		`SELECT 1 FROM credential_runtime_state WHERE credential_id=?`: {credentialA},
	} {
		var exists int
		if err := storage.DB.QueryRowContext(ctx, query, args...).Scan(&exists); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("cascade query %q error = %v", query, err)
		}
	}

	modelAfter, err := storage.GetPublishedModel(ctx, model.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !modelAfter.Enabled || modelAfter.Revision != model.Revision+1 ||
		!reflect.DeepEqual(publishedTargetProviderIDs(modelAfter.Targets), []int64{targetB}) ||
		len(modelAfter.Targets) != 1 || modelAfter.Targets[0].Position != 0 {
		t.Fatalf("shared model after site delete = %+v", modelAfter)
	}
	orphanAfter, err := storage.GetPublishedModel(ctx, orphan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if orphanAfter.Enabled || orphanAfter.Revision != orphan.Revision+1 || len(orphanAfter.Targets) != 0 {
		t.Fatalf("orphan model after site delete = %+v", orphanAfter)
	}
	routeAfter, err := storage.GetRoutingProfileRoute(ctx, profile.ID, model.ID)
	if err != nil {
		t.Fatal(err)
	}
	if routeAfter.Revision != route.Revision+1 ||
		!reflect.DeepEqual(publishedTargetProviderIDs(routeAfter.Targets), []int64{targetB}) ||
		len(routeAfter.Targets) != 1 || routeAfter.Targets[0].Position != 0 {
		t.Fatalf("custom route after site delete = %+v", routeAfter)
	}
	orphanRouteAfter, err := storage.GetRoutingProfileRoute(ctx, profile.ID, orphan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if orphanRouteAfter.Enabled || orphanRouteAfter.Revision != orphanRoute.Revision+1 || len(orphanRouteAfter.Targets) != 0 {
		t.Fatalf("empty custom route after site delete = %+v", orphanRouteAfter)
	}
	profileAfter, err := storage.GetRoutingProfile(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if profileAfter.Revision != profileBefore.Revision+1 {
		t.Fatalf("custom profile revision = %d, want %d", profileAfter.Revision, profileBefore.Revision+1)
	}
	defaultAfter, err := storage.GetDefaultRoutingProfile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if defaultAfter.Revision != defaultBefore.Revision+1 {
		t.Fatalf("default profile revision = %d, want %d", defaultAfter.Revision, defaultBefore.Revision+1)
	}
	latest, err := storage.LatestConfigRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Reason != "site_deleted" {
		t.Fatalf("latest config revision = %+v", latest)
	}
	assertNoForeignKeyViolations(t, storage)
}

func intPointer(value int) *int {
	return &value
}

func bindingCredentialIDs(items []CredentialEndpointBinding) []int64 {
	result := make([]int64, 0, len(items))
	for _, item := range items {
		result = append(result, item.CredentialID)
	}
	return result
}

func publishedTargetProviderIDs(items []PublishedModelTarget) []int64 {
	result := make([]int64, 0, len(items))
	for _, item := range items {
		result = append(result, item.ProviderModelTargetID)
	}
	return result
}
