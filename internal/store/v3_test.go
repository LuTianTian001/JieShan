package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestLatestMigrationIsAdditive(t *testing.T) {
	s := newV3TestStore(t)
	ctx := context.Background()
	var version int
	if err := s.DB.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	wantVersion := migrations[len(migrations)-1].version
	if version != wantVersion {
		t.Fatalf("schema version = %d, want %d", version, wantVersion)
	}
	for _, table := range []string{
		"upstreams", "sites", "inference_endpoints", "inference_credentials", "site_models",
		"credential_model_access", "published_models", "route_site_targets", "route_site_target_health",
		"probe_runs", "probe_attempts", "model_discovery_runs", "model_discovery_attempts",
		"site_accounts", "site_account_snapshots", "site_account_usage_records",
		"legacy_upstream_site_mappings", "legacy_route_published_mappings",
		"routing_profiles", "routing_profile_model_targets",
	} {
		var found string
		if err := s.DB.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&found); err != nil {
			t.Fatalf("table %s: %v", table, err)
		}
	}
}

func TestV3SiteInventorySupportsMultipleCredentialsAndReorder(t *testing.T) {
	s := newV3TestStore(t)
	ctx := context.Background()
	siteID := mustCreateSite(t, s, "Ciii")

	site, _ := s.GetSite(ctx, siteID)
	primaryID, err := s.CreateInferenceEndpoint(ctx, siteID, site.Revision, InferenceEndpointWrite{
		Name: "Primary", BaseURL: "https://codex.ciii.club/v1/", WireProtocol: "OPENAI_CHAT_COMPLETIONS",
		CompatibilityProfile: "CIII", AuthScheme: "Bearer", CustomHeaders: json.RawMessage(`{"X-Client":"JieShan"}`), Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	site, _ = s.GetSite(ctx, siteID)
	secondaryID, err := s.CreateInferenceEndpoint(ctx, siteID, site.Revision, InferenceEndpointWrite{
		Name: "Secondary", BaseURL: "https://backup.ciii.club/v1", WireProtocol: "openai_chat_completions", Enabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}

	credentialIDs := make([]int64, 0, 3)
	for _, name := range []string{"Minis", "Host", "Shared"} {
		site, _ = s.GetSite(ctx, siteID)
		id, err := s.CreateInferenceCredential(ctx, siteID, site.Revision, InferenceCredentialWrite{
			Name: name, SecretCipher: []byte("cipher-" + name), Enabled: true,
		})
		if err != nil {
			t.Fatalf("create credential %s: %v", name, err)
		}
		credentialIDs = append(credentialIDs, id)
	}
	invalidAt := int64(1_000)
	if err := s.UpdateInferenceCredentialRuntime(ctx, credentialIDs[1], InferenceCredentialRuntimeUpdate{
		RuntimeState: "invalid", LastTestAt: &invalidAt, LastTestStatus: "failed", LastErrorMessage: "401 invalid key",
	}); err != nil {
		t.Fatal(err)
	}
	future := int64(20_000)
	if err := s.UpdateInferenceCredentialRuntime(ctx, credentialIDs[2], InferenceCredentialRuntimeUpdate{
		RuntimeState: "rate_limited", CooldownUntil: &future, LastTestStatus: "HTTP 429",
	}); err != nil {
		t.Fatal(err)
	}

	siteBeforeOrder, _ := s.GetSite(ctx, siteID)
	if err := s.ReorderInferenceEndpoints(ctx, siteID, siteBeforeOrder.Revision, []int64{secondaryID, primaryID}); err != nil {
		t.Fatal(err)
	}
	endpoints, err := s.ListInferenceEndpoints(ctx, siteID)
	if err != nil || len(endpoints) != 2 || endpoints[0].ID != secondaryID || endpoints[1].ID != primaryID {
		t.Fatalf("endpoint order = %+v, %v", endpoints, err)
	}
	if endpoints[1].BaseURL != "https://codex.ciii.club/v1" || endpoints[1].WireProtocol != "compatible" || endpoints[1].CompatibilityProfile != "ciii" {
		t.Fatalf("normalized endpoint = %+v", endpoints[1])
	}

	siteAfterEndpointOrder, _ := s.GetSite(ctx, siteID)
	if err := s.ReorderInferenceCredentials(ctx, siteID, siteAfterEndpointOrder.Revision, []int64{credentialIDs[2], credentialIDs[0], credentialIDs[1]}); err != nil {
		t.Fatal(err)
	}
	credentials, err := s.ListInferenceCredentials(ctx, siteID)
	if err != nil || len(credentials) != 3 || credentials[0].ID != credentialIDs[2] || credentials[1].ID != credentialIDs[0] {
		t.Fatalf("credential order = %+v, %v", credentials, err)
	}
	if err := s.ReorderInferenceCredentials(ctx, siteID, siteAfterEndpointOrder.Revision, []int64{credentialIDs[0], credentialIDs[1], credentialIDs[2]}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale reorder error = %v, want ErrRevisionConflict", err)
	}

	routable, err := s.ListRoutableInferenceCredentials(ctx, siteID, 10_000)
	if err != nil || len(routable) != 1 || routable[0].ID != credentialIDs[0] {
		t.Fatalf("routable before cooldown = %+v, %v", routable, err)
	}
	routable, err = s.ListRoutableInferenceCredentials(ctx, siteID, 30_000)
	if err != nil || len(routable) != 2 || routable[0].ID != credentialIDs[2] || routable[1].ID != credentialIDs[0] {
		t.Fatalf("routable after cooldown = %+v, %v", routable, err)
	}
	secret, err := s.GetInferenceCredentialSecret(ctx, credentialIDs[0])
	if err != nil || string(secret.SecretCipher) != "cipher-Minis" {
		t.Fatalf("credential secret = %q, %v", secret.SecretCipher, err)
	}

	summaries, err := s.ListSiteSummaries(ctx)
	if err != nil || len(summaries) != 1 {
		t.Fatalf("site summaries = %+v, %v", summaries, err)
	}
	summary := summaries[0]
	if summary.EndpointCount != 2 || summary.EnabledEndpointCount != 1 || summary.CredentialCount != 3 ||
		summary.EnabledCredentialCount != 3 || summary.UnavailableCredentialCount != 2 {
		t.Fatalf("site summary = %+v", summary)
	}
}

func TestV3ModelCatalogTracksPerCredentialCoverage(t *testing.T) {
	s := newV3TestStore(t)
	ctx := context.Background()
	siteID, endpointID, credentials := mustCreateSiteResources(t, s, "First", 2)
	seenAt := int64(5_000)
	modelID, err := s.CreateSiteModel(ctx, SiteModelWrite{
		SiteID: siteID, EndpointID: endpointID, ModelName: "gpt-test", Enabled: true, LastSeenAt: &seenAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCredentialModelAccess(ctx, CredentialModelAccessWrite{
		SiteID: siteID, CredentialID: credentials[0], SiteModelID: modelID, Availability: "supported", LastSeenAt: &seenAt, LastCheckedAt: &seenAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCredentialModelAccess(ctx, CredentialModelAccessWrite{
		SiteID: siteID, CredentialID: credentials[1], SiteModelID: modelID, Availability: "unsupported", LastCheckedAt: &seenAt,
	}); err != nil {
		t.Fatal(err)
	}
	coverage, err := s.ListSiteModelsWithCoverage(ctx, siteID)
	if err != nil || len(coverage) != 1 {
		t.Fatalf("coverage = %+v, %v", coverage, err)
	}
	if coverage[0].CredentialCount != 2 || coverage[0].SupportedCredentialCount != 1 ||
		coverage[0].UnsupportedCredentialCount != 1 || coverage[0].UnknownCredentialCount != 0 {
		t.Fatalf("coverage counts = %+v", coverage[0])
	}

	before, _ := s.GetSiteModel(ctx, modelID)
	upsertedID, err := s.UpsertSiteModel(ctx, SiteModelWrite{
		SiteID: siteID, EndpointID: endpointID, ModelName: "gpt-test", DisplayName: "GPT Test", Enabled: true, LastSeenAt: &seenAt,
	})
	if err != nil || upsertedID != modelID {
		t.Fatalf("UpsertSiteModel() = %d, %v", upsertedID, err)
	}
	after, _ := s.GetSiteModel(ctx, modelID)
	if after.Revision != before.Revision+1 || after.DisplayName != "GPT Test" {
		t.Fatalf("upserted model = %+v, before = %+v", after, before)
	}

	otherSiteID, otherEndpointID, _ := mustCreateSiteResources(t, s, "Other", 1)
	if _, err := s.CreateSiteModel(ctx, SiteModelWrite{
		SiteID: otherSiteID, EndpointID: endpointID, ModelName: "cross-site", Enabled: true,
	}); err == nil {
		t.Fatal("cross-site endpoint/model relation unexpectedly succeeded")
	}
	otherModelID, err := s.CreateSiteModel(ctx, SiteModelWrite{
		SiteID: otherSiteID, EndpointID: otherEndpointID, ModelName: "other-model", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCredentialModelAccess(ctx, CredentialModelAccessWrite{
		SiteID: otherSiteID, CredentialID: credentials[0], SiteModelID: otherModelID, Availability: "supported",
	}); err == nil {
		t.Fatal("cross-site credential/model access unexpectedly succeeded")
	}
}

func TestV3PublishedRouteOrdersSitesAndResolvesKeyPools(t *testing.T) {
	s := newV3TestStore(t)
	ctx := context.Background()
	firstSiteID, firstEndpointID, firstCredentials := mustCreateSiteResources(t, s, "First", 2)
	secondSiteID, secondEndpointID, secondCredentials := mustCreateSiteResources(t, s, "Second", 1)
	firstModelID, err := s.CreateSiteModel(ctx, SiteModelWrite{
		SiteID: firstSiteID, EndpointID: firstEndpointID, ModelName: "source-first", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondModelID, err := s.CreateSiteModel(ctx, SiteModelWrite{
		SiteID: secondSiteID, EndpointID: secondEndpointID, ModelName: "source-second", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCredentialModelAccess(ctx, CredentialModelAccessWrite{
		SiteID: firstSiteID, CredentialID: firstCredentials[0], SiteModelID: firstModelID, Availability: "unsupported",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCredentialModelAccess(ctx, CredentialModelAccessWrite{
		SiteID: firstSiteID, CredentialID: firstCredentials[1], SiteModelID: firstModelID, Availability: "supported",
	}); err != nil {
		t.Fatal(err)
	}

	publishedID, err := s.CreatePublishedModel(ctx, PublishedModelWrite{
		PublicName: "public-model", DisplayName: "Public Model", OfficialPriceSKU: "official/model", Enabled: true, MonitorEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	published, _ := s.GetPublishedModel(ctx, publishedID)
	firstTargetID, err := s.CreateRouteSiteTarget(ctx, publishedID, published.Revision, RouteSiteTargetWrite{
		SiteID: firstSiteID, EndpointID: firstEndpointID, SiteModelID: firstModelID, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	published, _ = s.GetPublishedModel(ctx, publishedID)
	secondTargetID, err := s.CreateRouteSiteTarget(ctx, publishedID, published.Revision, RouteSiteTargetWrite{
		SiteID: secondSiteID, EndpointID: secondEndpointID, SiteModelID: secondModelID, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishedBeforeDuplicate, _ := s.GetPublishedModel(ctx, publishedID)
	if _, err := s.CreateRouteSiteTarget(ctx, publishedID, publishedBeforeDuplicate.Revision, RouteSiteTargetWrite{
		SiteID: firstSiteID, EndpointID: firstEndpointID, SiteModelID: firstModelID, Enabled: true,
	}); err == nil {
		t.Fatal("second target for the same site unexpectedly succeeded")
	}
	publishedAfterDuplicate, _ := s.GetPublishedModel(ctx, publishedID)
	if publishedAfterDuplicate.Revision != publishedBeforeDuplicate.Revision {
		t.Fatalf("failed target insert changed revision: before=%d after=%d", publishedBeforeDuplicate.Revision, publishedAfterDuplicate.Revision)
	}

	resolved, err := s.ResolvePublishedModel(ctx, "public-model", NowMS())
	if err != nil || len(resolved.Targets) != 2 {
		t.Fatalf("ResolvePublishedModel() = %+v, %v", resolved, err)
	}
	if resolved.Targets[0].SiteID != firstSiteID || resolved.Targets[1].SiteID != secondSiteID {
		t.Fatalf("resolved target order = %+v", resolved.Targets)
	}
	if len(resolved.Targets[0].Credentials) != 1 || resolved.Targets[0].Credentials[0].ID != firstCredentials[1] {
		t.Fatalf("first site credential pool = %+v", resolved.Targets[0].Credentials)
	}
	if len(resolved.Targets[1].Credentials) != 1 || resolved.Targets[1].Credentials[0].ID != secondCredentials[0] {
		t.Fatalf("second site credential pool = %+v", resolved.Targets[1].Credentials)
	}

	revisionBeforeOrder := resolved.Revision
	if err := s.ReorderRouteSiteTargets(ctx, publishedID, revisionBeforeOrder, []int64{secondTargetID, firstTargetID}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReorderRouteSiteTargets(ctx, publishedID, revisionBeforeOrder, []int64{firstTargetID, secondTargetID}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale route reorder error = %v, want ErrRevisionConflict", err)
	}
	resolved, err = s.ResolvePublishedModel(ctx, "public-model", NowMS())
	if err != nil || resolved.Targets[0].ID != secondTargetID || resolved.Targets[1].ID != firstTargetID {
		t.Fatalf("resolved reordered route = %+v, %v", resolved.Targets, err)
	}
	routes, err := s.ListPublishedModelRoutes(ctx)
	if err != nil || len(routes) != 1 || len(routes[0].Targets) != 2 || routes[0].Targets[0].ID != secondTargetID {
		t.Fatalf("bulk published routes = %+v, %v", routes, err)
	}

	firstModel, _ := s.GetSiteModel(ctx, firstModelID)
	publishedBeforeCascade, _ := s.GetPublishedModel(ctx, publishedID)
	if err := s.DeleteSiteModel(ctx, firstModelID, firstModel.Revision); err != nil {
		t.Fatal(err)
	}
	publishedAfterCascade, _ := s.GetPublishedModel(ctx, publishedID)
	if publishedAfterCascade.Revision != publishedBeforeCascade.Revision+1 {
		t.Fatalf("cascade did not revise published model: before=%d after=%d", publishedBeforeCascade.Revision, publishedAfterCascade.Revision)
	}
	targets, err := s.ListRouteSiteTargets(ctx, publishedID)
	if err != nil || len(targets) != 1 || targets[0].ID != secondTargetID {
		t.Fatalf("targets after catalog delete = %+v, %v", targets, err)
	}
}

func newV3TestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "v3.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustCreateSite(t *testing.T, s *Store, name string) int64 {
	t.Helper()
	id, err := s.CreateSite(context.Background(), SiteWrite{Name: name, DashboardURL: "https://" + name + ".example.com/", Enabled: true})
	if err != nil {
		t.Fatalf("CreateSite(%s) error = %v", name, err)
	}
	return id
}

func mustCreateSiteResources(t *testing.T, s *Store, name string, credentialCount int) (int64, int64, []int64) {
	t.Helper()
	ctx := context.Background()
	siteID := mustCreateSite(t, s, name)
	site, _ := s.GetSite(ctx, siteID)
	endpointID, err := s.CreateInferenceEndpoint(ctx, siteID, site.Revision, InferenceEndpointWrite{
		Name: "Primary", BaseURL: "https://" + name + ".example.com/v1", WireProtocol: "openai_chat_completions", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateInferenceEndpoint(%s) error = %v", name, err)
	}
	credentials := make([]int64, 0, credentialCount)
	for index := 0; index < credentialCount; index++ {
		site, _ = s.GetSite(ctx, siteID)
		id, err := s.CreateInferenceCredential(ctx, siteID, site.Revision, InferenceCredentialWrite{
			Name: string(rune('A' + index)), SecretCipher: []byte{byte(index + 1)}, Enabled: true,
		})
		if err != nil {
			t.Fatalf("CreateInferenceCredential(%s/%d) error = %v", name, index, err)
		}
		credentials = append(credentials, id)
	}
	return siteID, endpointID, credentials
}

func TestV3MissingRowsReturnSQLNoRows(t *testing.T) {
	s := newV3TestStore(t)
	if _, err := s.GetSite(context.Background(), 999); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetSite() error = %v, want sql.ErrNoRows", err)
	}
}
