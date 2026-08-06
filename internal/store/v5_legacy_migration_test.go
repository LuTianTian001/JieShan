package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestLegacyMigrationPreviewApplyAndIdempotency(t *testing.T) {
	s := newV3TestStore(t)
	ctx := context.Background()

	alphaID := seedLegacyMigrationUpstream(t, s, "Alpha", "https://relay.example.test/alpha", "https://alpha-api.example.test/v1", []byte("alpha-key"), []string{"model-a", "model-common"})
	betaID := seedLegacyMigrationUpstream(t, s, "Beta", "https://relay.example.test/beta", "https://beta-api.example.test/v1", []byte("beta-key"), []string{"model-common"})
	gammaID := seedLegacyMigrationUpstream(t, s, "Gamma", "https://other.example.test", "https://gamma-api.example.test/v1", []byte("gamma-key"), []string{"model-common"})

	now := NowMS()
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO upstream_endpoints(upstream_id,name,base_url,position,enabled,created_at,updated_at)
VALUES (?,?,?,?,1,?,?)`, alphaID, "Responses", "https://alpha-responses.example.test/v1", 1, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO upstream_credentials(upstream_id,name,secret_cipher,enabled,runtime_state,created_at,updated_at)
VALUES (?,?,?,1,'active',?,?)`, alphaID, "Backup", []byte("alpha-backup-key"), now, now); err != nil {
		t.Fatal(err)
	}

	alphaModel := legacyModelID(t, s, alphaID, "model-common")
	betaModel := legacyModelID(t, s, betaID, "model-common")
	gammaModel := legacyModelID(t, s, gammaID, "model-common")
	routeID, err := s.CreateRoute(ctx, RouteWrite{
		PublicModel: "public-common", DisplayName: "Public Common", Enabled: true, MonitorEnabled: true,
		MonitorIntervalSeconds: 420, CooldownSeconds: 180, FailureThreshold: 3, FailureWindowSeconds: 240,
		TargetModelIDs: []int64{alphaModel, gammaModel, betaModel},
	})
	if err != nil {
		t.Fatal(err)
	}
	alphaAccountResult, err := s.DB.ExecContext(ctx, `INSERT INTO upstream_accounts(upstream_id,adapter_kind,api_origin,auth_cipher,enabled,capabilities_json,sync_state,created_at,updated_at)
VALUES (?,'new_api','https://relay.example.test',?,1,'{"usage":true}','healthy',?,?)`, alphaID, []byte("account-secret"), now, now)
	if err != nil {
		t.Fatal(err)
	}
	alphaAccountID, _ := alphaAccountResult.LastInsertId()
	betaAccountResult, err := s.DB.ExecContext(ctx, `INSERT INTO upstream_accounts(upstream_id,adapter_kind,api_origin,auth_cipher,enabled,capabilities_json,sync_state,created_at,updated_at)
VALUES (?,'new_api','https://relay.example.test',?,1,'{"usage":true}','healthy',?,?)`, betaID, []byte("account-secret"), now, now)
	if err != nil {
		t.Fatal(err)
	}
	betaAccountID, _ := betaAccountResult.LastInsertId()
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO upstream_account_snapshots(upstream_account_id,snapshot_json,captured_at) VALUES
(?,'{"balance":"10"}',?),(?,'{"balance":"10"}',?),(?,'{"balance":"9"}',?)`, alphaAccountID, now, betaAccountID, now, betaAccountID, now+1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO upstream_account_usage_records(upstream_account_id,dedupe_key,external_id,model_name,amount_text,unit,raw_json,occurred_at,synced_at) VALUES
(?,'usage-1','one','model-common','1','USD','{"cost":"1"}',?,?),
(?,'usage-1','one','model-common','1','USD','{"cost":"1"}',?,?),
(?,'usage-2','two','model-common','2','USD','{"cost":"2"}',?,?)`,
		alphaAccountID, now, now, betaAccountID, now, now, betaAccountID, now+1, now+1); err != nil {
		t.Fatal(err)
	}

	preview, err := s.PreviewLegacyMigration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.CanApply || preview.RequiresForce || preview.AlreadyApplied {
		t.Fatalf("unexpected preview state: %+v", preview)
	}
	if !strings.HasPrefix(preview.PlanFingerprint, "sha256:") {
		t.Fatalf("plan fingerprint = %q", preview.PlanFingerprint)
	}
	if preview.Legacy.Upstreams != 3 || preview.Legacy.Accounts != 2 || preview.Legacy.AccountSnapshots != 3 || preview.Legacy.AccountUsageRecords != 3 || preview.Legacy.Routes != 1 || preview.Legacy.RouteTargets != 3 {
		t.Fatalf("legacy inventory = %+v", preview.Legacy)
	}
	if preview.Plan.Sites != 2 || preview.Plan.Endpoints != 4 || preview.Plan.Credentials != 4 || preview.Plan.SiteModels != 6 || preview.Plan.PublishedModels != 1 || preview.Plan.RouteTargets != 2 ||
		preview.Plan.Accounts != 1 || preview.Plan.AccountSnapshots != 2 || preview.Plan.AccountUsageRecords != 2 {
		t.Fatalf("migration plan = %+v", preview.Plan)
	}

	result, err := s.ApplyLegacyMigration(ctx, preview.PlanFingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || !result.Preview.AlreadyApplied || !result.Preview.Plan.empty() {
		t.Fatalf("apply result = %+v", result)
	}

	var alphaSite, betaSite, gammaSite int64
	for upstreamID, target := range map[int64]*int64{alphaID: &alphaSite, betaID: &betaSite, gammaID: &gammaSite} {
		if err := s.DB.QueryRowContext(ctx, `SELECT site_id FROM legacy_upstream_site_mappings WHERE upstream_id=?`, upstreamID).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if alphaSite != betaSite || alphaSite == gammaSite {
		t.Fatalf("origin merge failed: alpha=%d beta=%d gamma=%d", alphaSite, betaSite, gammaSite)
	}
	var mergedEndpoints, mergedCredentials int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM inference_endpoints WHERE site_id=?`, alphaSite).Scan(&mergedEndpoints); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM inference_credentials WHERE site_id=?`, alphaSite).Scan(&mergedCredentials); err != nil {
		t.Fatal(err)
	}
	if mergedEndpoints != 3 || mergedCredentials != 3 {
		t.Fatalf("merged inventory endpoints=%d credentials=%d", mergedEndpoints, mergedCredentials)
	}
	account, err := s.GetSiteAccountSecret(ctx, alphaSite)
	if err != nil {
		t.Fatal(err)
	}
	if string(account.AuthCipher) != "account-secret" || account.AdapterKind != "new_api" || account.APIOrigin != "https://relay.example.test" {
		t.Fatalf("migrated account = %+v cipher=%q", account.SiteAccount, account.AuthCipher)
	}
	var snapshotCount, usageCount int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_account_snapshots WHERE site_account_id=?`, account.ID).Scan(&snapshotCount); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_account_usage_records WHERE site_account_id=?`, account.ID).Scan(&usageCount); err != nil {
		t.Fatal(err)
	}
	if snapshotCount != 2 || usageCount != 2 {
		t.Fatalf("migrated account history snapshots=%d usage=%d", snapshotCount, usageCount)
	}

	var publishedID int64
	if err := s.DB.QueryRowContext(ctx, `SELECT published_model_id FROM legacy_route_published_mappings WHERE route_id=?`, routeID).Scan(&publishedID); err != nil {
		t.Fatal(err)
	}
	published, err := s.GetPublishedModel(ctx, publishedID)
	if err != nil {
		t.Fatal(err)
	}
	if !published.MonitorEnabled || published.MonitorIntervalSeconds != 420 || published.CooldownSeconds != 180 || published.FailureThreshold != 3 || published.FailureWindowSeconds != 240 {
		t.Fatalf("route policy was not copied: %+v", published)
	}
	targets, err := s.ListRouteSiteTargets(ctx, publishedID)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0].SiteID != alphaSite || targets[1].SiteID != gammaSite || targets[0].Position != 0 || targets[1].Position != 1 {
		t.Fatalf("site route order = %+v", targets)
	}

	second, err := s.ApplyLegacyMigration(ctx, result.Preview.PlanFingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Applied || !second.Preview.AlreadyApplied {
		t.Fatalf("second apply = %+v", second)
	}
	for table, want := range map[string]int{"sites": 2, "inference_endpoints": 4, "inference_credentials": 4, "published_models": 1, "route_site_targets": 2, "site_accounts": 1, "site_account_snapshots": 2, "site_account_usage_records": 2} {
		var got int
		if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s count after idempotent apply = %d, want %d", table, got, want)
		}
	}
}

func TestLegacyMigrationBlocksAmbiguousMergedAccounts(t *testing.T) {
	for _, test := range []struct {
		name         string
		adapterTwo   string
		authTwo      []byte
		conflictCode string
	}{
		{name: "adapter", adapterTwo: "one_api", authTwo: []byte("same-auth"), conflictCode: "account_adapter_conflict"},
		{name: "opaque auth", adapterTwo: "new_api", authTwo: []byte("different-auth"), conflictCode: "account_auth_conflict"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newV3TestStore(t)
			ctx := context.Background()
			one := seedLegacyMigrationUpstream(t, s, "One", "https://accounts.example.test/one", "https://one.example.test/v1", []byte("one-key"), nil)
			two := seedLegacyMigrationUpstream(t, s, "Two", "https://accounts.example.test/two", "https://two.example.test/v1", []byte("two-key"), nil)
			now := NowMS()
			for _, account := range []struct {
				upstream int64
				adapter  string
				auth     []byte
			}{{one, "new_api", []byte("same-auth")}, {two, test.adapterTwo, test.authTwo}} {
				if _, err := s.DB.ExecContext(ctx, `INSERT INTO upstream_accounts(upstream_id,adapter_kind,api_origin,auth_cipher,enabled,capabilities_json,sync_state,created_at,updated_at)
VALUES (?,?,?,?,1,'{}','pending',?,?)`, account.upstream, account.adapter, "https://accounts.example.test", account.auth, now, now); err != nil {
					t.Fatal(err)
				}
			}
			preview, err := s.PreviewLegacyMigration(ctx)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, conflict := range preview.Conflicts {
				found = found || conflict.Code == test.conflictCode
			}
			if preview.CanApply || !found {
				t.Fatalf("preview = %+v", preview)
			}
			if _, err := s.ApplyLegacyMigration(ctx, preview.PlanFingerprint, true); !errors.Is(err, ErrLegacyMigrationBlocked) {
				t.Fatalf("forced apply error = %v", err)
			}
		})
	}
}

func TestLegacyMigrationSkipsDiscoveryOnlyRouteTargetsAndRaisesThreshold(t *testing.T) {
	for _, protocol := range []string{"anthropic", "gemini"} {
		t.Run(protocol, func(t *testing.T) {
			s := newV3TestStore(t)
			ctx := context.Background()
			upstreamID, err := s.CreateUpstream(ctx, UpstreamWrite{
				Name: protocol, Kind: protocol, DashboardURL: "https://" + protocol + ".example.test",
				BaseURL: "https://" + protocol + ".example.test/v1", Enabled: true, SecretCipher: []byte(protocol + "-key"),
			})
			if err != nil {
				t.Fatal(err)
			}
			now := NowMS()
			modelResult, err := s.DB.ExecContext(ctx, `INSERT INTO upstream_models(upstream_id,model_name,enabled,stale,missing_count,last_seen_at,created_at,updated_at)
VALUES (?,'native-model',1,0,0,?,?,?)`, upstreamID, now, now, now)
			if err != nil {
				t.Fatal(err)
			}
			modelID, _ := modelResult.LastInsertId()
			var endpointID, credentialID int64
			_ = s.DB.QueryRowContext(ctx, `SELECT id FROM upstream_endpoints WHERE upstream_id=?`, upstreamID).Scan(&endpointID)
			_ = s.DB.QueryRowContext(ctx, `SELECT id FROM upstream_credentials WHERE upstream_id=?`, upstreamID).Scan(&credentialID)
			routeResult, err := s.DB.ExecContext(ctx, `INSERT INTO routes(public_model,enabled,monitor_enabled,monitor_interval_seconds,cooldown_seconds,failure_threshold,failure_window_seconds,revision,created_at,updated_at)
VALUES (?,1,1,300,300,1,300,1,?,?)`, "public-"+protocol, now, now)
			if err != nil {
				t.Fatal(err)
			}
			routeID, _ := routeResult.LastInsertId()
			if _, err := s.DB.ExecContext(ctx, `INSERT INTO route_targets(route_id,upstream_model_id,endpoint_id,credential_id,position,enabled,created_at,updated_at)
VALUES (?,?,?,?,0,1,?,?)`, routeID, modelID, endpointID, credentialID, now, now); err != nil {
				t.Fatal(err)
			}
			preview, err := s.PreviewLegacyMigration(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if preview.Plan.RouteTargets != 0 || preview.Plan.SkippedRouteTargets != 1 || len(preview.Warnings) == 0 {
				t.Fatalf("preview = %+v", preview)
			}
			result, err := s.ApplyLegacyMigration(ctx, preview.PlanFingerprint, false)
			if err != nil {
				t.Fatal(err)
			}
			if result.Created.SkippedRouteTargets != 1 {
				t.Fatalf("apply report = %+v", result)
			}
			published, err := s.GetPublishedModelByName(ctx, "public-"+protocol)
			if err != nil {
				t.Fatal(err)
			}
			if published.FailureThreshold != 2 {
				t.Fatalf("failure threshold = %d, want 2", published.FailureThreshold)
			}
			targets, err := s.ListRouteSiteTargets(ctx, published.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(targets) != 0 {
				t.Fatalf("discovery-only route targets = %+v", targets)
			}
		})
	}
}

func TestLegacyMigrationBlocksExistingV3UnlessForced(t *testing.T) {
	s := newV3TestStore(t)
	ctx := context.Background()
	seedLegacyMigrationUpstream(t, s, "Legacy", "https://legacy.example.test", "https://legacy.example.test/v1", []byte("legacy-key"), []string{"legacy-model"})
	if _, err := s.CreateSite(ctx, SiteWrite{Name: "Manual", DashboardURL: "https://manual.example.test", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviewLegacyMigration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if preview.CanApply || !preview.RequiresForce || len(preview.Conflicts) == 0 || !preview.Conflicts[0].Overrideable {
		t.Fatalf("preview should require force: %+v", preview)
	}
	if _, err := s.ApplyLegacyMigration(ctx, preview.PlanFingerprint, false); !errors.Is(err, ErrLegacyMigrationBlocked) {
		t.Fatalf("ApplyLegacyMigration() error = %v", err)
	}
	result, err := s.ApplyLegacyMigration(ctx, preview.PlanFingerprint, true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied {
		t.Fatalf("forced apply = %+v", result)
	}
}

func TestLegacyMigrationApplyIsAtomic(t *testing.T) {
	s := newV3TestStore(t)
	ctx := context.Background()
	seedLegacyMigrationUpstream(t, s, "Atomic", "https://atomic.example.test", "https://atomic.example.test/v1", []byte("atomic-key"), []string{"atomic-model"})
	if _, err := s.DB.ExecContext(ctx, `CREATE TRIGGER fail_legacy_migration_credential BEFORE INSERT ON inference_credentials
BEGIN SELECT RAISE(ABORT, 'forced credential failure'); END`); err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviewLegacyMigration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyLegacyMigration(ctx, preview.PlanFingerprint, false); err == nil {
		t.Fatal("ApplyLegacyMigration() unexpectedly succeeded")
	}
	for _, table := range []string{"sites", "inference_endpoints", "inference_credentials", "legacy_upstream_site_mappings"} {
		var count int
		if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("atomic rollback left %d rows in %s", count, table)
		}
	}
}

func TestLegacyMigrationRequiresCurrentPlanFingerprint(t *testing.T) {
	s := newV3TestStore(t)
	ctx := context.Background()
	upstreamID := seedLegacyMigrationUpstream(t, s, "Stable", "https://stable.example.test", "https://stable.example.test/v1", []byte("stable-key"), []string{"stable-model"})

	if _, err := s.ApplyLegacyMigration(ctx, "", false); !errors.Is(err, ErrLegacyMigrationPlanFingerprintRequired) {
		t.Fatalf("missing fingerprint error = %v", err)
	}
	preview, err := s.PreviewLegacyMigration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := s.PreviewLegacyMigration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if preview.PlanFingerprint == "" || repeated.PlanFingerprint != preview.PlanFingerprint {
		t.Fatalf("unstable fingerprint: first=%q second=%q", preview.PlanFingerprint, repeated.PlanFingerprint)
	}

	if _, err := s.DB.ExecContext(ctx, `UPDATE upstreams SET name=?,updated_at=? WHERE id=?`, "Changed", NowMS(), upstreamID); err != nil {
		t.Fatal(err)
	}
	_, err = s.ApplyLegacyMigration(ctx, preview.PlanFingerprint, true)
	var changed *LegacyMigrationPlanChangedError
	if !errors.As(err, &changed) {
		t.Fatalf("stale fingerprint error = %v", err)
	}
	if changed.Preview.PlanFingerprint == preview.PlanFingerprint || changed.Preview.Legacy.Upstreams != preview.Legacy.Upstreams {
		t.Fatalf("changed preview = %+v", changed.Preview)
	}
	var sites int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sites`).Scan(&sites); err != nil {
		t.Fatal(err)
	}
	if sites != 0 {
		t.Fatalf("stale apply created %d sites", sites)
	}
	result, err := s.ApplyLegacyMigration(ctx, changed.Preview.PlanFingerprint, false)
	if err != nil || !result.Applied {
		t.Fatalf("apply after review = %+v, %v", result, err)
	}
}

func TestLegacyMigrationFingerprintDistinguishesSQLiteTextAndBlob(t *testing.T) {
	s := newV3TestStore(t)
	var textValue, blobValue any
	if err := s.DB.QueryRow(`SELECT CAST('same bytes' AS TEXT),CAST('same bytes' AS BLOB)`).Scan(&textValue, &blobValue); err != nil {
		t.Fatal(err)
	}
	if _, ok := textValue.(string); !ok {
		t.Fatalf("TEXT scan type = %T", textValue)
	}
	if _, ok := blobValue.([]byte); !ok {
		t.Fatalf("BLOB scan type = %T", blobValue)
	}
	digestFor := func(value any) []byte {
		t.Helper()
		digest := sha256.New()
		encoder := legacyMigrationFingerprintEncoder{digest: digest}
		if err := encoder.writeSQLiteValue(value); err != nil {
			t.Fatal(err)
		}
		return digest.Sum(nil)
	}
	if bytes.Equal(digestFor(textValue), digestFor(blobValue)) {
		t.Fatal("TEXT and BLOB values produced the same fingerprint encoding")
	}
}

func TestLegacyMigrationFingerprintBindsNormalizedPlanManifest(t *testing.T) {
	s := newV3TestStore(t)
	ctx := context.Background()
	upstreamID := seedLegacyMigrationUpstream(t, s, "Manifest", "https://manifest.example.test", "https://manifest.example.test/v1", []byte("manifest-key"), []string{"manifest-model"})
	now := NowMS()
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO upstream_accounts(upstream_id,adapter_kind,api_origin,auth_cipher,enabled,capabilities_json,sync_state,created_at,updated_at)
VALUES (?,'new_api','https://manifest.example.test',?,1,'{}','healthy',?,?)`, upstreamID, []byte("manifest-account"), now, now); err != nil {
		t.Fatal(err)
	}

	fingerprint := func(mutate func(*legacyMigrationPlanState)) string {
		t.Helper()
		tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		state, err := buildLegacyMigrationPlan(ctx, tx)
		if err != nil {
			t.Fatal(err)
		}
		if mutate != nil {
			mutate(&state)
		}
		value, err := legacyMigrationPlanFingerprint(ctx, tx, state)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	baseline := fingerprint(nil)
	if got := fingerprint(func(state *legacyMigrationPlanState) { state.preview.PlanFingerprint = "ignored" }); got != baseline {
		t.Fatalf("embedded fingerprint changed normalized manifest: baseline=%q got=%q", baseline, got)
	}
	mutations := []struct {
		name   string
		mutate func(*legacyMigrationPlanState)
	}{
		{name: "preview", mutate: func(state *legacyMigrationPlanState) { state.preview.Plan.Sites++ }},
		{name: "site group", mutate: func(state *legacyMigrationPlanState) { state.groups[0].name += " changed" }},
		{name: "upstream group mapping", mutate: func(state *legacyMigrationPlanState) {
			group := *state.groupByUpstream[upstreamID]
			group.key += " changed"
			state.groupByUpstream[upstreamID] = &group
		}},
		{name: "account unit", mutate: func(state *legacyMigrationPlanState) { state.accountUnits[0].capabilities = `{"changed":true}` }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			if got := fingerprint(test.mutate); got == baseline {
				t.Fatalf("different %s manifest produced baseline fingerprint %q", test.name, got)
			}
		})
	}
}

func seedLegacyMigrationUpstream(t *testing.T, s *Store, name, dashboard, baseURL string, secret []byte, models []string) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := s.CreateUpstream(ctx, UpstreamWrite{
		Name: name, Kind: "compatible", DashboardURL: dashboard, BaseURL: baseURL, Enabled: true,
		CustomHeaders: []byte(`{"X-Legacy":"true"}`), SecretCipher: secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := NowMS()
	for _, model := range models {
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO upstream_models(upstream_id,model_name,enabled,stale,missing_count,last_seen_at,created_at,updated_at)
VALUES (?,?,1,0,0,?,?,?)`, id, model, now, now, now); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func legacyModelID(t *testing.T, s *Store, upstreamID int64, name string) int64 {
	t.Helper()
	var id int64
	if err := s.DB.QueryRow(`SELECT id FROM upstream_models WHERE upstream_id=? AND model_name=?`, upstreamID, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
