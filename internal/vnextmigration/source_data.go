package vnextmigration

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type migrationSource struct {
	sites                  []migrationSite
	endpoints              []migrationEndpoint
	credentials            []migrationCredential
	siteModels             []migrationSiteModel
	credentialAccess       []migrationCredentialAccess
	publishedModels        []migrationPublishedModel
	routeTargets           []migrationRouteTarget
	profiles               []migrationProfile
	profileTargets         []migrationProfileTarget
	downstreamKeys         []migrationDownstreamKey
	accounts               []migrationAccount
	accountSnapshots       []migrationAccountSnapshot
	accountUsage           []migrationAccountUsage
	legacyAccounts         []migrationLegacyAccount
	legacyAccountSnapshots []migrationAccountSnapshot
	legacyAccountUsage     []migrationAccountUsage
	legacyUpstreams        []migrationLegacyUpstream
	legacyEndpoints        []migrationLegacyEndpoint
	legacyCredentials      []migrationLegacyCredential
	legacyModels           []migrationLegacyModel
	legacyRoutes           []migrationLegacyRoute
	legacyRouteTargets     []migrationLegacyRouteTarget
	legacySiteMappings     map[int64]migrationLegacySiteMapping
	runtimeSettings        *migrationRuntimeSettings
}

type migrationSite struct {
	id           int64
	name         string
	dashboardURL string
	enabled      bool
	revision     int64
	createdAt    int64
	updatedAt    int64
}

type migrationEndpoint struct {
	id                   int64
	siteID               int64
	name                 string
	baseURL              string
	protocol             string
	compatibilityProfile string
	authScheme           string
	customHeaders        string
	position             int
	enabled              bool
	revision             int64
	createdAt            int64
	updatedAt            int64
}

type migrationCredential struct {
	id            int64
	siteID        int64
	name          string
	secretCipher  []byte
	position      int
	enabled       bool
	runtimeState  string
	cooldownUntil *int64
	revision      int64
	createdAt     int64
	updatedAt     int64
}

type migrationSiteModel struct {
	id          int64
	siteID      int64
	endpointID  int64
	name        string
	displayName string
	enabled     bool
	lastSeenAt  *int64
	revision    int64
	createdAt   int64
	updatedAt   int64
}

type migrationCredentialAccess struct {
	siteID       int64
	credentialID int64
	siteModelID  int64
	availability string
	lastChecked  *int64
	revision     int64
	updatedAt    int64
}

type migrationPublishedModel struct {
	id                    int64
	name                  string
	priceSKU              string
	enabled               bool
	monitorEnabled        bool
	monitorIntervalSecond int64
	revision              int64
	createdAt             int64
	updatedAt             int64
}

type migrationRouteTarget struct {
	id               int64
	publishedModelID int64
	siteID           int64
	endpointID       int64
	siteModelID      int64
	position         int
	enabled          bool
	revision         int64
	createdAt        int64
	updatedAt        int64
}

type migrationProfile struct {
	id        int64
	name      string
	revision  int64
	createdAt int64
	updatedAt int64
}

type migrationProfileTarget struct {
	profileID        int64
	publishedModelID int64
	routeTargetID    int64
	position         int
	createdAt        int64
	updatedAt        int64
}

type migrationDownstreamKey struct {
	id               int64
	name             string
	prefix           string
	digest           []byte
	enabled          bool
	quotaMicroUSD    *int64
	usedMicroUSD     int64
	reservedMicroUSD int64
	// deprecatedRPMLimit is read only because supported legacy schemas require
	// the column. VNext migration deliberately discards its value.
	deprecatedRPMLimit int
	allowedModelsRaw   string
	profileID          *int64
	expiresAt          *int64
	lastUsedAt         *int64
	createdAt          int64
	updatedAt          int64
}

type migrationAccount struct {
	id            int64
	siteID        int64
	adapterKind   string
	origin        string
	authCipher    []byte
	enabled       bool
	lastAttemptAt *int64
	lastSuccessAt *int64
	lastErrorCode string
	createdAt     int64
	updatedAt     int64
}

type migrationLegacyAccount struct {
	id            int64
	upstreamID    int64
	adapterKind   string
	origin        string
	authCipher    []byte
	enabled       bool
	lastAttemptAt *int64
	lastSuccessAt *int64
	lastErrorCode string
	createdAt     int64
	updatedAt     int64
}

type migrationAccountSnapshot struct {
	id         int64
	accountID  int64
	json       string
	capturedAt int64
}

type migrationAccountUsage struct {
	id         int64
	accountID  int64
	dedupKey   string
	externalID string
	modelName  string
	amount     string
	unit       string
	rawJSON    string
	occurredAt *int64
	syncedAt   int64
}

type migrationLegacyUpstream struct {
	id            int64
	name          string
	protocol      string
	dashboardURL  string
	enabled       bool
	customHeaders string
	createdAt     int64
	updatedAt     int64
}

type migrationLegacyEndpoint struct {
	id         int64
	upstreamID int64
	name       string
	baseURL    string
	position   int
	enabled    bool
	createdAt  int64
	updatedAt  int64
}

type migrationLegacyCredential struct {
	id           int64
	upstreamID   int64
	name         string
	secretCipher []byte
	enabled      bool
	runtimeState string
	createdAt    int64
	updatedAt    int64
}

type migrationLegacyModel struct {
	id         int64
	upstreamID int64
	name       string
	enabled    bool
	lastSeenAt *int64
	createdAt  int64
	updatedAt  int64
}

type migrationLegacyRoute struct {
	id        int64
	name      string
	enabled   bool
	revision  int64
	createdAt int64
	updatedAt int64
}

type migrationLegacyRouteTarget struct {
	id                  int64
	routeID             int64
	modelID             int64
	endpointID          int64
	credentialID        int64
	sourceModelOverride string
	position            int
	enabled             bool
	createdAt           int64
	updatedAt           int64
}

type migrationLegacySiteMapping struct {
	upstreamID   int64
	siteID       int64
	endpointID   *int64
	credentialID *int64
}

type migrationRuntimeSettings struct {
	cooldownSeconds          int64
	failureThreshold         int
	failureWindowSeconds     int64
	probeIntervalSeconds     int64
	firstOutputTimeoutSecond int64
	streamIdleTimeoutSecond  int64
	requestDeadlineSeconds   int64
	maxAttempts              int
	logRetentionDays         int
	updatedAt                int64
}

type canonicalMigrationModel struct {
	name     string
	priceSKU string
}

func (source migrationSource) canonicalModels() []canonicalMigrationModel {
	models := make(map[string]canonicalMigrationModel, len(source.publishedModels)+len(source.legacyRoutes))
	for _, model := range source.publishedModels {
		models[model.name] = canonicalMigrationModel{name: model.name, priceSKU: model.priceSKU}
	}
	for _, route := range source.legacyRoutes {
		if _, exists := models[route.name]; !exists {
			models[route.name] = canonicalMigrationModel{name: route.name}
		}
	}
	result := make([]canonicalMigrationModel, 0, len(models))
	for _, model := range models {
		result = append(result, model)
	}
	sortCanonicalModels(result)
	return result
}

func sortCanonicalModels(models []canonicalMigrationModel) {
	for i := 1; i < len(models); i++ {
		for j := i; j > 0; j-- {
			left := strings.ToLower(models[j-1].name)
			right := strings.ToLower(models[j].name)
			if left < right || (left == right && models[j-1].name <= models[j].name) {
				break
			}
			models[j-1], models[j] = models[j], models[j-1]
		}
	}
}

func loadMigrationSource(ctx context.Context, tx *sql.Tx, schema schemaInfo) (migrationSource, error) {
	source := migrationSource{legacySiteMappings: make(map[int64]migrationLegacySiteMapping)}
	loaders := []func(context.Context, *sql.Tx, schemaInfo, *migrationSource) error{
		loadMigrationSites,
		loadMigrationEndpoints,
		loadMigrationCredentials,
		loadMigrationSiteModels,
		loadMigrationCredentialAccess,
		loadMigrationPublishedModels,
		loadMigrationRouteTargets,
		loadMigrationProfiles,
		loadMigrationDownstreamKeys,
		loadMigrationAccounts,
		loadMigrationLegacyDomain,
		loadMigrationRuntimeSettings,
	}
	for _, loader := range loaders {
		if err := loader(ctx, tx, schema, &source); err != nil {
			return migrationSource{}, err
		}
	}
	return source, nil
}

func loadMigrationSites(ctx context.Context, tx *sql.Tx, schema schemaInfo, source *migrationSource) error {
	if !schema.hasTable("sites") {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,name,COALESCE(dashboard_url,''),enabled,revision,created_at,updated_at FROM sites ORDER BY id`)
	if err != nil {
		return fmt.Errorf("load migration sites: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item migrationSite
		var enabled int
		if err := rows.Scan(&item.id, &item.name, &item.dashboardURL, &enabled, &item.revision, &item.createdAt, &item.updatedAt); err != nil {
			return fmt.Errorf("scan migration site: %w", err)
		}
		item.enabled = enabled == 1
		source.sites = append(source.sites, item)
	}
	return rows.Err()
}

func loadMigrationEndpoints(ctx context.Context, tx *sql.Tx, schema schemaInfo, source *migrationSource) error {
	if !schema.hasTable("inference_endpoints") {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,site_id,name,base_url,wire_protocol,compatibility_profile,auth_scheme,
custom_headers_json,position,enabled,revision,created_at,updated_at FROM inference_endpoints ORDER BY site_id,position,id`)
	if err != nil {
		return fmt.Errorf("load migration endpoints: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item migrationEndpoint
		var enabled int
		if err := rows.Scan(&item.id, &item.siteID, &item.name, &item.baseURL, &item.protocol,
			&item.compatibilityProfile, &item.authScheme, &item.customHeaders, &item.position, &enabled,
			&item.revision, &item.createdAt, &item.updatedAt); err != nil {
			return fmt.Errorf("scan migration endpoint: %w", err)
		}
		item.enabled = enabled == 1
		source.endpoints = append(source.endpoints, item)
	}
	return rows.Err()
}

func loadMigrationCredentials(ctx context.Context, tx *sql.Tx, schema schemaInfo, source *migrationSource) error {
	if !schema.hasTable("inference_credentials") {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,site_id,name,secret_cipher,position,enabled,runtime_state,cooldown_until,
revision,created_at,updated_at FROM inference_credentials ORDER BY site_id,position,id`)
	if err != nil {
		return fmt.Errorf("load migration credentials: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item migrationCredential
		var enabled int
		var cooldown sql.NullInt64
		if err := rows.Scan(&item.id, &item.siteID, &item.name, &item.secretCipher, &item.position, &enabled,
			&item.runtimeState, &cooldown, &item.revision, &item.createdAt, &item.updatedAt); err != nil {
			return fmt.Errorf("scan migration credential: %w", err)
		}
		item.enabled = enabled == 1
		item.cooldownUntil = nullInt64Pointer(cooldown)
		item.secretCipher = append([]byte(nil), item.secretCipher...)
		source.credentials = append(source.credentials, item)
	}
	return rows.Err()
}

func loadMigrationSiteModels(ctx context.Context, tx *sql.Tx, schema schemaInfo, source *migrationSource) error {
	if !schema.hasTable("site_models") {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,site_id,endpoint_id,model_name,COALESCE(display_name,''),enabled,
last_seen_at,revision,created_at,updated_at FROM site_models ORDER BY site_id,endpoint_id,model_name,id`)
	if err != nil {
		return fmt.Errorf("load migration site models: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item migrationSiteModel
		var enabled int
		var lastSeen sql.NullInt64
		if err := rows.Scan(&item.id, &item.siteID, &item.endpointID, &item.name, &item.displayName,
			&enabled, &lastSeen, &item.revision, &item.createdAt, &item.updatedAt); err != nil {
			return fmt.Errorf("scan migration site model: %w", err)
		}
		item.enabled = enabled == 1
		item.lastSeenAt = nullInt64Pointer(lastSeen)
		source.siteModels = append(source.siteModels, item)
	}
	return rows.Err()
}

func loadMigrationCredentialAccess(ctx context.Context, tx *sql.Tx, schema schemaInfo, source *migrationSource) error {
	if !schema.hasTable("credential_model_access") {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT site_id,credential_id,site_model_id,availability,last_checked_at,revision,updated_at
FROM credential_model_access ORDER BY credential_id,site_model_id`)
	if err != nil {
		return fmt.Errorf("load migration credential access: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item migrationCredentialAccess
		var lastChecked sql.NullInt64
		if err := rows.Scan(&item.siteID, &item.credentialID, &item.siteModelID, &item.availability,
			&lastChecked, &item.revision, &item.updatedAt); err != nil {
			return fmt.Errorf("scan migration credential access: %w", err)
		}
		item.lastChecked = nullInt64Pointer(lastChecked)
		source.credentialAccess = append(source.credentialAccess, item)
	}
	return rows.Err()
}

func loadMigrationPublishedModels(ctx context.Context, tx *sql.Tx, schema schemaInfo, source *migrationSource) error {
	if !schema.hasTable("published_models") {
		return nil
	}
	monitorInterval := "300"
	if schema.hasColumn("published_models", "monitor_interval_seconds") {
		monitorInterval = "monitor_interval_seconds"
	}
	monitorEnabled := "0"
	if schema.hasColumn("published_models", "monitor_enabled") {
		monitorEnabled = "monitor_enabled"
	}
	query := fmt.Sprintf(`SELECT id,public_name,COALESCE(official_price_sku,''),enabled,%s,%s,revision,created_at,updated_at
FROM published_models ORDER BY id`, monitorEnabled, monitorInterval)
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("load migration published models: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item migrationPublishedModel
		var enabled, monitor int
		if err := rows.Scan(&item.id, &item.name, &item.priceSKU, &enabled, &monitor,
			&item.monitorIntervalSecond, &item.revision, &item.createdAt, &item.updatedAt); err != nil {
			return fmt.Errorf("scan migration published model: %w", err)
		}
		item.enabled = enabled == 1
		item.monitorEnabled = monitor == 1
		source.publishedModels = append(source.publishedModels, item)
	}
	return rows.Err()
}

func loadMigrationRouteTargets(ctx context.Context, tx *sql.Tx, schema schemaInfo, source *migrationSource) error {
	if !schema.hasTable("route_site_targets") {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,published_model_id,site_id,endpoint_id,site_model_id,position,enabled,
revision,created_at,updated_at FROM route_site_targets ORDER BY published_model_id,position,id`)
	if err != nil {
		return fmt.Errorf("load migration route targets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item migrationRouteTarget
		var enabled int
		if err := rows.Scan(&item.id, &item.publishedModelID, &item.siteID, &item.endpointID, &item.siteModelID,
			&item.position, &enabled, &item.revision, &item.createdAt, &item.updatedAt); err != nil {
			return fmt.Errorf("scan migration route target: %w", err)
		}
		item.enabled = enabled == 1
		source.routeTargets = append(source.routeTargets, item)
	}
	return rows.Err()
}

func loadMigrationProfiles(ctx context.Context, tx *sql.Tx, schema schemaInfo, source *migrationSource) error {
	if schema.hasTable("routing_profiles") {
		rows, err := tx.QueryContext(ctx, `SELECT id,name,revision,created_at,updated_at FROM routing_profiles ORDER BY id`)
		if err != nil {
			return fmt.Errorf("load migration routing profiles: %w", err)
		}
		for rows.Next() {
			var item migrationProfile
			if err := rows.Scan(&item.id, &item.name, &item.revision, &item.createdAt, &item.updatedAt); err != nil {
				rows.Close()
				return fmt.Errorf("scan migration routing profile: %w", err)
			}
			source.profiles = append(source.profiles, item)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	if !schema.hasTable("routing_profile_model_targets") {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT routing_profile_id,published_model_id,route_site_target_id,position,created_at,updated_at
FROM routing_profile_model_targets ORDER BY routing_profile_id,published_model_id,position,route_site_target_id`)
	if err != nil {
		return fmt.Errorf("load migration routing profile targets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item migrationProfileTarget
		if err := rows.Scan(&item.profileID, &item.publishedModelID, &item.routeTargetID, &item.position,
			&item.createdAt, &item.updatedAt); err != nil {
			return fmt.Errorf("scan migration routing profile target: %w", err)
		}
		source.profileTargets = append(source.profileTargets, item)
	}
	return rows.Err()
}

func loadMigrationDownstreamKeys(ctx context.Context, tx *sql.Tx, schema schemaInfo, source *migrationSource) error {
	required := []string{"id", "name", "key_prefix", "key_hash", "enabled", "quota_micro_usd", "rpm_limit",
		"used_micro_usd", "reserved_micro_usd", "created_at", "updated_at"}
	for _, column := range required {
		if !schema.hasColumn("downstream_keys", column) {
			return fmt.Errorf("source downstream_keys is missing required column %s", column)
		}
	}
	allowed := "'[]'"
	if schema.hasColumn("downstream_keys", "allowed_models_json") {
		allowed = "COALESCE(allowed_models_json,'[]')"
	}
	profile := "NULL"
	if schema.hasColumn("downstream_keys", "routing_profile_id") {
		profile = "routing_profile_id"
	}
	expires := "NULL"
	if schema.hasColumn("downstream_keys", "expires_at") {
		expires = "expires_at"
	}
	lastUsed := "NULL"
	if schema.hasColumn("downstream_keys", "last_used_at") {
		lastUsed = "last_used_at"
	}
	query := fmt.Sprintf(`SELECT id,name,key_prefix,key_hash,enabled,quota_micro_usd,used_micro_usd,reserved_micro_usd,
rpm_limit,%s,%s,%s,%s,created_at,updated_at FROM downstream_keys ORDER BY id`, allowed, profile, expires, lastUsed)
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("load migration downstream keys: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item migrationDownstreamKey
		var enabled int
		var quota, profileID, expiresAt, lastUsedAt sql.NullInt64
		if err := rows.Scan(&item.id, &item.name, &item.prefix, &item.digest, &enabled, &quota,
			&item.usedMicroUSD, &item.reservedMicroUSD, &item.deprecatedRPMLimit, &item.allowedModelsRaw,
			&profileID, &expiresAt, &lastUsedAt, &item.createdAt, &item.updatedAt); err != nil {
			return fmt.Errorf("scan migration downstream key: %w", err)
		}
		item.enabled = enabled == 1
		item.digest = append([]byte(nil), item.digest...)
		item.quotaMicroUSD = nullInt64Pointer(quota)
		item.profileID = nullInt64Pointer(profileID)
		item.expiresAt = nullInt64Pointer(expiresAt)
		item.lastUsedAt = nullInt64Pointer(lastUsedAt)
		source.downstreamKeys = append(source.downstreamKeys, item)
	}
	return rows.Err()
}

func loadMigrationAccounts(ctx context.Context, tx *sql.Tx, schema schemaInfo, source *migrationSource) error {
	if schema.hasTable("site_accounts") {
		rows, err := tx.QueryContext(ctx, `SELECT id,site_id,adapter_kind,api_origin,auth_cipher,enabled,last_attempt_at,
last_success_at,COALESCE(last_error_code,''),created_at,updated_at FROM site_accounts ORDER BY id`)
		if err != nil {
			return fmt.Errorf("load migration site accounts: %w", err)
		}
		for rows.Next() {
			var item migrationAccount
			var enabled int
			var lastAttempt, lastSuccess sql.NullInt64
			if err := rows.Scan(&item.id, &item.siteID, &item.adapterKind, &item.origin, &item.authCipher, &enabled,
				&lastAttempt, &lastSuccess, &item.lastErrorCode, &item.createdAt, &item.updatedAt); err != nil {
				rows.Close()
				return fmt.Errorf("scan migration site account: %w", err)
			}
			item.enabled = enabled == 1
			item.authCipher = append([]byte(nil), item.authCipher...)
			item.lastAttemptAt = nullInt64Pointer(lastAttempt)
			item.lastSuccessAt = nullInt64Pointer(lastSuccess)
			source.accounts = append(source.accounts, item)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	if schema.hasTable("site_account_snapshots") {
		rows, err := tx.QueryContext(ctx, `SELECT id,site_account_id,snapshot_json,captured_at FROM site_account_snapshots ORDER BY id`)
		if err != nil {
			return fmt.Errorf("load migration account snapshots: %w", err)
		}
		for rows.Next() {
			var item migrationAccountSnapshot
			if err := rows.Scan(&item.id, &item.accountID, &item.json, &item.capturedAt); err != nil {
				rows.Close()
				return fmt.Errorf("scan migration account snapshot: %w", err)
			}
			source.accountSnapshots = append(source.accountSnapshots, item)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	if schema.hasTable("site_account_usage_records") {
		rows, err := tx.QueryContext(ctx, `SELECT id,site_account_id,dedupe_key,COALESCE(external_id,''),COALESCE(model_name,''),
COALESCE(amount_text,''),COALESCE(unit,''),raw_json,occurred_at,synced_at FROM site_account_usage_records ORDER BY id`)
		if err != nil {
			return fmt.Errorf("load migration account usage: %w", err)
		}
		for rows.Next() {
			var item migrationAccountUsage
			var occurred sql.NullInt64
			if err := rows.Scan(&item.id, &item.accountID, &item.dedupKey, &item.externalID, &item.modelName,
				&item.amount, &item.unit, &item.rawJSON, &occurred, &item.syncedAt); err != nil {
				rows.Close()
				return fmt.Errorf("scan migration account usage: %w", err)
			}
			item.occurredAt = nullInt64Pointer(occurred)
			source.accountUsage = append(source.accountUsage, item)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	if schema.hasTable("upstream_accounts") {
		rows, err := tx.QueryContext(ctx, `SELECT id,upstream_id,adapter_kind,api_origin,auth_cipher,enabled,last_attempt_at,
last_success_at,COALESCE(last_error_code,''),created_at,updated_at FROM upstream_accounts ORDER BY id`)
		if err != nil {
			return fmt.Errorf("load migration legacy accounts: %w", err)
		}
		for rows.Next() {
			var item migrationLegacyAccount
			var enabled int
			var lastAttempt, lastSuccess sql.NullInt64
			if err := rows.Scan(&item.id, &item.upstreamID, &item.adapterKind, &item.origin, &item.authCipher, &enabled,
				&lastAttempt, &lastSuccess, &item.lastErrorCode, &item.createdAt, &item.updatedAt); err != nil {
				rows.Close()
				return fmt.Errorf("scan migration legacy account: %w", err)
			}
			item.enabled = enabled == 1
			item.authCipher = append([]byte(nil), item.authCipher...)
			item.lastAttemptAt = nullInt64Pointer(lastAttempt)
			item.lastSuccessAt = nullInt64Pointer(lastSuccess)
			source.legacyAccounts = append(source.legacyAccounts, item)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	if schema.hasTable("upstream_account_snapshots") {
		rows, err := tx.QueryContext(ctx, `SELECT id,upstream_account_id,snapshot_json,captured_at
FROM upstream_account_snapshots ORDER BY id`)
		if err != nil {
			return fmt.Errorf("load migration legacy account snapshots: %w", err)
		}
		for rows.Next() {
			var item migrationAccountSnapshot
			if err := rows.Scan(&item.id, &item.accountID, &item.json, &item.capturedAt); err != nil {
				rows.Close()
				return fmt.Errorf("scan migration legacy account snapshot: %w", err)
			}
			source.legacyAccountSnapshots = append(source.legacyAccountSnapshots, item)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	if schema.hasTable("upstream_account_usage_records") {
		rows, err := tx.QueryContext(ctx, `SELECT id,upstream_account_id,dedupe_key,COALESCE(external_id,''),COALESCE(model_name,''),
COALESCE(amount_text,''),COALESCE(unit,''),raw_json,occurred_at,synced_at
FROM upstream_account_usage_records ORDER BY id`)
		if err != nil {
			return fmt.Errorf("load migration legacy account usage: %w", err)
		}
		for rows.Next() {
			var item migrationAccountUsage
			var occurred sql.NullInt64
			if err := rows.Scan(&item.id, &item.accountID, &item.dedupKey, &item.externalID, &item.modelName,
				&item.amount, &item.unit, &item.rawJSON, &occurred, &item.syncedAt); err != nil {
				rows.Close()
				return fmt.Errorf("scan migration legacy account usage: %w", err)
			}
			item.occurredAt = nullInt64Pointer(occurred)
			source.legacyAccountUsage = append(source.legacyAccountUsage, item)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

func loadMigrationLegacyDomain(ctx context.Context, tx *sql.Tx, schema schemaInfo, source *migrationSource) error {
	if schema.hasTable("upstreams") {
		rows, err := tx.QueryContext(ctx, `SELECT id,name,kind,COALESCE(dashboard_url,''),enabled,custom_headers_json,created_at,updated_at
FROM upstreams ORDER BY id`)
		if err != nil {
			return fmt.Errorf("load migration legacy upstreams: %w", err)
		}
		for rows.Next() {
			var item migrationLegacyUpstream
			var enabled int
			if err := rows.Scan(&item.id, &item.name, &item.protocol, &item.dashboardURL, &enabled,
				&item.customHeaders, &item.createdAt, &item.updatedAt); err != nil {
				rows.Close()
				return fmt.Errorf("scan migration legacy upstream: %w", err)
			}
			item.enabled = enabled == 1
			source.legacyUpstreams = append(source.legacyUpstreams, item)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	if schema.hasTable("upstream_endpoints") {
		rows, err := tx.QueryContext(ctx, `SELECT id,upstream_id,name,base_url,position,enabled,created_at,updated_at
FROM upstream_endpoints ORDER BY upstream_id,position,id`)
		if err != nil {
			return fmt.Errorf("load migration legacy endpoints: %w", err)
		}
		for rows.Next() {
			var item migrationLegacyEndpoint
			var enabled int
			if err := rows.Scan(&item.id, &item.upstreamID, &item.name, &item.baseURL, &item.position,
				&enabled, &item.createdAt, &item.updatedAt); err != nil {
				rows.Close()
				return fmt.Errorf("scan migration legacy endpoint: %w", err)
			}
			item.enabled = enabled == 1
			source.legacyEndpoints = append(source.legacyEndpoints, item)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	if schema.hasTable("upstream_credentials") {
		rows, err := tx.QueryContext(ctx, `SELECT id,upstream_id,name,secret_cipher,enabled,runtime_state,created_at,updated_at
FROM upstream_credentials ORDER BY upstream_id,id`)
		if err != nil {
			return fmt.Errorf("load migration legacy credentials: %w", err)
		}
		for rows.Next() {
			var item migrationLegacyCredential
			var enabled int
			if err := rows.Scan(&item.id, &item.upstreamID, &item.name, &item.secretCipher, &enabled,
				&item.runtimeState, &item.createdAt, &item.updatedAt); err != nil {
				rows.Close()
				return fmt.Errorf("scan migration legacy credential: %w", err)
			}
			item.enabled = enabled == 1
			item.secretCipher = append([]byte(nil), item.secretCipher...)
			source.legacyCredentials = append(source.legacyCredentials, item)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	if schema.hasTable("upstream_models") {
		rows, err := tx.QueryContext(ctx, `SELECT id,upstream_id,model_name,enabled,last_seen_at,created_at,updated_at
FROM upstream_models ORDER BY upstream_id,model_name,id`)
		if err != nil {
			return fmt.Errorf("load migration legacy models: %w", err)
		}
		for rows.Next() {
			var item migrationLegacyModel
			var enabled int
			var lastSeen sql.NullInt64
			if err := rows.Scan(&item.id, &item.upstreamID, &item.name, &enabled, &lastSeen,
				&item.createdAt, &item.updatedAt); err != nil {
				rows.Close()
				return fmt.Errorf("scan migration legacy model: %w", err)
			}
			item.enabled = enabled == 1
			item.lastSeenAt = nullInt64Pointer(lastSeen)
			source.legacyModels = append(source.legacyModels, item)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	if schema.hasTable("routes") {
		rows, err := tx.QueryContext(ctx, `SELECT id,public_model,enabled,revision,created_at,updated_at FROM routes ORDER BY id`)
		if err != nil {
			return fmt.Errorf("load migration legacy routes: %w", err)
		}
		for rows.Next() {
			var item migrationLegacyRoute
			var enabled int
			if err := rows.Scan(&item.id, &item.name, &enabled, &item.revision, &item.createdAt, &item.updatedAt); err != nil {
				rows.Close()
				return fmt.Errorf("scan migration legacy route: %w", err)
			}
			item.enabled = enabled == 1
			source.legacyRoutes = append(source.legacyRoutes, item)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	if schema.hasTable("route_targets") {
		rows, err := tx.QueryContext(ctx, `SELECT id,route_id,upstream_model_id,endpoint_id,credential_id,
COALESCE(upstream_model_override,''),position,enabled,created_at,updated_at FROM route_targets ORDER BY route_id,position,id`)
		if err != nil {
			return fmt.Errorf("load migration legacy route targets: %w", err)
		}
		for rows.Next() {
			var item migrationLegacyRouteTarget
			var enabled int
			if err := rows.Scan(&item.id, &item.routeID, &item.modelID, &item.endpointID, &item.credentialID,
				&item.sourceModelOverride, &item.position, &enabled, &item.createdAt, &item.updatedAt); err != nil {
				rows.Close()
				return fmt.Errorf("scan migration legacy route target: %w", err)
			}
			item.enabled = enabled == 1
			source.legacyRouteTargets = append(source.legacyRouteTargets, item)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	if schema.hasTable("legacy_upstream_site_mappings") {
		rows, err := tx.QueryContext(ctx, `SELECT upstream_id,site_id,endpoint_id,credential_id FROM legacy_upstream_site_mappings ORDER BY upstream_id`)
		if err != nil {
			return fmt.Errorf("load migration legacy site mappings: %w", err)
		}
		for rows.Next() {
			var item migrationLegacySiteMapping
			var endpointID, credentialID sql.NullInt64
			if err := rows.Scan(&item.upstreamID, &item.siteID, &endpointID, &credentialID); err != nil {
				rows.Close()
				return fmt.Errorf("scan migration legacy site mapping: %w", err)
			}
			item.endpointID = nullInt64Pointer(endpointID)
			item.credentialID = nullInt64Pointer(credentialID)
			source.legacySiteMappings[item.upstreamID] = item
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

func loadMigrationRuntimeSettings(ctx context.Context, tx *sql.Tx, schema schemaInfo, source *migrationSource) error {
	if !schema.hasTable("app_settings") {
		return nil
	}
	firstOutput := "15"
	if schema.hasColumn("app_settings", "first_output_timeout_seconds") {
		firstOutput = "first_output_timeout_seconds"
	}
	streamIdle := "60"
	if schema.hasColumn("app_settings", "stream_idle_timeout_seconds") {
		streamIdle = "stream_idle_timeout_seconds"
	}
	query := fmt.Sprintf(`SELECT default_cooldown_seconds,failure_threshold,failure_window_seconds,probe_interval_seconds,
%s,%s,request_deadline_seconds,max_attempts,log_retention_days,updated_at FROM app_settings WHERE id=1`, firstOutput, streamIdle)
	var item migrationRuntimeSettings
	if err := tx.QueryRowContext(ctx, query).Scan(&item.cooldownSeconds, &item.failureThreshold,
		&item.failureWindowSeconds, &item.probeIntervalSeconds, &item.firstOutputTimeoutSecond,
		&item.streamIdleTimeoutSecond, &item.requestDeadlineSeconds, &item.maxAttempts,
		&item.logRetentionDays, &item.updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return fmt.Errorf("load migration runtime settings: %w", err)
	}
	source.runtimeSettings = &item
	return nil
}
