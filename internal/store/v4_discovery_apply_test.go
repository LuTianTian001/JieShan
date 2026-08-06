package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestApplyModelDiscoveryRunChecksRevisionAndAppliesAtomically(t *testing.T) {
	s := newV3TestStore(t)
	ctx := context.Background()
	siteID, endpointID, credentials := mustCreateSiteResources(t, s, "Discovery apply", 2)
	site, err := s.GetSite(ctx, siteID)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := s.GetInferenceEndpoint(ctx, endpointID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InsertModelDiscoveryRun(ctx, ModelDiscoveryRun{
		ID: "apply-success", SiteID: siteID, EndpointID: endpointID, Mode: "all",
		BaseSiteRevision: site.Revision, BaseEndpointRevision: endpoint.Revision,
		CredentialCount: 2, StartedAt: 1_000,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyModelDiscoveryRun(ctx, "apply-success", []ModelDiscoveryCredentialModels{
		{CredentialID: credentials[0], Models: []string{"model-a"}},
		{CredentialID: credentials[1], Models: []string{"model-a", "model-b"}},
	}, []string{"model-b", "model-a", "model-a"}, 2_000); err != nil {
		t.Fatal(err)
	}
	models, err := s.ListEndpointModels(ctx, endpointID)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ModelName != "model-a" || models[1].ModelName != "model-b" {
		t.Fatalf("applied models = %+v", models)
	}
	firstAccess, err := s.GetCredentialModelAccess(ctx, credentials[0], models[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if firstAccess.Availability != "unknown" || firstAccess.MissingCount != 1 {
		t.Fatalf("first credential model-b access = %+v", firstAccess)
	}
	secondAccess, err := s.GetCredentialModelAccess(ctx, credentials[1], models[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if secondAccess.Availability != "supported" || secondAccess.MissingCount != 0 || secondAccess.LastSeenAt == nil {
		t.Fatalf("second credential model-b access = %+v", secondAccess)
	}
	if err := s.InsertModelDiscoveryRun(ctx, ModelDiscoveryRun{
		ID: "apply-success-again", SiteID: siteID, EndpointID: endpointID, Mode: "all",
		BaseSiteRevision: site.Revision, BaseEndpointRevision: endpoint.Revision,
		CredentialCount: 2, StartedAt: 2_100,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyModelDiscoveryRun(ctx, "apply-success-again", []ModelDiscoveryCredentialModels{
		{CredentialID: credentials[0], Models: []string{"model-a"}},
		{CredentialID: credentials[1], Models: []string{"model-a", "model-b"}},
	}, []string{"model-a", "model-b"}, 2_200); err != nil {
		t.Fatal(err)
	}
	firstAccess, err = s.GetCredentialModelAccess(ctx, credentials[0], models[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if firstAccess.Availability != "unsupported" || firstAccess.MissingCount != 2 {
		t.Fatalf("second missing observation did not mark unsupported: %+v", firstAccess)
	}

	site, _ = s.GetSite(ctx, siteID)
	endpoint, _ = s.GetInferenceEndpoint(ctx, endpointID)
	if err := s.InsertModelDiscoveryRun(ctx, ModelDiscoveryRun{
		ID: "apply-conflict", SiteID: siteID, EndpointID: endpointID, Mode: "selected",
		BaseSiteRevision: site.Revision, BaseEndpointRevision: endpoint.Revision,
		CredentialCount: 1, StartedAt: 3_000,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateInferenceEndpoint(ctx, endpointID, endpoint.Revision, InferenceEndpointWrite{
		Name: endpoint.Name + " changed", BaseURL: endpoint.BaseURL, WireProtocol: endpoint.WireProtocol,
		CompatibilityProfile: endpoint.CompatibilityProfile, AuthScheme: endpoint.AuthScheme,
		CustomHeaders: endpoint.CustomHeaders, Enabled: endpoint.Enabled,
	}); err != nil {
		t.Fatal(err)
	}
	err = s.ApplyModelDiscoveryRun(ctx, "apply-conflict", []ModelDiscoveryCredentialModels{
		{CredentialID: credentials[0], Models: []string{"must-not-apply"}},
	}, []string{"must-not-apply"}, 4_000)
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale apply error = %v, want ErrRevisionConflict", err)
	}
	models, err = s.ListEndpointModels(ctx, endpointID)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("revision conflict changed catalog: %+v", models)
	}
	run, err := s.FailModelDiscoveryRun(ctx, "apply-conflict", 1,
		json.RawMessage(`{"outcome":"configuration_conflict"}`), "configuration changed", 4_100)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" || run.ErrorMessage == "" {
		t.Fatalf("failed discovery run = %+v", run)
	}
}
