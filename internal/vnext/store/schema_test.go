package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFreshSchemaIsCanonicalAndHasNoLegacyRoutingSurface(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "vnext.db")
	s := openTestStoreAt(t, databasePath)
	ctx := context.Background()

	var version int
	var name string
	if err := s.DB.QueryRowContext(ctx, `SELECT version,name FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&version, &name); err != nil {
		t.Fatal(err)
	}
	if version != 17 || name != "vnext_price_threshold_inclusive_v1" {
		t.Fatalf("migration = %d/%q", version, name)
	}
	var prefixIndex string
	if err := s.DB.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='index' AND name='downstream_keys_prefix_idx'`).Scan(&prefixIndex); err != nil {
		t.Fatalf("downstream key prefix index: %v", err)
	}

	for _, table := range []string{
		"sites", "site_endpoints", "site_credentials", "credential_endpoint_bindings",
		"provider_model_targets", "credential_target_access", "routing_profiles", "published_models",
		"published_model_targets", "routing_profile_model_routes", "routing_profile_route_targets",
		"downstream_keys", "target_health", "target_attempt_sequences",
		"request_logs", "request_attempts", "request_route_candidates", "quota_ledger", "credential_runtime_state",
		"downstream_key_hourly_usage",
		"model_monitor_settings", "model_probe_runs", "model_probe_results",
		"site_account_connections", "site_balance_snapshots", "site_usage_records",
		"price_catalogs", "price_catalog_entries", "price_catalog_rates", "price_catalog_long_context_rates", "price_catalog_state",
		"admin_users", "admin_sessions", "runtime_settings",
	} {
		var found string
		if err := s.DB.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&found); err != nil {
			t.Fatalf("canonical table %s: %v", table, err)
		}
	}
	priceCatalogColumns := tableColumns(t, s, "price_catalogs")
	for _, required := range []string{
		"version", "schema_version", "digest", "settlement_currency", "source_digest",
		"fx_version", "fx_source_url", "fx_source_digest", "fx_verified_at", "verified_at",
		"effective_at", "imported_at", "sealed",
	} {
		if _, ok := priceCatalogColumns[required]; !ok {
			t.Fatalf("price_catalogs missing %s", required)
		}
	}
	priceEntryColumns := tableColumns(t, s, "price_catalog_entries")
	for _, required := range []string{
		"catalog_version", "sku", "pricing_basis", "verification_status", "source_url",
		"source_digest", "verified_at", "native_currency", "usd_per_native_unit", "long_context_threshold_tokens",
		"long_context_threshold_inclusive",
	} {
		if _, ok := priceEntryColumns[required]; !ok {
			t.Fatalf("price_catalog_entries missing %s", required)
		}
	}
	priceRateColumns := tableColumns(t, s, "price_catalog_rates")
	for _, required := range []string{"catalog_version", "sku", "token_class", "native_price_per_million", "nano_usd_per_million"} {
		if _, ok := priceRateColumns[required]; !ok {
			t.Fatalf("price_catalog_rates missing %s", required)
		}
	}
	longContextRateColumns := tableColumns(t, s, "price_catalog_long_context_rates")
	for _, required := range []string{"catalog_version", "sku", "token_class", "native_price_per_million", "nano_usd_per_million"} {
		if _, ok := longContextRateColumns[required]; !ok {
			t.Fatalf("price_catalog_long_context_rates missing %s", required)
		}
	}
	priceStateColumns := tableColumns(t, s, "price_catalog_state")
	for _, required := range []string{"active_version", "revision", "updated_at"} {
		if _, ok := priceStateColumns[required]; !ok {
			t.Fatalf("price_catalog_state missing %s", required)
		}
	}
	runtimeSettingsColumns := tableColumns(t, s, "runtime_settings")
	for _, required := range []string{
		"singleton_id", "failure_threshold", "failure_window_ms", "cooldown_ms", "probe_interval_ms",
		"first_output_timeout_ms", "stream_idle_timeout_ms", "request_timeout_ms", "max_attempts",
		"log_retention_days", "revision", "updated_at",
	} {
		if _, ok := runtimeSettingsColumns[required]; !ok {
			t.Fatalf("runtime_settings missing %s", required)
		}
	}
	adminColumns := tableColumns(t, s, "admin_users")
	for _, required := range []string{"id", "username", "password_hash", "password_changed_at", "revision", "created_at", "updated_at"} {
		if _, ok := adminColumns[required]; !ok {
			t.Fatalf("admin_users missing %s", required)
		}
	}
	sessionColumns := tableColumns(t, s, "admin_sessions")
	for _, required := range []string{"token_hash", "admin_user_id", "csrf_hash", "expires_at", "last_seen_at", "created_at"} {
		if _, ok := sessionColumns[required]; !ok {
			t.Fatalf("admin_sessions missing %s", required)
		}
	}
	for _, forbidden := range []string{"token", "session_token", "csrf_token", "password"} {
		if _, ok := sessionColumns[forbidden]; ok {
			t.Fatalf("admin_sessions unexpectedly stores plaintext column %s", forbidden)
		}
	}
	for _, table := range []string{"upstreams", "routes", "route_targets", "route_site_targets", "key_model_routes", "key_route_targets"} {
		var found string
		err := s.DB.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&found)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("legacy table %s unexpectedly exists: %q, %v", table, found, err)
		}
	}

	columns := tableColumns(t, s, "downstream_keys")
	for _, required := range []string{"key_digest", "encrypted_secret", "reveal_version", "routing_profile_id", "quota_nano_usd", "hourly_quota_nano_usd", "billing_multiplier_bps", "rpm_limit"} {
		if _, ok := columns[required]; !ok {
			t.Fatalf("downstream_keys missing %s", required)
		}
	}
	for _, forbidden := range []string{"allowed_models_json", "quota_micro_usd"} {
		if _, ok := columns[forbidden]; ok {
			t.Fatalf("downstream_keys unexpectedly contains legacy column %s", forbidden)
		}
	}
	if columns["quota_nano_usd"] != "INTEGER" {
		t.Fatalf("quota_nano_usd type = %q, want INTEGER", columns["quota_nano_usd"])
	}
	endpointColumns := tableColumns(t, s, "site_endpoints")
	for _, required := range []string{"wire_protocol", "surface", "header_template_json", "secret_headers_cipher", "cipher_version"} {
		if _, ok := endpointColumns[required]; !ok {
			t.Fatalf("site_endpoints missing %s", required)
		}
	}
	if _, ok := endpointColumns["custom_headers_json"]; ok {
		t.Fatal("site_endpoints still contains plaintext custom_headers_json")
	}
	if _, ok := endpointColumns["protocol"]; ok {
		t.Fatal("site_endpoints still contains ambiguous protocol column")
	}
	if _, ok := tableColumns(t, s, "site_credentials")["cipher_version"]; !ok {
		t.Fatal("site_credentials missing cipher_version")
	}
	healthColumns := tableColumns(t, s, "target_health")
	for _, required := range []string{
		"config_revision", "state_version", "phase", "capability", "consecutive_failures",
		"failure_window_started_at", "last_failure_at", "last_failure_incident_id", "cooldown_until",
		"last_event_sequence", "half_open_sequence", "half_open_lease_until",
	} {
		if _, ok := healthColumns[required]; !ok {
			t.Fatalf("target_health missing %s", required)
		}
	}
	if _, ok := tableColumns(t, s, "target_attempt_sequences")["last_allocated_sequence"]; !ok {
		t.Fatal("target_attempt_sequences missing last_allocated_sequence")
	}
	requestColumns := tableColumns(t, s, "request_logs")
	for _, required := range []string{
		"published_model_id", "published_model_revision", "effective_routing_profile_id",
		"effective_routing_profile_name_snapshot", "source_routing_profile_id", "source_routing_profile_name_snapshot",
		"route_revision", "metering_status", "metering_error_code",
		"price_catalog_version", "price_sku", "billing_multiplier_bps", "reservation_nano_usd", "official_cost_nano_usd",
		"charged_nano_usd", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens",
		"cache_write_5m_tokens", "cache_write_1h_tokens", "reasoning_tokens",
	} {
		if _, ok := requestColumns[required]; !ok {
			t.Fatalf("request_logs missing %s", required)
		}
	}
	hourlyUsageColumns := tableColumns(t, s, "downstream_key_hourly_usage")
	for _, required := range []string{"downstream_key_id", "window_started_at", "used_nano_usd", "reserved_nano_usd", "updated_at"} {
		if _, ok := hourlyUsageColumns[required]; !ok {
			t.Fatalf("downstream_key_hourly_usage missing %s", required)
		}
	}
	attemptColumns := tableColumns(t, s, "request_attempts")
	for _, required := range []string{
		"request_id", "attempt_index", "published_model_target_id", "published_model_target_revision",
		"provider_model_target_id", "provider_model_target_revision", "failure_kind", "error_code",
		"switch_reason", "response_model", "first_token_ms", "duration_ms",
	} {
		if _, ok := attemptColumns[required]; !ok {
			t.Fatalf("request_attempts missing %s", required)
		}
	}
	candidateColumns := tableColumns(t, s, "request_route_candidates")
	for _, required := range []string{
		"request_id", "position", "published_model_target_id", "published_model_target_revision",
		"provider_model_target_id", "provider_model_target_revision", "site_name_snapshot",
		"endpoint_name_snapshot", "source_model", "wire_protocol", "api_surface", "credentials_json",
		"initial_eligibility", "initial_reason", "disposition", "disposition_reason", "attempt_count",
		"first_attempt_index", "last_attempt_index",
	} {
		if _, ok := candidateColumns[required]; !ok {
			t.Fatalf("request_route_candidates missing %s", required)
		}
	}
	for table, columns := range map[string]map[string]string{
		"request_logs":     requestColumns,
		"request_attempts": attemptColumns,
	} {
		for column := range columns {
			lower := strings.ToLower(column)
			if strings.Contains(lower, "prompt") || strings.Contains(lower, "body") || strings.Contains(lower, "header") {
				t.Fatalf("%s unexpectedly persists sensitive payload column %s", table, column)
			}
		}
	}
	ledgerColumns := tableColumns(t, s, "quota_ledger")
	for _, required := range []string{
		"reserved_delta_nano_usd", "used_delta_nano_usd", "price_catalog_version", "price_sku",
	} {
		columnType, ok := ledgerColumns[required]
		if !ok {
			t.Fatalf("quota_ledger missing %s", required)
		}
		if columnType != "INTEGER" && strings.HasSuffix(required, "nano_usd") {
			t.Fatalf("quota_ledger %s type = %q, want INTEGER", required, columnType)
		}
	}
	for table, columns := range map[string]map[string]string{
		"request_logs": requestColumns,
		"quota_ledger": ledgerColumns,
	} {
		for column := range columns {
			if strings.Contains(strings.ToLower(column), "micro_usd") {
				t.Fatalf("%s unexpectedly contains legacy money column %s", table, column)
			}
		}
	}
	runtimeColumns := tableColumns(t, s, "credential_runtime_state")
	for _, required := range []string{"credential_id", "state", "cooling_until", "last_http_status", "last_error_code", "revision"} {
		if _, ok := runtimeColumns[required]; !ok {
			t.Fatalf("credential_runtime_state missing %s", required)
		}
	}
	monitorColumns := tableColumns(t, s, "model_monitor_settings")
	for _, required := range []string{
		"published_model_id", "enabled", "interval_ms", "history_limit", "next_probe_at",
		"last_probe_started_at", "last_probe_finished_at", "lease_owner", "lease_until", "revision",
	} {
		if _, ok := monitorColumns[required]; !ok {
			t.Fatalf("model_monitor_settings missing %s", required)
		}
	}
	probeColumns := tableColumns(t, s, "model_probe_results")
	for _, required := range []string{
		"run_id", "published_model_id", "published_model_target_id", "published_model_target_revision",
		"provider_model_target_id", "provider_model_target_revision",
		"outcome", "http_status", "failure_kind", "error_code", "latency_ms", "first_output_ms",
		"started_at", "finished_at", "health_applied", "health_apply_reason", "health_error_code",
	} {
		if _, ok := probeColumns[required]; !ok {
			t.Fatalf("model_probe_results missing %s", required)
		}
	}
	accountColumns := tableColumns(t, s, "site_account_connections")
	for _, required := range []string{
		"site_id", "adapter_kind", "origin", "secrets_cipher", "cipher_version", "enabled",
		"last_session_refresh_at", "last_balance_refresh_at", "last_usage_refresh_at",
		"last_error_operation", "last_error_code", "last_error_at", "revision",
	} {
		if _, ok := accountColumns[required]; !ok {
			t.Fatalf("site_account_connections missing %s", required)
		}
	}
	usageColumns := tableColumns(t, s, "site_usage_records")
	for _, required := range []string{
		"dedup_key", "occurred_at", "model", "upstream_model", "status", "http_status",
		"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens",
		"reasoning_tokens", "total_tokens", "charge_value", "charge_unit", "duration_ms",
		"api_key_name", "source_fetched_at",
	} {
		if _, ok := usageColumns[required]; !ok {
			t.Fatalf("site_usage_records missing %s", required)
		}
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("idempotent reopen: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRequestRouteSnapshotMigrationBackfillsHonestLegacyAccountingState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "request-route-v11.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (
version INTEGER PRIMARY KEY,
name TEXT NOT NULL,
applied_at INTEGER NOT NULL
)`); err != nil {
		t.Fatal(err)
	}
	for _, item := range migrations[:11] {
		if _, err := db.ExecContext(ctx, item.sql); err != nil {
			t.Fatalf("apply historical migration %d: %v", item.version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,applied_at) VALUES (?,?,0)`, item.version, item.name); err != nil {
			t.Fatal(err)
		}
	}
	digest := make([]byte, 32)
	digest[0] = 1
	result, err := db.ExecContext(ctx, `INSERT INTO downstream_keys(
name,key_prefix,key_digest,enabled,rpm_limit,created_at,updated_at
) VALUES ('Legacy key','js_legacy',?,1,0,0,0)`, digest)
	if err != nil {
		t.Fatal(err)
	}
	keyID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	insertRequest := func(id, status string, inputTokens any, officialCost, charged int64, finishedAt any) {
		t.Helper()
		_, insertErr := db.ExecContext(ctx, `INSERT INTO request_logs(
id,downstream_key_id,downstream_key_name_snapshot,published_model_id,published_model_revision,
effective_routing_profile_id,effective_routing_profile_name_snapshot,
source_routing_profile_id,source_routing_profile_name_snapshot,public_model,api_surface,is_stream,
price_catalog_version,price_sku,reservation_nano_usd,status,input_tokens,
official_cost_nano_usd,charged_nano_usd,started_at,finished_at
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, keyID, "Legacy key", 1, 1, 1, "Default", 1, "Default", "legacy-model",
			"openai.chat_completions", 0, "legacy-catalog", "legacy-sku", 0, status,
			inputTokens, officialCost, charged, 1_000, finishedAt)
		if insertErr != nil {
			t.Fatalf("insert historical request %q: %v", id, insertErr)
		}
	}
	insertRequest("legacy-running", "running", nil, 0, 0, nil)
	insertRequest("legacy-metered-usage", "success", int64(0), 0, 0, int64(1_100))
	insertRequest("legacy-metered-cost", "success", nil, 25, 20, int64(1_100))
	insertRequest("legacy-unavailable", "success", nil, 0, 0, int64(1_100))
	insertRequest("legacy-failed", "failed", nil, 0, 0, int64(1_100))
	insertRequest("legacy-cancelled-metered", "cancelled", int64(3), 0, 0, int64(1_100))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	want := map[string]struct {
		status string
		code   string
	}{
		"legacy-running":           {status: "pending"},
		"legacy-metered-usage":     {status: "metered"},
		"legacy-metered-cost":      {status: "metered"},
		"legacy-unavailable":       {status: "unavailable", code: "legacy_usage_unavailable"},
		"legacy-failed":            {status: "not_applicable"},
		"legacy-cancelled-metered": {status: "metered"},
	}
	for id, expected := range want {
		var routeRevision int64
		var meteringStatus string
		var meteringError sql.NullString
		if err := upgraded.DB.QueryRowContext(ctx, `SELECT route_revision,metering_status,metering_error_code
FROM request_logs WHERE id=?`, id).Scan(&routeRevision, &meteringStatus, &meteringError); err != nil {
			t.Fatal(err)
		}
		if routeRevision != 0 || meteringStatus != expected.status || meteringError.String != expected.code {
			t.Fatalf("legacy request %q = route %d, status %q, code %q", id, routeRevision, meteringStatus, meteringError.String)
		}
	}
	assertTableCount(t, upgraded, "request_route_candidates", 0)
}

func TestMultiplierConstraintMigrationPreservesRequestAuditChildren(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "request-billing-v13.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (
version INTEGER PRIMARY KEY,
name TEXT NOT NULL,
applied_at INTEGER NOT NULL
)`); err != nil {
		t.Fatal(err)
	}
	for _, item := range migrations[:13] {
		if _, err := db.ExecContext(ctx, item.sql); err != nil {
			t.Fatalf("apply historical migration %d: %v", item.version, err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO schema_migrations(version,name,applied_at) VALUES (?,?,0)`, item.version, item.name); err != nil {
			t.Fatal(err)
		}
	}
	digest := make([]byte, 32)
	digest[0] = 1
	keyResult, err := db.ExecContext(ctx, `INSERT INTO downstream_keys(
name,key_prefix,key_digest,enabled,rpm_limit,created_at,updated_at
) VALUES ('Migration key','js_migration',?,1,0,1000,1000)`, digest)
	if err != nil {
		t.Fatal(err)
	}
	keyID, err := keyResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO request_logs(
id,downstream_key_id,downstream_key_name_snapshot,published_model_id,published_model_revision,
effective_routing_profile_id,effective_routing_profile_name_snapshot,
source_routing_profile_id,source_routing_profile_name_snapshot,route_revision,public_model,api_surface,
is_stream,price_catalog_version,price_sku,billing_multiplier_bps,reservation_nano_usd,status,
metering_status,official_cost_nano_usd,charged_nano_usd,started_at,finished_at
) VALUES ('migration-request',?,'Migration key',1,1,1,'Default',1,'Default',1,'model',
'openai.chat_completions',0,'catalog','sku',10000,10,'success','metered',10,10,1000,1100)`, keyID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO request_attempts(
request_id,attempt_index,published_model_target_id,published_model_target_revision,
provider_model_target_id,provider_model_target_revision,site_id,endpoint_id,credential_id,
site_name_snapshot,endpoint_name_snapshot,credential_name_snapshot,source_model,response_model,
wire_protocol,api_surface,status,http_status,duration_ms,started_at,finished_at
) VALUES ('migration-request',0,1,1,1,1,1,1,1,'Site','Endpoint','Key','model','model',
'openai','openai.chat_completions','success',200,100,1000,1100)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO request_route_candidates(
request_id,position,published_model_target_id,published_model_target_revision,
provider_model_target_id,provider_model_target_revision,site_id,site_name_snapshot,endpoint_id,
endpoint_name_snapshot,source_model,wire_protocol,api_surface,credentials_json,
initial_eligibility,initial_reason,disposition,disposition_reason,attempt_count,
first_attempt_index,last_attempt_index
) VALUES ('migration-request',0,1,1,1,1,1,'Site',1,'Endpoint','model','openai',
'openai.chat_completions','[]','eligible','ready','attempted','success',1,0,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO quota_ledger(
downstream_key_id,request_id,event_type,reserved_delta_nano_usd,used_delta_nano_usd,
price_catalog_version,price_sku,created_at
) VALUES (?,'migration-request','settle',-10,10,'catalog','sku',1100)`, keyID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	request, err := upgraded.GetRequestLog(ctx, "migration-request")
	if err != nil {
		t.Fatal(err)
	}
	if request.OfficialCostNanoUSD != 10 || request.ChargedNanoUSD != 10 || request.BillingMultiplierBPS != 10_000 {
		t.Fatalf("migrated request = %+v", request)
	}
	for table, want := range map[string]int{
		"request_attempts": 1, "request_route_candidates": 1, "quota_ledger": 1,
	} {
		var got int
		if err := upgraded.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+
			` WHERE request_id='migration-request'`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s rows = %d, want %d", table, got, want)
		}
	}
}

func TestBillingAndTimeoutMigrationsUpgradeV12DataWithoutRepricingHistory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "billing-policy-v12.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (
version INTEGER PRIMARY KEY,
name TEXT NOT NULL,
applied_at INTEGER NOT NULL
)`); err != nil {
		t.Fatal(err)
	}
	for _, item := range migrations[:12] {
		if _, err := db.ExecContext(ctx, item.sql); err != nil {
			t.Fatalf("apply historical migration %d: %v", item.version, err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO schema_migrations(version,name,applied_at) VALUES (?,?,0)`, item.version, item.name); err != nil {
			t.Fatal(err)
		}
	}
	digest := make([]byte, 32)
	digest[0] = 7
	keyResult, err := db.ExecContext(ctx, `INSERT INTO downstream_keys(
name,key_prefix,key_digest,enabled,quota_nano_usd,used_nano_usd,reserved_nano_usd,rpm_limit,created_at,updated_at
) VALUES ('Legacy billed key','js_legacy_billed',?,1,500,120,30,45,1000,1000)`, digest)
	if err != nil {
		t.Fatal(err)
	}
	keyID, err := keyResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO request_logs(
id,downstream_key_id,downstream_key_name_snapshot,published_model_id,published_model_revision,
effective_routing_profile_id,effective_routing_profile_name_snapshot,
source_routing_profile_id,source_routing_profile_name_snapshot,route_revision,public_model,api_surface,
is_stream,price_catalog_version,price_sku,reservation_nano_usd,status,metering_status,
official_cost_nano_usd,charged_nano_usd,started_at,finished_at
) VALUES ('legacy-billed-request',?,'Legacy billed key',1,1,1,'Default',1,'Default',1,'model',
'openai.chat_completions',0,'catalog','sku',10,'success','metered',10,10,1000,1100)`, keyID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO request_logs(
id,downstream_key_id,downstream_key_name_snapshot,published_model_id,published_model_revision,
effective_routing_profile_id,effective_routing_profile_name_snapshot,
source_routing_profile_id,source_routing_profile_name_snapshot,route_revision,public_model,api_surface,
is_stream,price_catalog_version,price_sku,reservation_nano_usd,status,metering_status,
official_cost_nano_usd,charged_nano_usd,started_at
) VALUES ('legacy-running-request',?,'Legacy billed key',1,1,1,'Default',1,'Default',1,'model',
'openai.chat_completions',0,'catalog','sku',30,'running','pending',0,0,2000)`, keyID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE runtime_settings
SET first_output_timeout_ms=30000,revision=1,updated_at=1234 WHERE singleton_id=1`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	key, err := upgraded.GetDownstreamKey(ctx, keyID)
	if err != nil {
		t.Fatal(err)
	}
	if key.UsedNanoUSD != 120 || key.ReservedNanoUSD != 30 || key.RPMLimit != 0 ||
		key.HourlyQuotaNanoUSD != nil || key.BillingMultiplierBPS != DefaultBillingMultiplierBPS ||
		key.UsedThisHourNanoUSD != 0 || key.ReservedThisHourNanoUSD != 0 {
		t.Fatalf("upgraded downstream key = %+v", key)
	}
	request, err := upgraded.GetRequestLog(ctx, "legacy-billed-request")
	if err != nil {
		t.Fatal(err)
	}
	if request.BillingMultiplierBPS != DefaultBillingMultiplierBPS || request.OfficialCostNanoUSD != 10 ||
		request.ChargedNanoUSD != 10 || request.QuotaCapped {
		t.Fatalf("historical request was repriced during migration: %+v", request)
	}
	var hourlyReserved int64
	if err := upgraded.DB.QueryRowContext(ctx, `SELECT reserved_nano_usd
FROM downstream_key_hourly_usage WHERE downstream_key_id=? AND window_started_at=0`, keyID).Scan(&hourlyReserved); err != nil {
		t.Fatal(err)
	}
	if hourlyReserved != 30 {
		t.Fatalf("legacy running reservation was not materialized: %d", hourlyReserved)
	}
	if recovered, err := upgraded.RecoverInterruptedRequests(ctx, 3_000); err != nil || recovered != 1 {
		t.Fatalf("recover migrated running request = %d, %v", recovered, err)
	}
	key, err = upgraded.GetDownstreamKey(ctx, keyID)
	if err != nil {
		t.Fatal(err)
	}
	if key.ReservedNanoUSD != 0 || key.UsedNanoUSD != 120 {
		t.Fatalf("migrated recovery leaked global reservation: %+v", key)
	}
	if err := upgraded.DB.QueryRowContext(ctx, `SELECT reserved_nano_usd
FROM downstream_key_hourly_usage WHERE downstream_key_id=? AND window_started_at=0`, keyID).Scan(&hourlyReserved); err != nil {
		t.Fatal(err)
	}
	if hourlyReserved != 0 {
		t.Fatalf("migrated recovery leaked hourly reservation: %d", hourlyReserved)
	}
	settings, err := upgraded.GetRuntimeSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.FirstOutputTimeout != 15*time.Second || settings.Revision != 1 || settings.UpdatedAt.UnixMilli() != 1234 {
		t.Fatalf("upgraded runtime settings = %+v", settings)
	}
}

func TestHourlyReservationRepairMigrationFixesAlreadyUpgradedDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hourly-reservation-v15.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (
version INTEGER PRIMARY KEY,
name TEXT NOT NULL,
applied_at INTEGER NOT NULL
)`); err != nil {
		t.Fatal(err)
	}
	for _, item := range migrations[:15] {
		if _, err := db.ExecContext(ctx, item.sql); err != nil {
			t.Fatalf("apply historical migration %d: %v", item.version, err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO schema_migrations(version,name,applied_at) VALUES (?,?,0)`, item.version, item.name); err != nil {
			t.Fatal(err)
		}
	}
	digest := make([]byte, 32)
	digest[0] = 8
	keyResult, err := db.ExecContext(ctx, `INSERT INTO downstream_keys(
name,key_prefix,key_digest,enabled,quota_nano_usd,used_nano_usd,reserved_nano_usd,rpm_limit,
hourly_quota_nano_usd,billing_multiplier_bps,created_at,updated_at
) VALUES ('Already upgraded key','js_already_upgraded',?,1,500,120,30,0,200,10000,1000,1000)`, digest)
	if err != nil {
		t.Fatal(err)
	}
	keyID, err := keyResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO request_logs(
id,downstream_key_id,downstream_key_name_snapshot,published_model_id,published_model_revision,
effective_routing_profile_id,effective_routing_profile_name_snapshot,
source_routing_profile_id,source_routing_profile_name_snapshot,route_revision,public_model,api_surface,
is_stream,price_catalog_version,price_sku,reservation_nano_usd,billing_multiplier_bps,status,metering_status,
official_cost_nano_usd,charged_nano_usd,started_at
) VALUES ('already-upgraded-running',?,'Already upgraded key',1,1,1,'Default',1,'Default',1,'model',
'openai.chat_completions',0,'catalog','sku',30,10000,'running','pending',0,0,3601000)`, keyID); err != nil {
		t.Fatal(err)
	}
	var hourlyRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM downstream_key_hourly_usage WHERE downstream_key_id=?`, keyID).Scan(&hourlyRows); err != nil {
		t.Fatal(err)
	}
	if hourlyRows != 0 {
		t.Fatalf("test fixture unexpectedly has %d hourly rows", hourlyRows)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var hourlyReserved int64
	if err := upgraded.DB.QueryRowContext(ctx, `SELECT reserved_nano_usd
FROM downstream_key_hourly_usage WHERE downstream_key_id=? AND window_started_at=3600000`, keyID).Scan(&hourlyReserved); err != nil {
		t.Fatal(err)
	}
	if hourlyReserved != 30 {
		t.Fatalf("repair migration reserved amount = %d, want 30", hourlyReserved)
	}
	if recovered, err := upgraded.RecoverInterruptedRequests(ctx, 3_700_000); err != nil || recovered != 1 {
		t.Fatalf("recover repaired running request = %d, %v", recovered, err)
	}
	key, err := upgraded.GetDownstreamKey(ctx, keyID)
	if err != nil {
		t.Fatal(err)
	}
	if key.ReservedNanoUSD != 0 || key.ReservedThisHourNanoUSD != 0 || key.UsedNanoUSD != 120 {
		t.Fatalf("repaired recovery leaked reservation: %+v", key)
	}
}

func TestFirstOutputDefaultMigrationOnlyUpgradesBootstrapPolicy(t *testing.T) {
	for _, test := range []struct {
		name        string
		revision    int64
		updatedAt   int64
		wantTimeout int64
	}{
		{name: "bootstrap", revision: 1, updatedAt: 1234, wantTimeout: 15_000},
		{name: "administrator saved", revision: 2, updatedAt: 5678, wantTimeout: 30_000},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "runtime-settings-v14.db")
			db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(1)")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (
version INTEGER PRIMARY KEY,
name TEXT NOT NULL,
applied_at INTEGER NOT NULL
)`); err != nil {
				t.Fatal(err)
			}
			for _, item := range migrations[:14] {
				if _, err := db.ExecContext(ctx, item.sql); err != nil {
					t.Fatalf("apply historical migration %d: %v", item.version, err)
				}
				if _, err := db.ExecContext(ctx,
					`INSERT INTO schema_migrations(version,name,applied_at) VALUES (?,?,0)`, item.version, item.name); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.ExecContext(ctx, `UPDATE runtime_settings
SET first_output_timeout_ms=30000,revision=?,updated_at=? WHERE singleton_id=1`, test.revision, test.updatedAt); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			upgraded, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer upgraded.Close()
			var timeout, revision, updatedAt int64
			if err := upgraded.DB.QueryRowContext(ctx, `SELECT first_output_timeout_ms,revision,updated_at
FROM runtime_settings WHERE singleton_id=1`).Scan(&timeout, &revision, &updatedAt); err != nil {
				t.Fatal(err)
			}
			if timeout != test.wantTimeout || revision != test.revision || updatedAt != test.updatedAt {
				t.Fatalf("upgraded policy = timeout %d revision %d updatedAt %d", timeout, revision, updatedAt)
			}
		})
	}
}

func TestSiteEndpointRequiresAnExactProtocolSurfacePair(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	siteID := mustCreateSite(t, s, "Exact protocol")
	for name, input := range map[string]SiteEndpointWrite{
		"missing surface": {
			Name: "Missing", BaseURL: "https://missing.example/v1", WireProtocol: "openai", Enabled: true,
		},
		"mismatched surface": {
			Name: "Mismatch", BaseURL: "https://mismatch.example/v1", WireProtocol: "openai",
			Surface: "anthropic.messages", Enabled: true,
		},
		"ambiguous legacy label": {
			Name: "Ambiguous", BaseURL: "https://ambiguous.example/v1", WireProtocol: "openai_chat_completions",
			Surface: "openai.chat_completions", Enabled: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.CreateSiteEndpoint(ctx, siteID, input); err == nil {
				t.Fatalf("CreateSiteEndpoint(%s) unexpectedly succeeded", name)
			}
		})
	}
	id, err := s.CreateSiteEndpoint(ctx, siteID, SiteEndpointWrite{
		Name: "Responses", BaseURL: "https://responses.example/v1", WireProtocol: "openai",
		Surface: "openai.responses", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := s.GetSiteEndpoint(ctx, id)
	if err != nil || endpoint.WireProtocol != "openai" || endpoint.Surface != "openai.responses" {
		t.Fatalf("stored endpoint = %+v, %v", endpoint, err)
	}
}

func TestOpenRefusesLegacySchemaHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (
version INTEGER PRIMARY KEY,
name TEXT NOT NULL,
applied_at INTEGER NOT NULL
); INSERT INTO schema_migrations(version,name,applied_at) VALUES (1,'initial_v1',0);`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), path)
	if err == nil {
		_ = s.Close()
		t.Fatal("VNext store opened a legacy schema history")
	}
	if !strings.Contains(err.Error(), "migration 1 name mismatch") {
		t.Fatalf("legacy schema error = %v", err)
	}
}

func TestCredentialBindingsAndModelAccessCannotCrossSiteOrEndpoint(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	siteA := mustCreateSite(t, s, "Alpha")
	siteB := mustCreateSite(t, s, "Beta")
	endpointA := mustCreateEndpoint(t, s, siteA, "Alpha primary", "https://alpha.example/v1")
	endpointB := mustCreateEndpoint(t, s, siteB, "Beta primary", "https://beta.example/v1")
	credentialA := mustCreateCredential(t, s, siteA, "Alpha key")
	unboundCredentialA := mustCreateCredential(t, s, siteA, "Alpha spare")
	credentialB := mustCreateCredential(t, s, siteB, "Beta key")

	mustReplaceBindings(t, s, siteA, endpointA, []int64{credentialA})
	bindings, err := s.ListEndpointCredentialBindings(ctx, endpointA)
	if err != nil || len(bindings) != 1 || bindings[0].CredentialID != credentialA {
		t.Fatalf("explicit bindings = %+v, %v", bindings, err)
	}
	for _, item := range bindings {
		if item.CredentialID == unboundCredentialA {
			t.Fatal("unbound same-site credential was inherited")
		}
	}

	endpoint, err := s.GetSiteEndpoint(ctx, endpointA)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceEndpointCredentialBindings(ctx, siteA, endpointA, endpoint.Revision, []int64{credentialB}); err == nil {
		t.Fatal("cross-site binding through Store unexpectedly succeeded")
	}
	bindings, err = s.ListEndpointCredentialBindings(ctx, endpointA)
	if err != nil || len(bindings) != 1 || bindings[0].CredentialID != credentialA {
		t.Fatalf("failed replacement changed bindings = %+v, %v", bindings, err)
	}

	now := NowMS()
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO credential_endpoint_bindings(
site_id,endpoint_id,credential_id,position,enabled,created_at,updated_at) VALUES (?,?,?,?,1,?,?)`,
		siteA, endpointA, credentialB, 1, now, now); err == nil {
		t.Fatal("cross-site binding bypassed SQLite composite foreign keys")
	}

	targetA := mustCreateProviderTarget(t, s, siteA, endpointA, "model-a")
	if _, err := s.CreateProviderModelTarget(ctx, ProviderModelTargetWrite{
		SiteID: siteA, EndpointID: endpointB, SourceModel: "cross-site", Enabled: true,
	}); err == nil {
		t.Fatal("provider target crossed site/endpoint ownership")
	}
	if err := s.UpsertCredentialTargetAccess(ctx, CredentialTargetAccessWrite{
		SiteID: siteA, EndpointID: endpointA, CredentialID: credentialA,
		ProviderModelTargetID: targetA, Availability: "supported",
	}); err != nil {
		t.Fatalf("same binding target access: %v", err)
	}
	targetB := mustCreateProviderTarget(t, s, siteB, endpointB, "model-b")
	if err := s.UpsertCredentialTargetAccess(ctx, CredentialTargetAccessWrite{
		SiteID: siteB, EndpointID: endpointB, CredentialID: credentialB,
		ProviderModelTargetID: targetB, Availability: "forbidden",
	}); err == nil {
		t.Fatal("credential target access succeeded without an endpoint binding")
	}
	if err := s.UpsertCredentialTargetAccess(ctx, CredentialTargetAccessWrite{
		SiteID: siteA, EndpointID: endpointA, CredentialID: credentialA,
		ProviderModelTargetID: targetB, Availability: "supported",
	}); err == nil {
		t.Fatal("credential target access crossed endpoint/site ownership")
	}
}

func TestPublishedModelsAreGlobalAndProfilesStoreSparseOrderedOverrides(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	siteA := mustCreateSite(t, s, "Alpha")
	endpointA1 := mustCreateEndpoint(t, s, siteA, "Alpha primary", "https://alpha.example/v1")
	endpointA2 := mustCreateEndpoint(t, s, siteA, "Alpha backup", "https://alpha.example/openai")
	endpointA3 := mustCreateEndpoint(t, s, siteA, "Alpha reserve", "https://alpha.example/api")
	credentialA := mustCreateCredential(t, s, siteA, "Alpha key")
	for _, endpointID := range []int64{endpointA1, endpointA2, endpointA3} {
		mustReplaceBindings(t, s, siteA, endpointID, []int64{credentialA})
	}
	targetA1 := mustCreateProviderTarget(t, s, siteA, endpointA1, "claude-sonnet")
	targetA2 := mustCreateProviderTarget(t, s, siteA, endpointA2, "claude-sonnet")
	targetA3 := mustCreateProviderTarget(t, s, siteA, endpointA3, "claude-sonnet")

	siteB := mustCreateSite(t, s, "Beta")
	endpointB := mustCreateEndpoint(t, s, siteB, "Beta primary", "https://beta.example/v1")
	credentialB := mustCreateCredential(t, s, siteB, "Beta key")
	mustReplaceBindings(t, s, siteB, endpointB, []int64{credentialB})
	targetB := mustCreateProviderTarget(t, s, siteB, endpointB, "claude-sonnet")

	model, err := s.CreatePublishedModel(ctx, PublishedModelWrite{
		PublicName: "claude-sonnet", OfficialPriceSKU: "claude-sonnet", Enabled: true,
	}, []int64{targetA2, targetA1, targetB})
	if err != nil {
		t.Fatalf("create global published model: %v", err)
	}
	if model.PublicName != "claude-sonnet" || model.OfficialPriceSKU != "claude-sonnet" {
		t.Fatalf("published model defaults = %+v", model)
	}
	wantOrder := []int64{targetA2, targetA1, targetB}
	if len(model.Targets) != len(wantOrder) {
		t.Fatalf("targets = %+v", model.Targets)
	}
	for index, wantTargetID := range wantOrder {
		if model.Targets[index].ProviderModelTargetID != wantTargetID || model.Targets[index].Position != index {
			t.Fatalf("target[%d] = %+v, want provider target ID %d", index, model.Targets[index], wantTargetID)
		}
	}

	if _, err := s.CreatePublishedModel(ctx, PublishedModelWrite{
		PublicName: model.PublicName, OfficialPriceSKU: "duplicate", Enabled: true,
	}, []int64{targetA3}); err == nil {
		t.Fatal("duplicate global published model name unexpectedly succeeded")
	}

	profile, err := s.CreateRoutingProfile(ctx, "Priority B")
	if err != nil {
		t.Fatal(err)
	}
	inherited, err := s.GetRoutingProfileRoute(ctx, profile.ID, model.ID)
	if err != nil || !inherited.Inherited || inherited.SourceProfileName != "Default" ||
		!reflect.DeepEqual(publishedTargetProviderIDs(inherited.Targets), wantOrder) {
		t.Fatalf("inherited route = %+v, %v", inherited, err)
	}

	overrideOrder := []int64{model.Targets[2].ID, model.Targets[0].ID}
	override, err := s.CreateRoutingProfileRoute(ctx, profile.ID, profile.Revision, RoutingProfileRouteWrite{
		PublishedModelID: model.ID, Enabled: true, TargetIDs: overrideOrder,
	})
	if err != nil {
		t.Fatal(err)
	}
	if override.Inherited || !override.TargetsOverridden || override.SourceProfileID != profile.ID ||
		!reflect.DeepEqual(publishedTargetProviderIDs(override.Targets), []int64{targetB, targetA2}) {
		t.Fatalf("custom route = %+v", override)
	}
	for index, target := range override.Targets {
		if target.Position != index {
			t.Fatalf("custom target[%d] position = %d", index, target.Position)
		}
	}
	if _, err := s.ReplaceRoutingProfileRouteTargets(ctx, profile.ID, model.ID, override.Revision, nil); err == nil {
		t.Fatal("empty explicit target override unexpectedly succeeded")
	}
	otherModel, err := s.CreatePublishedModel(ctx, PublishedModelWrite{
		PublicName: "other-model", OfficialPriceSKU: "other-model", Enabled: true,
	}, []int64{targetA3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplaceRoutingProfileRouteTargets(ctx, profile.ID, model.ID, override.Revision, []int64{otherModel.Targets[0].ID}); err == nil {
		t.Fatal("profile accepted a target outside the default model target set")
	}
	if _, err := s.ReplacePublishedModelTargets(ctx, model.ID, model.Revision, []int64{targetA2, targetA1}); !errors.Is(err, ErrPublishedTargetInUse) {
		t.Fatalf("removing an overridden default target = %v, want ErrPublishedTargetInUse", err)
	}
	if err := s.DeleteRoutingProfileRoute(ctx, profile.ID, model.ID, override.Revision); err != nil {
		t.Fatal(err)
	}
	restored, err := s.GetRoutingProfileRoute(ctx, profile.ID, model.ID)
	if err != nil || !restored.Inherited || restored.SourceProfileName != "Default" ||
		!reflect.DeepEqual(publishedTargetProviderIDs(restored.Targets), wantOrder) {
		t.Fatalf("restored inherited route = %+v, %v", restored, err)
	}

	defaultProfile, err := s.GetDefaultRoutingProfile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRoutingProfileRoute(ctx, defaultProfile.ID, defaultProfile.Revision, RoutingProfileRouteWrite{
		PublishedModelID: model.ID, Enabled: false,
	}); !errors.Is(err, ErrDefaultRoutingProfile) {
		t.Fatalf("default profile override error = %v", err)
	}

	defaultKey := mustCreateDownstreamKey(t, s, "Default client", "js_default", nil, 0)
	profileID := profile.ID
	customKey, err := s.ImportDigestOnlyDownstreamKey(ctx, DownstreamKeyWrite{
		Name: "Custom client", KeyPrefix: "js_custom", KeyDigest: DigestDownstreamKey("custom-secret"),
		RoutingProfileID: &profileID, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defaultKeyView, err := s.GetDownstreamKey(ctx, defaultKey)
	if err != nil || !defaultKeyView.UsesDefaultRoutingProfile || defaultKeyView.RoutingProfileID != defaultProfile.ID {
		t.Fatalf("default key profile = %+v, %v", defaultKeyView, err)
	}
	customKeyView, err := s.GetDownstreamKey(ctx, customKey)
	if err != nil || customKeyView.UsesDefaultRoutingProfile || customKeyView.RoutingProfileID != profile.ID {
		t.Fatalf("custom key profile = %+v, %v", customKeyView, err)
	}
}

func TestEncryptedFieldsAndNanoUSDConstraints(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	siteID := mustCreateSite(t, s, "Secrets")

	if _, err := s.CreateSiteEndpoint(ctx, siteID, SiteEndpointWrite{
		Name: "Unsafe", BaseURL: "https://unsafe.example/v1", WireProtocol: "openai", Surface: "openai.chat_completions",
		HeaderTemplate: []byte(`{"Authorization":"Bearer plaintext"}`), Enabled: true,
	}); err == nil || !strings.Contains(err.Error(), "secret_headers_cipher") {
		t.Fatalf("sensitive public header error = %v", err)
	}
	if _, err := s.CreateSiteEndpoint(ctx, siteID, SiteEndpointWrite{
		Name: "Missing version", BaseURL: "https://headers.example/v1", WireProtocol: "openai", Surface: "openai.chat_completions",
		SecretHeadersCipher: []byte("cipher"), Enabled: true,
	}); err == nil {
		t.Fatal("secret headers without cipher version unexpectedly succeeded")
	}
	endpointID, err := s.CreateSiteEndpoint(ctx, siteID, SiteEndpointWrite{
		Name: "Safe", BaseURL: "https://safe.example/v1", WireProtocol: "openai", Surface: "openai.chat_completions",
		HeaderTemplate: []byte(`{"X-Client":"JieShan"}`), SecretHeadersCipher: []byte("cipher"),
		SecretHeadersCipherVersion: 1, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := s.GetSiteEndpoint(ctx, endpointID)
	if err != nil || !endpoint.SecretHeadersConfigured || endpoint.SecretHeadersCipherVersion != 1 {
		t.Fatalf("endpoint secret metadata = %+v, %v", endpoint, err)
	}
	if _, err := s.CreateSiteCredential(ctx, siteID, SiteCredentialWrite{
		Name: "No version", SecretCipher: []byte("cipher"), Enabled: true,
	}); err == nil {
		t.Fatal("credential without cipher version unexpectedly succeeded")
	}

	negative := int64(-1)
	if _, err := s.ImportDigestOnlyDownstreamKey(ctx, DownstreamKeyWrite{
		Name: "Negative", KeyPrefix: "js_negative", KeyDigest: DigestDownstreamKey("negative"),
		Enabled: true, QuotaNanoUSD: &negative,
	}); err == nil {
		t.Fatal("negative nano-USD quota unexpectedly succeeded")
	}
	digestOnly := mustCreateDownstreamKey(t, s, "Rotated legacy", "js_legacy", nil, 0)
	revealable := mustCreateDownstreamKey(t, s, "Revealable", "js_reveal", []byte("encrypted-secret"), 3)
	key, err := s.GetDownstreamKey(ctx, digestOnly)
	if err != nil || key.Revealable || key.RevealVersion != 0 {
		t.Fatalf("digest-only key = %+v, %v", key, err)
	}
	key, err = s.GetDownstreamKey(ctx, revealable)
	if err != nil || !key.Revealable || key.RevealVersion != 3 {
		t.Fatalf("revealable key = %+v, %v", key, err)
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return openTestStoreAt(t, filepath.Join(t.TempDir(), "vnext.db"))
}

func openTestStoreAt(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustCreateSite(t *testing.T, s *Store, name string) int64 {
	t.Helper()
	id, err := s.CreateSite(context.Background(), SiteWrite{Name: name, Enabled: true})
	if err != nil {
		t.Fatalf("CreateSite(%s): %v", name, err)
	}
	return id
}

func mustCreateEndpoint(t *testing.T, s *Store, siteID int64, name, baseURL string) int64 {
	t.Helper()
	id, err := s.CreateSiteEndpoint(context.Background(), siteID, SiteEndpointWrite{
		Name: name, BaseURL: baseURL, WireProtocol: "openai", Surface: "openai.chat_completions", Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateSiteEndpoint(%s): %v", name, err)
	}
	return id
}

func mustCreateCredential(t *testing.T, s *Store, siteID int64, name string) int64 {
	t.Helper()
	id, err := s.CreateSiteCredential(context.Background(), siteID, SiteCredentialWrite{
		Name: name, SecretCipher: []byte("encrypted-" + name), CipherVersion: 1, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateSiteCredential(%s): %v", name, err)
	}
	return id
}

func mustReplaceBindings(t *testing.T, s *Store, siteID, endpointID int64, credentialIDs []int64) {
	t.Helper()
	endpoint, err := s.GetSiteEndpoint(context.Background(), endpointID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceEndpointCredentialBindings(context.Background(), siteID, endpointID, endpoint.Revision, credentialIDs); err != nil {
		t.Fatalf("ReplaceEndpointCredentialBindings: %v", err)
	}
}

func mustCreateProviderTarget(t *testing.T, s *Store, siteID, endpointID int64, sourceModel string) int64 {
	t.Helper()
	id, err := s.CreateProviderModelTarget(context.Background(), ProviderModelTargetWrite{
		SiteID: siteID, EndpointID: endpointID, SourceModel: sourceModel, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateProviderModelTarget(%s): %v", sourceModel, err)
	}
	return id
}

func mustCreateDownstreamKey(t *testing.T, s *Store, name, prefix string, encryptedSecret []byte, revealVersion int64) int64 {
	t.Helper()
	input := DownstreamKeyWrite{Name: name, KeyPrefix: prefix, KeyDigest: DigestDownstreamKey("raw-" + name), Enabled: true}
	var id int64
	var err error
	if len(encryptedSecret) == 0 {
		id, err = s.ImportDigestOnlyDownstreamKey(context.Background(), input)
	} else {
		item, createErr := s.CreateRevealableDownstreamKey(context.Background(), input, revealVersion, func(int64) ([]byte, error) {
			return encryptedSecret, nil
		})
		id, err = item.ID, createErr
	}
	if err != nil {
		t.Fatalf("create downstream key %s: %v", name, err)
	}
	return id
}

func tableColumns(t *testing.T, s *Store, table string) map[string]string {
	t.Helper()
	rows, err := s.DB.Query(`PRAGMA table_info('` + table + `')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := make(map[string]string)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		result[name] = strings.ToUpper(columnType)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
