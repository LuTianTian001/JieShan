package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/LuTianTian001/JieShan/internal/store"
)

func TestLegacyMigrationV2PreviewApplyContract(t *testing.T) {
	fixture := newAPIContractFixture(t)
	ctx := context.Background()
	legacyID, err := fixture.store.CreateUpstream(ctx, store.UpstreamWrite{
		Name: "Legacy Relay", Kind: "compatible", DashboardURL: "https://legacy.example.test/panel",
		BaseURL: "https://legacy.example.test/v1", Enabled: true, SecretCipher: []byte("encrypted-key"),
	})
	if err != nil {
		t.Fatal(err)
	}
	now := store.NowMS()
	result, err := fixture.store.DB.ExecContext(ctx, `INSERT INTO upstream_models(upstream_id,model_name,enabled,stale,missing_count,last_seen_at,created_at,updated_at)
VALUES (?,'legacy-model',1,0,0,?,?,?)`, legacyID, now, now, now)
	if err != nil {
		t.Fatal(err)
	}
	modelID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.CreateRoute(ctx, store.RouteWrite{
		PublicModel: "public-legacy", Enabled: true, MonitorEnabled: true, MonitorIntervalSeconds: 300,
		CooldownSeconds: 300, FailureThreshold: 2, FailureWindowSeconds: 300, TargetModelIDs: []int64{modelID},
	}); err != nil {
		t.Fatal(err)
	}
	accountResult, err := fixture.store.DB.ExecContext(ctx, `INSERT INTO upstream_accounts(upstream_id,adapter_kind,api_origin,auth_cipher,enabled,capabilities_json,sync_state,created_at,updated_at)
VALUES (?,'new_api','https://legacy.example.test',?,1,'{}','healthy',?,?)`, legacyID, []byte("encrypted-account"), now, now)
	if err != nil {
		t.Fatal(err)
	}
	accountID, _ := accountResult.LastInsertId()
	if _, err := fixture.store.DB.ExecContext(ctx, `INSERT INTO upstream_account_snapshots(upstream_account_id,snapshot_json,captured_at) VALUES (?,'{"balance":"5"}',?)`, accountID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.DB.ExecContext(ctx, `INSERT INTO upstream_account_usage_records(upstream_account_id,dedupe_key,raw_json,synced_at) VALUES (?,'usage-http','{}',?)`, accountID, now); err != nil {
		t.Fatal(err)
	}

	resp, body := fixture.request(t, http.MethodGet, "/api/v2/migrations/legacy/preview", nil, nil)
	assertHTTPStatus(t, resp, body, http.StatusUnauthorized)
	resp, body = fixture.request(t, http.MethodPost, "/api/v1/auth/login", map[string]any{"password": "correct horse battery staple"}, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)

	resp, body = fixture.request(t, http.MethodGet, "/api/v2/migrations/legacy/preview", nil, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	preview := decodeContract[struct {
		Item store.LegacyMigrationPreview `json:"item"`
	}](t, body).Item
	if !preview.CanApply || preview.Plan.Sites != 1 || preview.Plan.PublishedModels != 1 || preview.Plan.RouteTargets != 1 ||
		preview.Plan.Accounts != 1 || preview.Plan.AccountSnapshots != 1 || preview.Plan.AccountUsageRecords != 1 {
		t.Fatalf("preview DTO = %+v", preview)
	}
	if preview.PlanFingerprint == "" {
		t.Fatal("preview omitted planFingerprint")
	}

	resp, body = fixture.request(t, http.MethodPost, "/api/v2/migrations/legacy/apply", map[string]any{}, nil)
	assertHTTPStatus(t, resp, body, http.StatusBadRequest)
	missing := decodeContract[struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}](t, body)
	if missing.Error.Code != "legacy_migration_plan_fingerprint_required" {
		t.Fatalf("missing fingerprint DTO = %+v", missing)
	}

	resp, body = fixture.request(t, http.MethodPost, "/api/v2/migrations/legacy/apply", map[string]any{"planFingerprint": preview.PlanFingerprint}, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	applied := decodeContract[struct {
		Item store.LegacyMigrationApplyResult `json:"item"`
	}](t, body).Item
	if !applied.Applied || !applied.Preview.AlreadyApplied || applied.Created.Accounts != 1 || applied.Created.AccountSnapshots != 1 || applied.Created.AccountUsageRecords != 1 {
		t.Fatalf("apply DTO = %+v", applied)
	}

	resp, body = fixture.request(t, http.MethodPost, "/api/v2/migrations/legacy/apply", map[string]any{"planFingerprint": applied.Preview.PlanFingerprint}, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	second := decodeContract[struct {
		Item store.LegacyMigrationApplyResult `json:"item"`
	}](t, body).Item
	if second.Applied || !second.Preview.AlreadyApplied {
		t.Fatalf("idempotent apply DTO = %+v", second)
	}
}

func TestLegacyMigrationV2ReportsMergedAccountConflict(t *testing.T) {
	fixture := newAPIContractFixture(t)
	ctx := context.Background()
	now := store.NowMS()
	upstreamIDs := make([]int64, 0, 2)
	for _, name := range []string{"One", "Two"} {
		id, err := fixture.store.CreateUpstream(ctx, store.UpstreamWrite{
			Name: name, Kind: "compatible", DashboardURL: "https://merged.example.test/" + name,
			BaseURL: "https://" + name + ".example.test/v1", Enabled: true, SecretCipher: []byte(name + "-key"),
		})
		if err != nil {
			t.Fatal(err)
		}
		upstreamIDs = append(upstreamIDs, id)
	}
	for index, upstreamID := range upstreamIDs {
		if _, err := fixture.store.DB.ExecContext(ctx, `INSERT INTO upstream_accounts(upstream_id,adapter_kind,api_origin,auth_cipher,enabled,capabilities_json,sync_state,created_at,updated_at)
VALUES (?,'new_api','https://merged.example.test',?,1,'{}','pending',?,?)`, upstreamID, []byte{byte(index + 1)}, now, now); err != nil {
			t.Fatal(err)
		}
	}
	resp, body := fixture.request(t, http.MethodPost, "/api/v1/auth/login", map[string]any{"password": "correct horse battery staple"}, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	resp, body = fixture.request(t, http.MethodGet, "/api/v2/migrations/legacy/preview", nil, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	preview := decodeContract[struct {
		Item store.LegacyMigrationPreview `json:"item"`
	}](t, body).Item
	resp, body = fixture.request(t, http.MethodPost, "/api/v2/migrations/legacy/apply", map[string]any{"planFingerprint": preview.PlanFingerprint, "force": true}, nil)
	assertHTTPStatus(t, resp, body, http.StatusConflict)
	conflict := decodeContract[struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Preview store.LegacyMigrationPreview `json:"preview"`
	}](t, body)
	found := false
	for _, item := range conflict.Preview.Conflicts {
		found = found || item.Code == "account_auth_conflict"
	}
	if conflict.Error.Code != "legacy_migration_blocked" || !found {
		t.Fatalf("account conflict DTO = %+v", conflict)
	}
}

func TestLegacyMigrationV2ReturnsConflictPreview(t *testing.T) {
	fixture := newAPIContractFixture(t)
	ctx := context.Background()
	if _, err := fixture.store.CreateUpstream(ctx, store.UpstreamWrite{
		Name: "Legacy", Kind: "compatible", DashboardURL: "https://legacy.example.test",
		BaseURL: "https://legacy.example.test/v1", Enabled: true, SecretCipher: []byte("encrypted-key"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.CreateSite(ctx, store.SiteWrite{Name: "Manual", DashboardURL: "https://manual.example.test", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	resp, body := fixture.request(t, http.MethodPost, "/api/v1/auth/login", map[string]any{"password": "correct horse battery staple"}, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	resp, body = fixture.request(t, http.MethodGet, "/api/v2/migrations/legacy/preview", nil, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	preview := decodeContract[struct {
		Item store.LegacyMigrationPreview `json:"item"`
	}](t, body).Item
	resp, body = fixture.request(t, http.MethodPost, "/api/v2/migrations/legacy/apply", map[string]any{"planFingerprint": preview.PlanFingerprint}, nil)
	assertHTTPStatus(t, resp, body, http.StatusConflict)
	conflict := decodeContract[struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Preview store.LegacyMigrationPreview `json:"preview"`
	}](t, body)
	if conflict.Error.Code != "legacy_migration_blocked" || !conflict.Preview.RequiresForce || len(conflict.Preview.Conflicts) == 0 {
		t.Fatalf("conflict DTO = %+v", conflict)
	}
}

func TestLegacyMigrationV2RejectsChangedPlanFingerprint(t *testing.T) {
	fixture := newAPIContractFixture(t)
	ctx := context.Background()
	upstreamID, err := fixture.store.CreateUpstream(ctx, store.UpstreamWrite{
		Name: "Before", Kind: "compatible", DashboardURL: "https://stale.example.test",
		BaseURL: "https://stale.example.test/v1", Enabled: true, SecretCipher: []byte("encrypted-key"),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, body := fixture.request(t, http.MethodPost, "/api/v1/auth/login", map[string]any{"password": "correct horse battery staple"}, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	resp, body = fixture.request(t, http.MethodGet, "/api/v2/migrations/legacy/preview", nil, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	preview := decodeContract[struct {
		Item store.LegacyMigrationPreview `json:"item"`
	}](t, body).Item
	if _, err := fixture.store.DB.ExecContext(ctx, `UPDATE upstreams SET name=?,updated_at=? WHERE id=?`, "After", store.NowMS(), upstreamID); err != nil {
		t.Fatal(err)
	}

	resp, body = fixture.request(t, http.MethodPost, "/api/v2/migrations/legacy/apply", map[string]any{"planFingerprint": preview.PlanFingerprint}, nil)
	assertHTTPStatus(t, resp, body, http.StatusConflict)
	conflict := decodeContract[struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Preview store.LegacyMigrationPreview `json:"preview"`
	}](t, body)
	if conflict.Error.Code != "legacy_migration_plan_changed" || conflict.Preview.PlanFingerprint == "" || conflict.Preview.PlanFingerprint == preview.PlanFingerprint {
		t.Fatalf("stale plan DTO = %+v", conflict)
	}
	var sites int
	if err := fixture.store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sites`).Scan(&sites); err != nil {
		t.Fatal(err)
	}
	if sites != 0 {
		t.Fatalf("stale apply created %d sites", sites)
	}
}
