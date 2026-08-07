package vnextmigration

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestPreviewMaterializesV3BeforeLegacyAndAppliesProfile(t *testing.T) {
	db, _ := newFixtureDatabase(t)
	seedRoutingFixture(t, db)
	mustExec(t, db, `INSERT INTO downstream_keys(id,name,key_prefix,enabled,allowed_models_json,routing_profile_id)
VALUES (1,'Primary','sk-old',1,'["gpt-4o","legacy-only","ghost"]',7)`)

	report, err := PreviewDatabase(context.Background(), db, Options{NowMS: 10_000})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Keys) != 1 {
		t.Fatalf("keys = %d, want 1", len(report.Keys))
	}
	key := report.Keys[0]
	if key.SecretRevealable || key.NonRevealableReason == "" {
		t.Fatalf("key reveal state = %+v", key)
	}
	if len(key.Models) != 2 {
		t.Fatalf("models = %+v", key.Models)
	}
	gpt := findModel(t, key, "gpt-4o")
	if gpt.Generation != generationV3 || gpt.ResolutionSource != resolutionV3Profile || gpt.ImplicitInheritance {
		t.Fatalf("gpt route = %+v", gpt)
	}
	if gpt.ShadowedLegacyRouteID == nil || *gpt.ShadowedLegacyRouteID != 21 {
		t.Fatalf("shadowed legacy route = %v, want 21", gpt.ShadowedLegacyRouteID)
	}
	if len(gpt.Targets) != 1 || gpt.Targets[0].SiteName != "Site B" || gpt.Targets[0].Position != 0 || !gpt.Targets[0].Routable {
		t.Fatalf("profile target = %+v", gpt.Targets)
	}
	if !gpt.Targets[0].ProtocolMappingAmbiguous || gpt.Targets[0].Surface != "" ||
		len(gpt.Targets[0].SurfaceCandidates) != 2 || !hasIssue(gpt.Targets[0].Issues, "protocol_surface_ambiguous") {
		t.Fatalf("ambiguous protocol mapping = %+v", gpt.Targets[0])
	}
	if !gpt.ExplicitPriceSKUMissing || !hasIssue(gpt.Issues, "price_sku_missing") {
		t.Fatalf("missing price SKU was not reported: %+v", gpt)
	}
	legacy := findModel(t, key, "legacy-only")
	if legacy.Generation != generationLegacy || legacy.ResolutionSource != resolutionLegacyGlobal || !legacy.ImplicitInheritance || !legacy.Routable {
		t.Fatalf("legacy route = %+v", legacy)
	}
	if len(key.UnresolvedAllowlist) != 1 || key.UnresolvedAllowlist[0] != "ghost" {
		t.Fatalf("unresolved allowlist = %+v", key.UnresolvedAllowlist)
	}
	if excluded := findExcluded(key, "v3-unlisted"); excluded == nil || !containsString(excluded.Reasons, "not_allowed") {
		t.Fatalf("allowlist exclusion = %+v", key.ExcludedModels)
	}
}

func TestPreviewFlagsProfileDefaultAndMissingProfileFailOpen(t *testing.T) {
	db, _ := newFixtureDatabase(t)
	seedRoutingFixture(t, db)
	mustExec(t, db, `INSERT INTO downstream_keys(id,name,key_prefix,enabled,allowed_models_json,routing_profile_id) VALUES
(1,'No model override','sk-a',1,'["v3-unlisted"]',7),
(2,'Dangling profile','sk-b',1,'["gpt-4o"]',99)`)

	report, err := PreviewDatabase(context.Background(), db, Options{NowMS: 10_000})
	if err != nil {
		t.Fatal(err)
	}
	first := findKey(t, report, 1)
	model := findModel(t, first, "v3-unlisted")
	if model.ResolutionSource != resolutionV3ProfileDefaultFallback || !model.ImplicitInheritance ||
		!hasIssue(model.Issues, "routing_profile_model_fail_open") {
		t.Fatalf("model default fallback = %+v", model)
	}
	if len(model.Targets) != 1 || model.Targets[0].SiteName != "Site A" {
		t.Fatalf("default target order = %+v", model.Targets)
	}
	if model.Targets[0].WireProtocol != "openai" || model.Targets[0].Surface != "openai.chat_completions" ||
		model.Targets[0].ProtocolMappingAmbiguous {
		t.Fatalf("exact protocol mapping = %+v", model.Targets[0])
	}

	second := findKey(t, report, 2)
	model = findModel(t, second, "gpt-4o")
	if model.ResolutionSource != resolutionV3MissingProfileFallback || !model.ImplicitInheritance ||
		!hasIssue(model.Issues, "routing_profile_missing_fail_open") || !hasIssue(second.Issues, "routing_profile_missing_fail_open") {
		t.Fatalf("missing profile fallback = key %+v model %+v", second, model)
	}
	if len(model.Targets) != 2 || model.Targets[0].SiteName != "Site A" || model.Targets[1].SiteName != "Site B" {
		t.Fatalf("missing-profile default targets = %+v", model.Targets)
	}
}

func TestPreviewReportsMissingEndpointCredentialAndLegacyTarget(t *testing.T) {
	db, _ := newFixtureDatabase(t)
	seedRoutingFixture(t, db)
	mustExec(t, db, `INSERT INTO sites(id,name,enabled) VALUES (3,'Broken Site',1);
INSERT INTO site_models(id,site_id,endpoint_id,model_name,enabled) VALUES (3101,3,3999,'broken-source',1);
INSERT INTO published_models(id,public_name,official_price_sku,enabled,revision) VALUES (13,'broken-v3',NULL,1,1);
INSERT INTO route_site_targets(id,published_model_id,site_id,endpoint_id,site_model_id,position,enabled)
VALUES (131,13,3,3999,3101,0,1);
INSERT INTO routes(id,public_model,enabled,revision) VALUES (23,'broken-legacy',1,1);
INSERT INTO upstream_models(id,upstream_id,model_name,enabled) VALUES (3103,31,'broken-legacy',1);
INSERT INTO route_targets(id,route_id,upstream_model_id,endpoint_id,credential_id,position,enabled)
VALUES (2301,23,3103,9998,9999,0,1);
INSERT INTO downstream_keys(id,name,key_prefix,enabled,allowed_models_json,routing_profile_id)
VALUES (1,'Broken routes','sk-broken',1,'["broken-v3","broken-legacy"]',NULL)`)

	report, err := PreviewDatabase(context.Background(), db, Options{NowMS: 10_000})
	if err != nil {
		t.Fatal(err)
	}
	key := report.Keys[0]
	v3 := findModel(t, key, "broken-v3")
	if v3.Routable || len(v3.Targets) != 1 || !v3.Targets[0].EndpointMissing || !v3.Targets[0].CredentialMissing ||
		!hasIssue(v3.Targets[0].Issues, "endpoint_missing") || !hasIssue(v3.Targets[0].Issues, "credential_missing") {
		t.Fatalf("broken V3 target = %+v", v3)
	}
	if !v3.ExplicitPriceSKUMissing {
		t.Fatalf("broken V3 price state = %+v", v3)
	}
	legacy := findModel(t, key, "broken-legacy")
	if legacy.Routable || len(legacy.Targets) != 1 || !legacy.Targets[0].EndpointMissing || !legacy.Targets[0].CredentialMissing ||
		!hasIssue(legacy.Targets[0].Issues, "endpoint_missing") || !hasIssue(legacy.Targets[0].Issues, "credential_missing") {
		t.Fatalf("broken legacy target = %+v", legacy)
	}
}

func TestPreviewInvalidAllowedModelsMatchesLegacyFailOpen(t *testing.T) {
	db, _ := newFixtureDatabase(t)
	seedRoutingFixture(t, db)
	mustExec(t, db, `INSERT INTO downstream_keys(id,name,key_prefix,enabled,allowed_models_json)
VALUES (1,'Invalid allowlist','sk-invalid',1,'not-json')`)

	report, err := PreviewDatabase(context.Background(), db, Options{NowMS: 10_000})
	if err != nil {
		t.Fatal(err)
	}
	key := report.Keys[0]
	if key.AllowedModelsMode != "all" || len(key.Models) != 3 || !hasIssue(key.Issues, "allowed_models_invalid_fail_open") {
		t.Fatalf("invalid allowlist result = %+v", key)
	}
}

func TestPreviewSQLiteFileUsesExistingDatabaseWithoutCreatingMissingFile(t *testing.T) {
	db, path := newFixtureDatabase(t)
	seedRoutingFixture(t, db)
	mustExec(t, db, `INSERT INTO downstream_keys(id,name,key_prefix,enabled,allowed_models_json) VALUES (1,'File key','sk-file',1,'[]')`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	report, err := PreviewSQLiteFile(context.Background(), path, Options{NowMS: 10_000})
	if err != nil {
		t.Fatal(err)
	}
	wantPath, _ := filepath.Abs(path)
	if report.Source.Path != wantPath || len(report.Keys) != 1 {
		t.Fatalf("file preview = %+v", report)
	}

	missing := filepath.Join(t.TempDir(), "does-not-exist.db")
	if _, err := PreviewSQLiteFile(context.Background(), missing, Options{}); err == nil {
		t.Fatal("previewing a missing source database succeeded")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing source was created: %v", err)
	}
}

func newFixtureDatabase(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(0)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db, `CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY);
INSERT INTO schema_migrations(version) VALUES (8);
CREATE TABLE downstream_keys (
  id INTEGER PRIMARY KEY, name TEXT NOT NULL, key_prefix TEXT NOT NULL, enabled INTEGER NOT NULL,
  allowed_models_json TEXT NOT NULL DEFAULT '[]', routing_profile_id INTEGER, expires_at INTEGER
);
CREATE TABLE routing_profiles(id INTEGER PRIMARY KEY,name TEXT NOT NULL);
CREATE TABLE routing_profile_model_targets(
  routing_profile_id INTEGER NOT NULL,published_model_id INTEGER NOT NULL,route_site_target_id INTEGER NOT NULL,position INTEGER NOT NULL
);
CREATE TABLE sites(id INTEGER PRIMARY KEY,name TEXT NOT NULL,enabled INTEGER NOT NULL);
CREATE TABLE inference_endpoints(
  id INTEGER PRIMARY KEY,site_id INTEGER NOT NULL,name TEXT NOT NULL,base_url TEXT NOT NULL,wire_protocol TEXT NOT NULL,enabled INTEGER NOT NULL
);
CREATE TABLE inference_credentials(
  id INTEGER PRIMARY KEY,site_id INTEGER NOT NULL,name TEXT NOT NULL,secret_cipher BLOB NOT NULL,position INTEGER NOT NULL,
  enabled INTEGER NOT NULL,runtime_state TEXT NOT NULL,cooldown_until INTEGER
);
CREATE TABLE site_models(id INTEGER PRIMARY KEY,site_id INTEGER NOT NULL,endpoint_id INTEGER NOT NULL,model_name TEXT NOT NULL,enabled INTEGER NOT NULL);
CREATE TABLE credential_model_access(credential_id INTEGER NOT NULL,site_model_id INTEGER NOT NULL,availability TEXT NOT NULL);
CREATE TABLE published_models(
  id INTEGER PRIMARY KEY,public_name TEXT NOT NULL,official_price_sku TEXT,enabled INTEGER NOT NULL,revision INTEGER NOT NULL
);
CREATE TABLE route_site_targets(
  id INTEGER PRIMARY KEY,published_model_id INTEGER NOT NULL,site_id INTEGER NOT NULL,endpoint_id INTEGER NOT NULL,
  site_model_id INTEGER NOT NULL,position INTEGER NOT NULL,enabled INTEGER NOT NULL
);
CREATE TABLE upstreams(id INTEGER PRIMARY KEY,name TEXT NOT NULL,kind TEXT NOT NULL,enabled INTEGER NOT NULL);
CREATE TABLE upstream_endpoints(
  id INTEGER PRIMARY KEY,upstream_id INTEGER NOT NULL,name TEXT NOT NULL,base_url TEXT NOT NULL,enabled INTEGER NOT NULL
);
CREATE TABLE upstream_credentials(
  id INTEGER PRIMARY KEY,upstream_id INTEGER NOT NULL,name TEXT NOT NULL,secret_cipher BLOB NOT NULL,enabled INTEGER NOT NULL,runtime_state TEXT NOT NULL
);
CREATE TABLE upstream_models(id INTEGER PRIMARY KEY,upstream_id INTEGER NOT NULL,model_name TEXT NOT NULL,enabled INTEGER NOT NULL);
CREATE TABLE routes(id INTEGER PRIMARY KEY,public_model TEXT NOT NULL,enabled INTEGER NOT NULL,revision INTEGER NOT NULL);
CREATE TABLE route_targets(
  id INTEGER PRIMARY KEY,route_id INTEGER NOT NULL,upstream_model_id INTEGER NOT NULL,endpoint_id INTEGER NOT NULL,
  credential_id INTEGER NOT NULL,upstream_model_override TEXT,position INTEGER NOT NULL,enabled INTEGER NOT NULL
)`)
	return db, path
}

func seedRoutingFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec(t, db, `INSERT INTO routing_profiles(id,name) VALUES (7,'Priority B');
INSERT INTO sites(id,name,enabled) VALUES (1,'Site A',1),(2,'Site B',1);
INSERT INTO inference_endpoints(id,site_id,name,base_url,wire_protocol,enabled) VALUES
(101,1,'A API','https://a.example/v1','openai_chat_completions',1),(201,2,'B API','https://b.example/v1','compatible',1);
INSERT INTO inference_credentials(id,site_id,name,secret_cipher,position,enabled,runtime_state) VALUES
(1001,1,'A key',x'01',0,1,'active'),(2001,2,'B key',x'02',0,1,'active');
INSERT INTO site_models(id,site_id,endpoint_id,model_name,enabled) VALUES
(1101,1,101,'gpt-4o',1),(2101,2,201,'gpt-4o',1),(1201,1,101,'v3-source',1);
INSERT INTO credential_model_access(credential_id,site_model_id,availability) VALUES
(1001,1101,'supported'),(2001,2101,'supported'),(1001,1201,'supported');
INSERT INTO published_models(id,public_name,official_price_sku,enabled,revision) VALUES
(11,'gpt-4o',NULL,1,3),(12,'v3-unlisted','official/v3',1,1);
INSERT INTO route_site_targets(id,published_model_id,site_id,endpoint_id,site_model_id,position,enabled) VALUES
(111,11,1,101,1101,0,1),(112,11,2,201,2101,1,1),(121,12,1,101,1201,0,1);
INSERT INTO routing_profile_model_targets(routing_profile_id,published_model_id,route_site_target_id,position)
VALUES (7,11,112,0);
INSERT INTO upstreams(id,name,kind,enabled) VALUES (31,'Legacy Site','openai',1);
INSERT INTO upstream_endpoints(id,upstream_id,name,base_url,enabled) VALUES (3201,31,'Legacy API','https://legacy.example/v1',1);
INSERT INTO upstream_credentials(id,upstream_id,name,secret_cipher,enabled,runtime_state) VALUES (3301,31,'Legacy key',x'03',1,'active');
INSERT INTO upstream_models(id,upstream_id,model_name,enabled) VALUES (3101,31,'gpt-4o',1),(3102,31,'legacy-only',1);
INSERT INTO routes(id,public_model,enabled,revision) VALUES (21,'gpt-4o',1,2),(22,'legacy-only',1,1);
INSERT INTO route_targets(id,route_id,upstream_model_id,endpoint_id,credential_id,position,enabled) VALUES
(2101,21,3101,3201,3301,0,1),(2201,22,3102,3201,3301,0,1)`)
}

func mustExec(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.Exec(query); err != nil {
		t.Fatal(err)
	}
}

func findKey(t *testing.T, report Report, id int64) KeyReport {
	t.Helper()
	for _, key := range report.Keys {
		if key.LegacyID == id {
			return key
		}
	}
	t.Fatalf("key %d not found", id)
	return KeyReport{}
}

func findModel(t *testing.T, key KeyReport, name string) ModelReport {
	t.Helper()
	for _, model := range key.Models {
		if model.PublicName == name {
			return model
		}
	}
	t.Fatalf("model %q not found in %+v", name, key.Models)
	return ModelReport{}
}

func findExcluded(key KeyReport, name string) *ExcludedModel {
	for index := range key.ExcludedModels {
		if key.ExcludedModels[index].PublicName == name {
			return &key.ExcludedModels[index]
		}
	}
	return nil
}

func hasIssue(issues []Issue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
