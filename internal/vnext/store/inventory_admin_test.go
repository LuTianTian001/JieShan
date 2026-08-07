package store

import (
	"context"
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
		ExpectedRevision: sites[0].Revision, Name: "Relay Main", DashboardURL: "https://relay.example/panel/", Enabled: true,
	})
	if err != nil || updatedSite.Revision != 2 || updatedSite.DashboardURL != "https://relay.example/panel" {
		t.Fatalf("UpdateSite() = %#v, %v", updatedSite, err)
	}
	if _, err := storage.UpdateSite(ctx, siteID, SiteUpdate{
		ExpectedRevision: 1, Name: "stale", Enabled: true,
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
