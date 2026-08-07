package vnextmigration

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	vnextsecretbox "github.com/LuTianTian001/JieShan/internal/vnext/secretbox"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"

	_ "modernc.org/sqlite"
)

func TestMigrateSQLiteFilePreservesCoreStateAndResealsSecrets(t *testing.T) {
	key := bytes.Repeat([]byte{0x31}, 32)
	sourcePath := newMigrationFixture(t, key)
	before := fileDigest(t, sourcePath)
	destination := filepath.Join(t.TempDir(), "vnext.sqlite")

	result, err := MigrateSQLiteFile(context.Background(), sourcePath, destination, MigrationOptions{
		MasterKey: key, OpenAISurface: OpenAISurfaceBoth, NowMS: 1_800_000_000_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if after := fileDigest(t, sourcePath); after != before {
		t.Fatalf("source database changed: before %x after %x", before, after)
	}
	if result.Sites != 1 || result.Endpoints != 2 || result.Credentials != 1 || result.ProviderModelTargets != 6 ||
		result.PublishedModels != 3 || result.PublishedModelTargets != 6 || result.DownstreamKeys != 1 ||
		result.NonRevealableDownstreamKeys != 1 || result.SiteAccountConnections != 1 ||
		result.BalanceSnapshots != 1 || result.SiteUsageRecords != 1 {
		t.Fatalf("unexpected migration result: %+v", result)
	}

	storage, err := vnextstore.Open(context.Background(), destination)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	box, err := vnextsecretbox.New(key)
	if err != nil {
		t.Fatal(err)
	}

	var credentialID, siteID int64
	var credentialCipher []byte
	if err := storage.DB.QueryRow(`SELECT id,site_id,secret_cipher FROM site_credentials`).
		Scan(&credentialID, &siteID, &credentialCipher); err != nil {
		t.Fatal(err)
	}
	credentialPlaintext, err := box.Open(vnextsecretbox.PurposeSiteCredential,
		vnextsecretbox.Identity{RecordID: credentialID, OwnerID: siteID}, credentialCipher)
	if err != nil || string(credentialPlaintext) != "sk-upstream-secret" {
		t.Fatalf("credential plaintext = %q, err %v", credentialPlaintext, err)
	}

	rows, err := storage.DB.Query(`SELECT id,secret_headers_cipher,cipher_version FROM site_endpoints ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	endpointCount := 0
	for rows.Next() {
		var endpointID, cipherVersion int64
		var ciphertext []byte
		if err := rows.Scan(&endpointID, &ciphertext, &cipherVersion); err != nil {
			t.Fatal(err)
		}
		if cipherVersion != 1 {
			t.Fatalf("endpoint %d cipher version = %d", endpointID, cipherVersion)
		}
		plaintext, err := box.Open(vnextsecretbox.PurposeSiteSecretHeaders,
			vnextsecretbox.Identity{RecordID: endpointID, OwnerID: siteID}, ciphertext)
		if err != nil || !strings.Contains(string(plaintext), "legacy-header-secret") {
			t.Fatalf("endpoint secret headers = %q, err %v", plaintext, err)
		}
		endpointCount++
	}
	rows.Close()
	if endpointCount != 2 {
		t.Fatalf("endpoint count = %d", endpointCount)
	}

	var connectionID int64
	var accountCipher []byte
	if err := storage.DB.QueryRow(`SELECT id,secrets_cipher FROM site_account_connections WHERE site_id=?`, siteID).
		Scan(&connectionID, &accountCipher); err != nil {
		t.Fatal(err)
	}
	accountPlaintext, err := box.Open(vnextsecretbox.PurposeSiteAdministration,
		vnextsecretbox.Identity{RecordID: connectionID, OwnerID: siteID}, accountCipher)
	if err != nil {
		t.Fatal(err)
	}
	var account map[string]any
	if err := json.Unmarshal(accountPlaintext, &account); err != nil {
		t.Fatal(err)
	}
	if account["accessToken"] != "access-token" || account["refreshToken"] != "refresh-token" ||
		account["authorization"] != "Bearer access-token" {
		t.Fatalf("converted account secret = %#v", account)
	}

	keys, err := storage.ListDownstreamKeyAuthCandidates(context.Background(), "sk-old")
	if err != nil || len(keys) != 1 {
		t.Fatalf("downstream auth candidates = %+v, %v", keys, err)
	}
	wantDigest := sha256.Sum256([]byte("sk-old-plaintext"))
	if keys[0].KeyDigest != wantDigest || keys[0].Key.QuotaNanoUSD == nil || *keys[0].Key.QuotaNanoUSD != 12_000_000 ||
		keys[0].Key.UsedNanoUSD != 3_000_000 || keys[0].Key.ReservedNanoUSD != 1_000_000 || keys[0].Key.Revealable {
		t.Fatalf("migrated key = %+v digest=%x", keys[0].Key, keys[0].KeyDigest)
	}
	profileID := keys[0].Key.RoutingProfileID
	if _, err := storage.LoadResolverRoute(context.Background(), profileID, "gpt-4o-mini"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("allowlist unexpectedly exposes gpt-4o-mini: %v", err)
	}
	chatRoute, err := storage.LoadResolverRoute(context.Background(), profileID, "gpt-4o")
	if err != nil || len(chatRoute.Targets) != 2 || chatRoute.Targets[0].Surface != "openai.chat_completions" ||
		chatRoute.Targets[1].Surface != "openai.responses" {
		t.Fatalf("gpt-4o route = %+v, %v", chatRoute, err)
	}
	legacyRoute, err := storage.LoadResolverRoute(context.Background(), profileID, "gpt-4.1-mini")
	if err != nil || len(legacyRoute.Targets) != 2 {
		t.Fatalf("legacy route = %+v, %v", legacyRoute, err)
	}
	var monitorCount, balanceCount, usageCount int
	var monitorIntervalMS int64
	if err := storage.DB.QueryRow(`SELECT COUNT(*) FROM model_monitor_settings WHERE enabled=1`).Scan(&monitorCount); err != nil {
		t.Fatal(err)
	}
	if err := storage.DB.QueryRow(`SELECT interval_ms FROM model_monitor_settings WHERE enabled=1`).Scan(&monitorIntervalMS); err != nil {
		t.Fatal(err)
	}
	if err := storage.DB.QueryRow(`SELECT COUNT(*) FROM site_balance_snapshots`).Scan(&balanceCount); err != nil {
		t.Fatal(err)
	}
	if err := storage.DB.QueryRow(`SELECT COUNT(*) FROM site_usage_records`).Scan(&usageCount); err != nil {
		t.Fatal(err)
	}
	if monitorCount != 1 || monitorIntervalMS != 300_000 || balanceCount != 1 || usageCount != 1 {
		t.Fatalf("monitor count/interval and balance/usage counts = %d/%d/%d/%d",
			monitorCount, monitorIntervalMS, balanceCount, usageCount)
	}
}

func TestMigrateSQLiteFileSupportsLegacyUpstreamAccounts(t *testing.T) {
	t.Run("legacy account is used as the fallback", func(t *testing.T) {
		key := bytes.Repeat([]byte{0x35}, 32)
		source := newMigrationFixture(t, key)
		db := openFixtureForUpdate(t, source)
		mustExec(t, db, `DELETE FROM site_account_usage_records;
DELETE FROM site_account_snapshots;
DELETE FROM site_accounts;`)
		legacyAccountCipher := legacySeal(t, key, []byte(`{"access_token":"legacy-access","refresh_token":"legacy-refresh"}`))
		if _, err := db.Exec(`INSERT INTO upstream_accounts(
id,upstream_id,adapter_kind,api_origin,auth_cipher,enabled,capabilities_json,sync_state,
last_attempt_at,last_success_at,last_error_code,last_error_message,created_at,updated_at)
VALUES (801,31,'new_api','https://relay.example',?,1,'{}','healthy',181,180,NULL,NULL,100,200)`, legacyAccountCipher); err != nil {
			db.Close()
			t.Fatal(err)
		}
		mustExec(t, db, `INSERT INTO upstream_account_snapshots VALUES(
901,801,'{"account_id":"legacy-319","username":"legacy-owner","currency":"USD","balance":"6.50"}',175);
INSERT INTO upstream_account_usage_records VALUES(
902,801,'legacy-only','legacy-remote','gpt-4o','0.02','USD',
'{"request_id":"legacy-req","model":"gpt-4o","status":"success","prompt_tokens":4,"completion_tokens":2,"total_tokens":6,"duration_ms":90}',174,180);`)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}

		destination := filepath.Join(t.TempDir(), "vnext.sqlite")
		result, err := MigrateSQLiteFile(context.Background(), source, destination, MigrationOptions{
			MasterKey: key, OpenAISurface: OpenAISurfaceChat, NowMS: 1_800_000_000_000,
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.SiteAccountConnections != 1 || result.BalanceSnapshots != 1 || result.SiteUsageRecords != 1 {
			t.Fatalf("legacy account migration result = %+v", result)
		}

		storage, err := vnextstore.Open(context.Background(), destination)
		if err != nil {
			t.Fatal(err)
		}
		defer storage.Close()
		box, err := vnextsecretbox.New(key)
		if err != nil {
			t.Fatal(err)
		}
		var connectionID, siteID int64
		var adapterKind string
		var ciphertext []byte
		if err := storage.DB.QueryRow(`SELECT id,site_id,adapter_kind,secrets_cipher FROM site_account_connections`).
			Scan(&connectionID, &siteID, &adapterKind, &ciphertext); err != nil {
			t.Fatal(err)
		}
		plaintext, err := box.Open(vnextsecretbox.PurposeSiteAdministration,
			vnextsecretbox.Identity{RecordID: connectionID, OwnerID: siteID}, ciphertext)
		if err != nil {
			t.Fatal(err)
		}
		var secret map[string]any
		if err := json.Unmarshal(plaintext, &secret); err != nil {
			t.Fatal(err)
		}
		if adapterKind != "new_api" || secret["accessToken"] != "legacy-access" || secret["refreshToken"] != "legacy-refresh" {
			t.Fatalf("legacy account connection = adapter %q secret %#v", adapterKind, secret)
		}
		var balance, dedupKey string
		if err := storage.DB.QueryRow(`SELECT available_value FROM site_balance_snapshots`).Scan(&balance); err != nil {
			t.Fatal(err)
		}
		if err := storage.DB.QueryRow(`SELECT dedup_key FROM site_usage_records`).Scan(&dedupKey); err != nil {
			t.Fatal(err)
		}
		if balance != "6.50" || dedupKey != "legacy-only" {
			t.Fatalf("legacy balance/usage = %q/%q", balance, dedupKey)
		}
	})

	t.Run("newer site account wins and legacy history is merged", func(t *testing.T) {
		key := bytes.Repeat([]byte{0x36}, 32)
		source := newMigrationFixture(t, key)
		db := openFixtureForUpdate(t, source)
		if _, err := db.Exec(`INSERT INTO upstream_accounts(
id,upstream_id,adapter_kind,api_origin,auth_cipher,enabled,capabilities_json,sync_state,
last_attempt_at,last_success_at,last_error_code,last_error_message,created_at,updated_at)
VALUES (811,31,'new_api','https://legacy-account.example',?,1,'{}','healthy',181,180,NULL,NULL,100,200)`, []byte{1, 2, 3}); err != nil {
			db.Close()
			t.Fatal(err)
		}
		mustExec(t, db, `INSERT INTO upstream_account_snapshots VALUES(
911,811,'{"account_id":"legacy-319","username":"legacy-owner","currency":"USD","balance":"7.00"}',176);
INSERT INTO upstream_account_usage_records VALUES(
912,811,'usage-1','legacy-duplicate','gpt-4o','0.99','USD',
'{"request_id":"legacy-duplicate","model":"gpt-4o","status":"success","prompt_tokens":1,"completion_tokens":1,"total_tokens":2}',176,181);
INSERT INTO upstream_account_usage_records VALUES(
913,811,'legacy-unique','legacy-unique','gpt-4o','0.03','USD',
'{"request_id":"legacy-unique","model":"gpt-4o","status":"success","prompt_tokens":2,"completion_tokens":3,"total_tokens":5}',177,181);`)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}

		destination := filepath.Join(t.TempDir(), "vnext.sqlite")
		result, err := MigrateSQLiteFile(context.Background(), source, destination, MigrationOptions{
			MasterKey: key, OpenAISurface: OpenAISurfaceChat, NowMS: 1_800_000_000_000,
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.SiteAccountConnections != 1 || result.BalanceSnapshots != 2 || result.SiteUsageRecords != 2 {
			t.Fatalf("merged account migration result = %+v", result)
		}
		warningFound := false
		for _, warning := range result.Warnings {
			if strings.Contains(warning, "older credentials were not imported") {
				warningFound = true
				break
			}
		}
		if !warningFound {
			t.Fatalf("missing duplicate account warning: %+v", result.Warnings)
		}

		storage, err := vnextstore.Open(context.Background(), destination)
		if err != nil {
			t.Fatal(err)
		}
		defer storage.Close()
		box, err := vnextsecretbox.New(key)
		if err != nil {
			t.Fatal(err)
		}
		var connectionID, siteID int64
		var adapterKind string
		var ciphertext []byte
		if err := storage.DB.QueryRow(`SELECT id,site_id,adapter_kind,secrets_cipher FROM site_account_connections`).
			Scan(&connectionID, &siteID, &adapterKind, &ciphertext); err != nil {
			t.Fatal(err)
		}
		plaintext, err := box.Open(vnextsecretbox.PurposeSiteAdministration,
			vnextsecretbox.Identity{RecordID: connectionID, OwnerID: siteID}, ciphertext)
		if err != nil {
			t.Fatal(err)
		}
		var secret map[string]any
		if err := json.Unmarshal(plaintext, &secret); err != nil {
			t.Fatal(err)
		}
		if adapterKind != "ciii" || secret["accessToken"] != "access-token" {
			t.Fatalf("preferred site account = adapter %q secret %#v", adapterKind, secret)
		}
		var duplicateCount, uniqueCount int
		if err := storage.DB.QueryRow(`SELECT COUNT(*) FROM site_usage_records WHERE dedup_key='usage-1'`).Scan(&duplicateCount); err != nil {
			t.Fatal(err)
		}
		if err := storage.DB.QueryRow(`SELECT COUNT(*) FROM site_usage_records WHERE dedup_key='legacy-unique'`).Scan(&uniqueCount); err != nil {
			t.Fatal(err)
		}
		if duplicateCount != 1 || uniqueCount != 1 {
			t.Fatalf("merged usage dedup counts = %d/%d", duplicateCount, uniqueCount)
		}
	})
}

func TestMigrateSQLiteFileRefusesNonEmptyDestinationBeforeReadingSecrets(t *testing.T) {
	key := bytes.Repeat([]byte{0x32}, 32)
	source := newMigrationFixture(t, key)
	before := fileDigest(t, source)
	destination := filepath.Join(t.TempDir(), "occupied.sqlite")
	marker := []byte("do-not-overwrite")
	if err := os.WriteFile(destination, marker, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := MigrateSQLiteFile(context.Background(), source, destination, MigrationOptions{
		MasterKey: key, OpenAISurface: OpenAISurfaceChat,
	})
	if err == nil || !strings.Contains(err.Error(), "must be empty") {
		t.Fatalf("migration error = %v", err)
	}
	if got, readErr := os.ReadFile(destination); readErr != nil || !bytes.Equal(got, marker) {
		t.Fatalf("destination changed: %q, %v", got, readErr)
	}
	if after := fileDigest(t, source); after != before {
		t.Fatal("source database changed")
	}
}

func TestMigrateSQLiteFileRepeatedRunLeavesInstalledDestinationUntouched(t *testing.T) {
	key := bytes.Repeat([]byte{0x37}, 32)
	source := newMigrationFixture(t, key)
	sourceBefore := fileDigest(t, source)
	destination := filepath.Join(t.TempDir(), "vnext.sqlite")
	options := MigrationOptions{
		MasterKey: key, OpenAISurface: OpenAISurfaceBoth, NowMS: 1_800_000_000_000,
	}
	if _, err := MigrateSQLiteFile(context.Background(), source, destination, options); err != nil {
		t.Fatal(err)
	}
	destinationBefore := fileDigest(t, destination)
	if _, err := MigrateSQLiteFile(context.Background(), source, destination, options); err == nil || !strings.Contains(err.Error(), "must be empty") {
		t.Fatalf("repeated migration error = %v", err)
	}
	if sourceAfter := fileDigest(t, source); sourceAfter != sourceBefore {
		t.Fatalf("source changed after repeated migration: before %x after %x", sourceBefore, sourceAfter)
	}
	if destinationAfter := fileDigest(t, destination); destinationAfter != destinationBefore {
		t.Fatalf("installed destination changed after repeated migration: before %x after %x", destinationBefore, destinationAfter)
	}
	stages, err := filepath.Glob(filepath.Join(filepath.Dir(destination), "."+filepath.Base(destination)+".migration-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(stages) != 0 {
		t.Fatalf("repeated migration left staging files: %v", stages)
	}
}

func TestMigrateSQLiteFileRequiresExplicitSurfacePolicyAndCorrectMasterKey(t *testing.T) {
	key := bytes.Repeat([]byte{0x33}, 32)
	source := newMigrationFixture(t, key)
	destination := filepath.Join(t.TempDir(), "vnext.sqlite")
	if _, err := MigrateSQLiteFile(context.Background(), source, destination, MigrationOptions{MasterKey: key}); err == nil || !strings.Contains(err.Error(), "explicit OpenAI surface policy") {
		t.Fatalf("missing policy error = %v", err)
	}
	wrongKey := bytes.Repeat([]byte{0x99}, 32)
	if _, err := MigrateSQLiteFile(context.Background(), source, destination, MigrationOptions{
		MasterKey: wrongKey, OpenAISurface: OpenAISurfaceResponses,
	}); err == nil || !strings.Contains(err.Error(), "cannot be decrypted") {
		t.Fatalf("wrong master key error = %v", err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed migration installed a destination: %v", err)
	}
}

func TestMigrateSQLiteFileRequiresOfficialPriceMappingAndAcceptsOverride(t *testing.T) {
	key := bytes.Repeat([]byte{0x34}, 32)
	source := newMigrationFixture(t, key)
	db := openFixtureForUpdate(t, source)
	mustExec(t, db, `UPDATE published_models SET public_name='private-alias',official_price_sku=NULL WHERE id=11;
UPDATE downstream_keys SET allowed_models_json='["private-alias"]'`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "vnext.sqlite")
	_, err := MigrateSQLiteFile(context.Background(), source, destination, MigrationOptions{
		MasterKey: key, OpenAISurface: OpenAISurfaceChat,
	})
	if err == nil || !strings.Contains(err.Error(), "private-alias=private-alias") {
		t.Fatalf("missing price mapping error = %v", err)
	}
	result, err := MigrateSQLiteFile(context.Background(), source, destination, MigrationOptions{
		MasterKey: key, OpenAISurface: OpenAISurfaceChat,
		PriceSKUOverrides: map[string]string{"private-alias": "gpt-4o"},
	})
	if err != nil || result.PublishedModels != 3 {
		t.Fatalf("override migration = %+v, %v", result, err)
	}
	destinationDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(destination)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer destinationDB.Close()
	var sku string
	if err := destinationDB.QueryRow(`SELECT official_price_sku FROM published_models WHERE public_name='private-alias'`).Scan(&sku); err != nil || sku != "gpt-4o" {
		t.Fatalf("price SKU = %q, %v", sku, err)
	}
}

func newMigrationFixture(t *testing.T, key []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(1)&_pragma=journal_mode(DELETE)")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT,applied_at INTEGER);
INSERT INTO schema_migrations(version,name,applied_at) VALUES (8,'legacy-v8',0);
CREATE TABLE app_settings(
 id INTEGER PRIMARY KEY,default_cooldown_seconds INTEGER,failure_threshold INTEGER,failure_window_seconds INTEGER,
 probe_interval_seconds INTEGER,request_deadline_seconds INTEGER,max_attempts INTEGER,log_retention_days INTEGER,
 first_output_timeout_seconds INTEGER,stream_idle_timeout_seconds INTEGER,updated_at INTEGER
);
INSERT INTO app_settings VALUES(1,300,2,300,300,120,3,30,30,60,1000);
CREATE TABLE sites(id INTEGER PRIMARY KEY,name TEXT,dashboard_url TEXT,enabled INTEGER,revision INTEGER,created_at INTEGER,updated_at INTEGER);
CREATE TABLE inference_endpoints(
 id INTEGER PRIMARY KEY,site_id INTEGER,name TEXT,base_url TEXT,wire_protocol TEXT,compatibility_profile TEXT,auth_scheme TEXT,
 custom_headers_json TEXT,position INTEGER,enabled INTEGER,revision INTEGER,created_at INTEGER,updated_at INTEGER
);
CREATE TABLE inference_credentials(
 id INTEGER PRIMARY KEY,site_id INTEGER,name TEXT,secret_cipher BLOB,position INTEGER,enabled INTEGER,runtime_state TEXT,
 cooldown_until INTEGER,revision INTEGER,created_at INTEGER,updated_at INTEGER
);
CREATE TABLE site_models(
 id INTEGER PRIMARY KEY,site_id INTEGER,endpoint_id INTEGER,model_name TEXT,display_name TEXT,enabled INTEGER,last_seen_at INTEGER,
 revision INTEGER,created_at INTEGER,updated_at INTEGER
);
CREATE TABLE credential_model_access(
 site_id INTEGER,credential_id INTEGER,site_model_id INTEGER,availability TEXT,last_checked_at INTEGER,revision INTEGER,updated_at INTEGER
);
CREATE TABLE published_models(
 id INTEGER PRIMARY KEY,public_name TEXT,official_price_sku TEXT,enabled INTEGER,monitor_enabled INTEGER,
 monitor_interval_seconds INTEGER,revision INTEGER,created_at INTEGER,updated_at INTEGER
);
CREATE TABLE route_site_targets(
 id INTEGER PRIMARY KEY,published_model_id INTEGER,site_id INTEGER,endpoint_id INTEGER,site_model_id INTEGER,
 position INTEGER,enabled INTEGER,revision INTEGER,created_at INTEGER,updated_at INTEGER
);
CREATE TABLE routing_profiles(id INTEGER PRIMARY KEY,name TEXT,revision INTEGER,created_at INTEGER,updated_at INTEGER);
CREATE TABLE routing_profile_model_targets(
 routing_profile_id INTEGER,published_model_id INTEGER,route_site_target_id INTEGER,position INTEGER,created_at INTEGER,updated_at INTEGER
);
CREATE TABLE downstream_keys(
 id INTEGER PRIMARY KEY,name TEXT,key_prefix TEXT,key_hash BLOB,enabled INTEGER,quota_micro_usd INTEGER,rpm_limit INTEGER,
 used_micro_usd INTEGER,reserved_micro_usd INTEGER,allowed_models_json TEXT,routing_profile_id INTEGER,expires_at INTEGER,
 last_used_at INTEGER,created_at INTEGER,updated_at INTEGER
);
CREATE TABLE site_accounts(
 id INTEGER PRIMARY KEY,site_id INTEGER,adapter_kind TEXT,api_origin TEXT,auth_cipher BLOB,enabled INTEGER,
 last_attempt_at INTEGER,last_success_at INTEGER,last_error_code TEXT,created_at INTEGER,updated_at INTEGER
);
CREATE TABLE site_account_snapshots(id INTEGER PRIMARY KEY,site_account_id INTEGER,snapshot_json TEXT,captured_at INTEGER);
CREATE TABLE site_account_usage_records(
 id INTEGER PRIMARY KEY,site_account_id INTEGER,dedupe_key TEXT,external_id TEXT,model_name TEXT,amount_text TEXT,
 unit TEXT,raw_json TEXT,occurred_at INTEGER,synced_at INTEGER
);
CREATE TABLE upstreams(id INTEGER PRIMARY KEY,name TEXT,kind TEXT,dashboard_url TEXT,enabled INTEGER,custom_headers_json TEXT,created_at INTEGER,updated_at INTEGER);
CREATE TABLE upstream_accounts(
 id INTEGER PRIMARY KEY,upstream_id INTEGER UNIQUE,adapter_kind TEXT,api_origin TEXT,auth_cipher BLOB,enabled INTEGER,
 capabilities_json TEXT,sync_state TEXT,last_attempt_at INTEGER,last_success_at INTEGER,last_error_code TEXT,
 last_error_message TEXT,created_at INTEGER,updated_at INTEGER
);
CREATE TABLE upstream_account_snapshots(
 id INTEGER PRIMARY KEY,upstream_account_id INTEGER,snapshot_json TEXT,captured_at INTEGER
);
CREATE TABLE upstream_account_usage_records(
 id INTEGER PRIMARY KEY,upstream_account_id INTEGER,dedupe_key TEXT,external_id TEXT,model_name TEXT,amount_text TEXT,
 unit TEXT,raw_json TEXT,occurred_at INTEGER,synced_at INTEGER,UNIQUE(upstream_account_id,dedupe_key)
);
CREATE TABLE upstream_endpoints(id INTEGER PRIMARY KEY,upstream_id INTEGER,name TEXT,base_url TEXT,position INTEGER,enabled INTEGER,created_at INTEGER,updated_at INTEGER);
CREATE TABLE upstream_credentials(id INTEGER PRIMARY KEY,upstream_id INTEGER,name TEXT,secret_cipher BLOB,enabled INTEGER,runtime_state TEXT,created_at INTEGER,updated_at INTEGER);
CREATE TABLE upstream_models(id INTEGER PRIMARY KEY,upstream_id INTEGER,model_name TEXT,enabled INTEGER,last_seen_at INTEGER,created_at INTEGER,updated_at INTEGER);
CREATE TABLE routes(id INTEGER PRIMARY KEY,public_model TEXT,enabled INTEGER,revision INTEGER,created_at INTEGER,updated_at INTEGER);
CREATE TABLE route_targets(id INTEGER PRIMARY KEY,route_id INTEGER,upstream_model_id INTEGER,endpoint_id INTEGER,credential_id INTEGER,upstream_model_override TEXT,position INTEGER,enabled INTEGER,created_at INTEGER,updated_at INTEGER);
CREATE TABLE legacy_upstream_site_mappings(upstream_id INTEGER PRIMARY KEY,site_id INTEGER,endpoint_id INTEGER,credential_id INTEGER);`)

	credentialCipher := legacySeal(t, key, []byte("sk-upstream-secret"))
	legacy, err := newLegacyCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	if plaintext, err := legacy.open(credentialCipher); err != nil || string(plaintext) != "sk-upstream-secret" {
		t.Fatalf("fixture cipher round trip = %q, %v", plaintext, err)
	}
	accountCipher := legacySeal(t, key, []byte(`{"version":1,"kind":"access_refresh","credentials":{"access_token":"access-token","refresh_token":"refresh-token","expires_at":"1800000000000"}}`))
	digest := sha256.Sum256([]byte("sk-old-plaintext"))
	if _, err := db.Exec(`INSERT INTO sites VALUES(1,'Relay','https://relay.example',1,3,100,200);
INSERT INTO inference_endpoints VALUES(101,1,'Primary','https://relay.example/v1','compatible','generic','bearer','{"X-Trace":"migrated","Authorization":"legacy-header-secret"}',0,1,2,100,200);
INSERT INTO site_models VALUES
 (1101,1,101,'gpt-4o','GPT 4o',1,150,2,100,200),
 (1201,1,101,'gpt-4o-mini','GPT 4o Mini',1,150,1,100,200);
INSERT INTO credential_model_access VALUES
 (1,1001,1101,'supported',160,1,160),(1,1001,1201,'supported',160,1,160);
INSERT INTO published_models VALUES
 (11,'gpt-4o',NULL,1,1,60,3,100,200),(12,'gpt-4o-mini','gpt-4o-mini',1,0,300,1,100,200);
INSERT INTO route_site_targets VALUES
 (111,11,1,101,1101,0,1,2,100,200),(121,12,1,101,1201,0,1,1,100,200);
INSERT INTO routing_profiles VALUES(7,'Priority route',2,100,200);
INSERT INTO routing_profile_model_targets VALUES(7,11,111,0,100,200);
INSERT INTO site_account_snapshots VALUES(601,501,'{"version":1,"account":{"account_id":"319","username":"owner","currency":"USD","balance":"8.25","used":"1.75"}}',170);
INSERT INTO site_account_usage_records VALUES(701,501,'usage-1','remote-1','gpt-4o','0.25','USD','{"request_id":"req-1","upstream_request_id":"up-1","prompt_tokens":10,"completion_tokens":20,"reasoning_tokens":3,"total_tokens":33,"status_code":200,"duration_ms":250,"actual_cost":"0.25","currency":"USD","api_key_name":"Primary key"}',165,175);
INSERT INTO upstreams VALUES(31,'Relay legacy','openai','https://relay.example',1,'{}',100,200);
INSERT INTO upstream_endpoints VALUES(3201,31,'Legacy API','https://relay.example/v1',0,1,100,200);
INSERT INTO upstream_models VALUES(3101,31,'gpt-4.1-mini',1,150,100,200);
INSERT INTO routes VALUES(21,'gpt-4.1-mini',1,2,100,200);
INSERT INTO route_targets VALUES(2101,21,3101,3201,3301,NULL,0,1,100,200);
	INSERT INTO legacy_upstream_site_mappings VALUES(31,1,101,1001)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO inference_credentials VALUES(1001,1,'Primary key',?,0,1,'active',NULL,2,100,200)`, credentialCipher); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO downstream_keys VALUES(1,'Primary','sk-old',?,1,12000,60,3000,1000,'["gpt-4o","gpt-4.1-mini"]',7,NULL,190,100,200)`, digest[:]); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO site_accounts VALUES(501,1,'ciii','https://relay.example',?,1,180,170,NULL,100,200)`, accountCipher); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO upstream_credentials VALUES(3301,31,'Legacy key',?,1,'active',100,200)`, credentialCipher); err != nil {
		db.Close()
		t.Fatal(err)
	}
	var storedCipher []byte
	if err := db.QueryRow(`SELECT secret_cipher FROM inference_credentials WHERE id=1001`).Scan(&storedCipher); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if plaintext, err := legacy.open(storedCipher); err != nil || string(plaintext) != "sk-upstream-secret" {
		db.Close()
		t.Fatalf("stored fixture cipher round trip = %q, %v (stored=%x original=%x)", plaintext, err, storedCipher, credentialCipher)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func legacySeal(t *testing.T, key, plaintext []byte) []byte {
	t.Helper()
	cipher, err := newLegacyCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, cipher.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	return cipher.aead.Seal(nonce, nonce, plaintext, nil)
}

func fileDigest(t *testing.T, path string) [sha256.Size]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(data)
}

func openFixtureForUpdate(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=journal_mode(DELETE)")
	if err != nil {
		t.Fatal(err)
	}
	return db
}
