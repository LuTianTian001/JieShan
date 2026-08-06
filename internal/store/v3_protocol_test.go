package store

import (
	"context"
	"strings"
	"testing"
)

func TestV3EndpointProtocolCapabilitiesAreExplicit(t *testing.T) {
	s := newV3TestStore(t)
	ctx := context.Background()
	siteID := mustCreateSite(t, s, "Protocol")

	site, _ := s.GetSite(ctx, siteID)
	compatibleID, err := s.CreateInferenceEndpoint(ctx, siteID, site.Revision, InferenceEndpointWrite{
		Name: "Compatible", BaseURL: "https://protocol.example.com/v1", WireProtocol: "OPENAI_CHAT_COMPLETIONS", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	compatible, err := s.GetInferenceEndpoint(ctx, compatibleID)
	if err != nil {
		t.Fatal(err)
	}
	if compatible.WireProtocol != "compatible" || compatible.AuthScheme != "bearer" || !compatible.Capabilities.RouteEligible || !compatible.Capabilities.Responses {
		t.Fatalf("compatible endpoint = %+v", compatible)
	}

	site, _ = s.GetSite(ctx, siteID)
	anthropicID, err := s.CreateInferenceEndpoint(ctx, siteID, site.Revision, InferenceEndpointWrite{
		Name: "Native Anthropic", BaseURL: "https://api.anthropic.com", WireProtocol: "anthropic", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	anthropic, err := s.GetInferenceEndpoint(ctx, anthropicID)
	if err != nil {
		t.Fatal(err)
	}
	if anthropic.AuthScheme != "x-api-key" || !anthropic.Capabilities.ModelDiscovery || anthropic.Capabilities.RouteEligible || anthropic.Capabilities.ChatCompletions || anthropic.Capabilities.Responses {
		t.Fatalf("Anthropic endpoint = %+v", anthropic)
	}

	site, _ = s.GetSite(ctx, siteID)
	if _, err := s.CreateInferenceEndpoint(ctx, siteID, site.Revision, InferenceEndpointWrite{
		Name: "Unknown", BaseURL: "https://unknown.example.com", WireProtocol: "native-magic", Enabled: true,
	}); err == nil || !strings.Contains(err.Error(), "unsupported inference protocol") {
		t.Fatalf("unknown protocol error = %v", err)
	}
}

func TestV3RouteTargetValidatesRelationshipsAndProtocol(t *testing.T) {
	s := newV3TestStore(t)
	ctx := context.Background()
	siteID := mustCreateSite(t, s, "Route Validation")
	site, _ := s.GetSite(ctx, siteID)
	primaryID, err := s.CreateInferenceEndpoint(ctx, siteID, site.Revision, InferenceEndpointWrite{
		Name: "Primary", BaseURL: "https://route.example.com/v1", WireProtocol: "compatible", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	site, _ = s.GetSite(ctx, siteID)
	secondaryID, err := s.CreateInferenceEndpoint(ctx, siteID, site.Revision, InferenceEndpointWrite{
		Name: "Secondary", BaseURL: "https://route.example.com/secondary/v1", WireProtocol: "compatible", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	site, _ = s.GetSite(ctx, siteID)
	nativeID, err := s.CreateInferenceEndpoint(ctx, siteID, site.Revision, InferenceEndpointWrite{
		Name: "Native", BaseURL: "https://api.anthropic.com", WireProtocol: "anthropic", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	primaryModelID, err := s.CreateSiteModel(ctx, SiteModelWrite{SiteID: siteID, EndpointID: primaryID, ModelName: "primary-model", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	secondaryModelID, err := s.CreateSiteModel(ctx, SiteModelWrite{SiteID: siteID, EndpointID: secondaryID, ModelName: "secondary-model", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	nativeModelID, err := s.CreateSiteModel(ctx, SiteModelWrite{SiteID: siteID, EndpointID: nativeID, ModelName: "claude-native", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	publishedID, err := s.CreatePublishedModel(ctx, PublishedModelWrite{PublicName: "public-route", Enabled: true, MonitorEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	published, _ := s.GetPublishedModel(ctx, publishedID)
	if _, err := s.CreateRouteSiteTarget(ctx, publishedID, published.Revision, RouteSiteTargetWrite{
		SiteID: siteID, EndpointID: primaryID, SiteModelID: secondaryModelID, Enabled: true,
	}); err == nil || !strings.Contains(err.Error(), "same site and endpoint") {
		t.Fatalf("cross-endpoint target error = %v", err)
	}
	afterMismatch, _ := s.GetPublishedModel(ctx, publishedID)
	if afterMismatch.Revision != published.Revision {
		t.Fatalf("failed relationship validation changed revision: before=%d after=%d", published.Revision, afterMismatch.Revision)
	}

	if _, err := s.CreateRouteSiteTarget(ctx, publishedID, published.Revision, RouteSiteTargetWrite{
		SiteID: siteID, EndpointID: nativeID, SiteModelID: nativeModelID, Enabled: true,
	}); err == nil || !strings.Contains(err.Error(), "model discovery only") {
		t.Fatalf("native route error = %v", err)
	}

	targetID, err := s.CreateRouteSiteTarget(ctx, publishedID, published.Revision, RouteSiteTargetWrite{
		SiteID: siteID, EndpointID: primaryID, SiteModelID: primaryModelID, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := s.GetRouteSiteTarget(ctx, targetID)
	published, _ = s.GetPublishedModel(ctx, publishedID)
	if err := s.UpdateRouteSiteTarget(ctx, targetID, target.Revision, published.Revision, RouteSiteTargetWrite{
		SiteID: siteID, EndpointID: primaryID, SiteModelID: secondaryModelID, Enabled: true,
	}); err == nil || !strings.Contains(err.Error(), "same site and endpoint") {
		t.Fatalf("invalid target update error = %v", err)
	}

	primary, _ := s.GetInferenceEndpoint(ctx, primaryID)
	if err := s.UpdateInferenceEndpoint(ctx, primaryID, primary.Revision, InferenceEndpointWrite{
		Name: primary.Name, BaseURL: primary.BaseURL, WireProtocol: "anthropic", Enabled: primary.Enabled,
	}); err == nil || !strings.Contains(err.Error(), "remove those route targets first") {
		t.Fatalf("routed endpoint protocol change error = %v", err)
	}
}

func TestV3NativeProtocolLegacyRowsAreNeverResolvedForGateway(t *testing.T) {
	s := newV3TestStore(t)
	ctx := context.Background()
	siteID := mustCreateSite(t, s, "Native Legacy")
	site, _ := s.GetSite(ctx, siteID)
	endpointID, err := s.CreateInferenceEndpoint(ctx, siteID, site.Revision, InferenceEndpointWrite{
		Name: "Gemini", BaseURL: "https://generativelanguage.googleapis.com", WireProtocol: "gemini", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	modelID, err := s.CreateSiteModel(ctx, SiteModelWrite{SiteID: siteID, EndpointID: endpointID, ModelName: "gemini-native", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	publishedID, err := s.CreatePublishedModel(ctx, PublishedModelWrite{PublicName: "native-history", Enabled: true, MonitorEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := NowMS()
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO route_site_targets(
published_model_id,site_id,endpoint_id,site_model_id,position,enabled,revision,created_at,updated_at)
VALUES (?,?,?,?,0,1,1,?,?)`, publishedID, siteID, endpointID, modelID, now, now); err != nil {
		t.Fatal(err)
	}

	resolved, err := s.ResolvePublishedModel(ctx, "native-history", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Targets) != 0 {
		t.Fatalf("native legacy targets resolved for gateway: %+v", resolved.Targets)
	}
	matrix, err := s.ListPublishedModelMonitorMatrix(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(matrix) != 1 || len(matrix[0].Targets) != 1 || matrix[0].Targets[0].Health.CapabilityState != "unsupported" || matrix[0].Targets[0].Health.LastErrorClass != "unsupported_protocol" {
		t.Fatalf("native monitor target = %+v", matrix)
	}
}
