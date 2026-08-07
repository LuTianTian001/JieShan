package vnextmigration

import (
	"context"
	"database/sql"
	"fmt"
)

type sourceInventory struct {
	keys                 []sourceKey
	profiles             map[int64]string
	v3Models             map[string]sourceV3Model
	legacyRoutes         map[string]sourceLegacyRoute
	v3Targets            map[int64]sourceV3Target
	v3TargetsByModel     map[int64][]int64
	profileTargets       map[profileModelKey][]sourceProfileTarget
	v3CredentialsBySite  map[int64][]sourceV3Credential
	credentialAccess     map[credentialModelKey]string
	legacyTargetsByRoute map[int64][]sourceLegacyTarget
	issues               []Issue
}

type sourceKey struct {
	id               int64
	name             string
	prefix           string
	enabled          bool
	allowedModelsRaw string
	profileID        *int64
	expiresAt        *int64
}

type sourceV3Model struct {
	id       int64
	name     string
	enabled  bool
	priceSKU string
	revision int64
}

type sourceLegacyRoute struct {
	id       int64
	name     string
	enabled  bool
	revision int64
}

type sourceV3Target struct {
	id               int64
	publishedModelID int64
	siteID           int64
	siteName         string
	siteFound        bool
	siteEnabled      bool
	endpointID       int64
	endpointName     string
	baseURL          string
	protocol         string
	endpointFound    bool
	endpointEnabled  bool
	siteModelID      int64
	sourceModel      string
	siteModelFound   bool
	siteModelEnabled bool
	position         int
	enabled          bool
}

type sourceProfileTarget struct {
	targetID int64
	position int
}

type profileModelKey struct {
	profileID int64
	modelID   int64
}

type sourceV3Credential struct {
	id            int64
	siteID        int64
	name          string
	enabled       bool
	runtimeState  string
	cooldownUntil *int64
	configured    bool
}

type credentialModelKey struct {
	credentialID int64
	modelID      int64
}

type sourceLegacyTarget struct {
	id                   int64
	routeID              int64
	position             int
	enabled              bool
	upstreamID           int64
	upstreamName         string
	upstreamFound        bool
	upstreamEnabled      bool
	protocol             string
	upstreamModelID      int64
	sourceModel          string
	upstreamModelFound   bool
	upstreamModelEnabled bool
	endpointID           int64
	endpointName         string
	baseURL              string
	endpointFound        bool
	endpointEnabled      bool
	endpointUpstreamID   int64
	credentialID         int64
	credentialName       string
	credentialFound      bool
	credentialEnabled    bool
	credentialState      string
	credentialConfigured bool
	credentialUpstreamID int64
}

func loadInventory(ctx context.Context, tx *sql.Tx, schema schemaInfo) (sourceInventory, error) {
	inventory := sourceInventory{
		profiles:             map[int64]string{},
		v3Models:             map[string]sourceV3Model{},
		legacyRoutes:         map[string]sourceLegacyRoute{},
		v3Targets:            map[int64]sourceV3Target{},
		v3TargetsByModel:     map[int64][]int64{},
		profileTargets:       map[profileModelKey][]sourceProfileTarget{},
		v3CredentialsBySite:  map[int64][]sourceV3Credential{},
		credentialAccess:     map[credentialModelKey]string{},
		legacyTargetsByRoute: map[int64][]sourceLegacyTarget{},
	}
	var err error
	if inventory.keys, err = loadSourceKeys(ctx, tx, schema); err != nil {
		return inventory, err
	}
	if err := loadProfiles(ctx, tx, schema, &inventory); err != nil {
		return inventory, err
	}
	if err := loadV3Inventory(ctx, tx, schema, &inventory); err != nil {
		return inventory, err
	}
	if err := loadLegacyInventory(ctx, tx, schema, &inventory); err != nil {
		return inventory, err
	}
	return inventory, nil
}

func loadSourceKeys(ctx context.Context, tx *sql.Tx, schema schemaInfo) ([]sourceKey, error) {
	allowedExpr := `'[]'`
	if schema.hasColumn("downstream_keys", "allowed_models_json") {
		allowedExpr = `COALESCE(allowed_models_json,'[]')`
	}
	profileExpr := `NULL`
	if schema.hasColumn("downstream_keys", "routing_profile_id") {
		profileExpr = `routing_profile_id`
	}
	expiresExpr := `NULL`
	if schema.hasColumn("downstream_keys", "expires_at") {
		expiresExpr = `expires_at`
	}
	query := fmt.Sprintf(`SELECT id,name,key_prefix,enabled,%s,%s,%s FROM downstream_keys ORDER BY id`, allowedExpr, profileExpr, expiresExpr)
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("load downstream keys: %w", err)
	}
	defer rows.Close()
	keys := make([]sourceKey, 0)
	for rows.Next() {
		var key sourceKey
		var enabled int
		var profileID, expiresAt sql.NullInt64
		if err := rows.Scan(&key.id, &key.name, &key.prefix, &enabled, &key.allowedModelsRaw, &profileID, &expiresAt); err != nil {
			return nil, fmt.Errorf("scan downstream key: %w", err)
		}
		key.enabled = enabled == 1
		key.profileID = nullInt64Pointer(profileID)
		key.expiresAt = nullInt64Pointer(expiresAt)
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate downstream keys: %w", err)
	}
	return keys, nil
}

func loadProfiles(ctx context.Context, tx *sql.Tx, schema schemaInfo, inventory *sourceInventory) error {
	if schema.hasTable("routing_profiles") {
		rows, err := tx.QueryContext(ctx, `SELECT id,name FROM routing_profiles ORDER BY id`)
		if err != nil {
			return fmt.Errorf("load routing profiles: %w", err)
		}
		for rows.Next() {
			var id int64
			var name string
			if err := rows.Scan(&id, &name); err != nil {
				rows.Close()
				return fmt.Errorf("scan routing profile: %w", err)
			}
			inventory.profiles[id] = name
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close routing profiles: %w", err)
		}
	}
	if !schema.hasTable("routing_profile_model_targets") {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT routing_profile_id,published_model_id,route_site_target_id,position
FROM routing_profile_model_targets ORDER BY routing_profile_id,published_model_id,position,route_site_target_id`)
	if err != nil {
		return fmt.Errorf("load routing profile targets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key profileModelKey
		var target sourceProfileTarget
		if err := rows.Scan(&key.profileID, &key.modelID, &target.targetID, &target.position); err != nil {
			return fmt.Errorf("scan routing profile target: %w", err)
		}
		inventory.profileTargets[key] = append(inventory.profileTargets[key], target)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate routing profile targets: %w", err)
	}
	return nil
}

func loadV3Inventory(ctx context.Context, tx *sql.Tx, schema schemaInfo, inventory *sourceInventory) error {
	if !schema.hasTable("published_models") {
		return nil
	}
	priceExpr := `''`
	if schema.hasColumn("published_models", "official_price_sku") {
		priceExpr = `COALESCE(official_price_sku,'')`
	}
	revisionExpr := `1`
	if schema.hasColumn("published_models", "revision") {
		revisionExpr = `revision`
	}
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT id,public_name,enabled,%s,%s FROM published_models ORDER BY id`, priceExpr, revisionExpr))
	if err != nil {
		return fmt.Errorf("load V3 published models: %w", err)
	}
	for rows.Next() {
		var model sourceV3Model
		var enabled int
		if err := rows.Scan(&model.id, &model.name, &enabled, &model.priceSKU, &model.revision); err != nil {
			rows.Close()
			return fmt.Errorf("scan V3 published model: %w", err)
		}
		model.enabled = enabled == 1
		inventory.v3Models[model.name] = model
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close V3 published models: %w", err)
	}

	if schema.hasTable("route_site_targets") {
		if err := loadV3Targets(ctx, tx, schema, inventory); err != nil {
			return err
		}
	}
	if schema.hasTable("inference_credentials") {
		credentialRows, err := tx.QueryContext(ctx, `SELECT id,site_id,name,enabled,runtime_state,cooldown_until,
CASE WHEN length(secret_cipher)>0 THEN 1 ELSE 0 END FROM inference_credentials ORDER BY site_id,position,id`)
		if err != nil {
			return fmt.Errorf("load V3 credentials: %w", err)
		}
		for credentialRows.Next() {
			var credential sourceV3Credential
			var enabled, configured int
			var cooldown sql.NullInt64
			if err := credentialRows.Scan(&credential.id, &credential.siteID, &credential.name, &enabled,
				&credential.runtimeState, &cooldown, &configured); err != nil {
				credentialRows.Close()
				return fmt.Errorf("scan V3 credential: %w", err)
			}
			credential.enabled = enabled == 1
			credential.configured = configured == 1
			credential.cooldownUntil = nullInt64Pointer(cooldown)
			inventory.v3CredentialsBySite[credential.siteID] = append(inventory.v3CredentialsBySite[credential.siteID], credential)
		}
		if err := credentialRows.Close(); err != nil {
			return fmt.Errorf("close V3 credentials: %w", err)
		}
	}
	if schema.hasTable("credential_model_access") {
		accessRows, err := tx.QueryContext(ctx, `SELECT credential_id,site_model_id,availability FROM credential_model_access`)
		if err != nil {
			return fmt.Errorf("load credential model access: %w", err)
		}
		for accessRows.Next() {
			var key credentialModelKey
			var availability string
			if err := accessRows.Scan(&key.credentialID, &key.modelID, &availability); err != nil {
				accessRows.Close()
				return fmt.Errorf("scan credential model access: %w", err)
			}
			inventory.credentialAccess[key] = availability
		}
		if err := accessRows.Close(); err != nil {
			return fmt.Errorf("close credential model access: %w", err)
		}
	}
	return nil
}

func loadV3Targets(ctx context.Context, tx *sql.Tx, schema schemaInfo, inventory *sourceInventory) error {
	completeDomain := schema.hasTable("sites") && schema.hasTable("inference_endpoints") && schema.hasTable("site_models")
	if !completeDomain {
		inventory.issues = append(inventory.issues, Issue{Code: "v3_domain_tables_missing", Severity: SeverityError,
			Message: "V3 route targets exist, but one or more sites/endpoints/models tables are missing."})
		rows, err := tx.QueryContext(ctx, `SELECT id,published_model_id,site_id,endpoint_id,site_model_id,position,enabled
FROM route_site_targets ORDER BY published_model_id,position,id`)
		if err != nil {
			return fmt.Errorf("load partial V3 targets: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var target sourceV3Target
			var enabled int
			if err := rows.Scan(&target.id, &target.publishedModelID, &target.siteID, &target.endpointID,
				&target.siteModelID, &target.position, &enabled); err != nil {
				return fmt.Errorf("scan partial V3 target: %w", err)
			}
			target.enabled = enabled == 1
			inventory.v3Targets[target.id] = target
			inventory.v3TargetsByModel[target.publishedModelID] = append(inventory.v3TargetsByModel[target.publishedModelID], target.id)
		}
		return rows.Err()
	}

	rows, err := tx.QueryContext(ctx, `SELECT t.id,t.published_model_id,t.site_id,s.id,s.name,s.enabled,
t.endpoint_id,e.id,e.name,e.base_url,e.wire_protocol,e.enabled,
t.site_model_id,m.id,m.model_name,m.enabled,t.position,t.enabled
FROM route_site_targets t
LEFT JOIN sites s ON s.id=t.site_id
LEFT JOIN inference_endpoints e ON e.id=t.endpoint_id
LEFT JOIN site_models m ON m.id=t.site_model_id
ORDER BY t.published_model_id,t.position,t.id`)
	if err != nil {
		return fmt.Errorf("load V3 targets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var target sourceV3Target
		var siteID, endpointID, siteModelID sql.NullInt64
		var siteName, endpointName, baseURL, protocol, sourceModel sql.NullString
		var siteEnabled, endpointEnabled, siteModelEnabled sql.NullInt64
		var enabled int
		if err := rows.Scan(&target.id, &target.publishedModelID, &target.siteID, &siteID, &siteName, &siteEnabled,
			&target.endpointID, &endpointID, &endpointName, &baseURL, &protocol, &endpointEnabled,
			&target.siteModelID, &siteModelID, &sourceModel, &siteModelEnabled, &target.position, &enabled); err != nil {
			return fmt.Errorf("scan V3 target: %w", err)
		}
		target.enabled = enabled == 1
		target.siteFound = siteID.Valid
		target.siteName = siteName.String
		target.siteEnabled = siteEnabled.Valid && siteEnabled.Int64 == 1
		target.endpointFound = endpointID.Valid
		target.endpointName = endpointName.String
		target.baseURL = baseURL.String
		target.protocol = protocol.String
		target.endpointEnabled = endpointEnabled.Valid && endpointEnabled.Int64 == 1
		target.siteModelFound = siteModelID.Valid
		target.sourceModel = sourceModel.String
		target.siteModelEnabled = siteModelEnabled.Valid && siteModelEnabled.Int64 == 1
		inventory.v3Targets[target.id] = target
		inventory.v3TargetsByModel[target.publishedModelID] = append(inventory.v3TargetsByModel[target.publishedModelID], target.id)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate V3 targets: %w", err)
	}
	return nil
}

func loadLegacyInventory(ctx context.Context, tx *sql.Tx, schema schemaInfo, inventory *sourceInventory) error {
	if !schema.hasTable("routes") {
		return nil
	}
	revisionExpr := `1`
	if schema.hasColumn("routes", "revision") {
		revisionExpr = `revision`
	}
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT id,public_model,enabled,%s FROM routes ORDER BY id`, revisionExpr))
	if err != nil {
		return fmt.Errorf("load legacy routes: %w", err)
	}
	for rows.Next() {
		var route sourceLegacyRoute
		var enabled int
		if err := rows.Scan(&route.id, &route.name, &enabled, &route.revision); err != nil {
			rows.Close()
			return fmt.Errorf("scan legacy route: %w", err)
		}
		route.enabled = enabled == 1
		inventory.legacyRoutes[route.name] = route
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close legacy routes: %w", err)
	}
	if !schema.hasTable("route_targets") {
		return nil
	}
	completeDomain := schema.hasTable("upstreams") && schema.hasTable("upstream_models") &&
		schema.hasTable("upstream_endpoints") && schema.hasTable("upstream_credentials")
	if !completeDomain {
		inventory.issues = append(inventory.issues, Issue{Code: "legacy_domain_tables_missing", Severity: SeverityError,
			Message: "Legacy route targets exist, but one or more upstream/model/endpoint/credential tables are missing."})
		return loadPartialLegacyTargets(ctx, tx, inventory)
	}

	targetRows, err := tx.QueryContext(ctx, `SELECT t.id,t.route_id,t.position,t.enabled,
m.upstream_id,u.id,u.name,u.enabled,u.kind,
t.upstream_model_id,m.id,COALESCE(t.upstream_model_override,m.model_name),m.enabled,
t.endpoint_id,e.id,e.name,e.base_url,e.enabled,e.upstream_id,
t.credential_id,c.id,c.name,c.enabled,c.runtime_state,CASE WHEN length(c.secret_cipher)>0 THEN 1 ELSE 0 END,c.upstream_id
FROM route_targets t
LEFT JOIN upstream_models m ON m.id=t.upstream_model_id
LEFT JOIN upstreams u ON u.id=m.upstream_id
LEFT JOIN upstream_endpoints e ON e.id=t.endpoint_id
LEFT JOIN upstream_credentials c ON c.id=t.credential_id
ORDER BY t.route_id,t.position,t.id`)
	if err != nil {
		return fmt.Errorf("load legacy targets: %w", err)
	}
	defer targetRows.Close()
	for targetRows.Next() {
		var target sourceLegacyTarget
		var enabled int
		var upstreamID, upstreamFoundID, modelFoundID, endpointFoundID, endpointUpstreamID sql.NullInt64
		var credentialFoundID, credentialUpstreamID sql.NullInt64
		var upstreamName, protocol, sourceModel, endpointName, baseURL, credentialName, credentialState sql.NullString
		var upstreamEnabled, modelEnabled, endpointEnabled, credentialEnabled, credentialConfigured sql.NullInt64
		if err := targetRows.Scan(&target.id, &target.routeID, &target.position, &enabled,
			&upstreamID, &upstreamFoundID, &upstreamName, &upstreamEnabled, &protocol,
			&target.upstreamModelID, &modelFoundID, &sourceModel, &modelEnabled,
			&target.endpointID, &endpointFoundID, &endpointName, &baseURL, &endpointEnabled, &endpointUpstreamID,
			&target.credentialID, &credentialFoundID, &credentialName, &credentialEnabled, &credentialState,
			&credentialConfigured, &credentialUpstreamID); err != nil {
			return fmt.Errorf("scan legacy target: %w", err)
		}
		target.enabled = enabled == 1
		if upstreamID.Valid {
			target.upstreamID = upstreamID.Int64
		}
		target.upstreamFound = upstreamFoundID.Valid
		target.upstreamName = upstreamName.String
		target.upstreamEnabled = upstreamEnabled.Valid && upstreamEnabled.Int64 == 1
		target.protocol = protocol.String
		target.upstreamModelFound = modelFoundID.Valid
		target.sourceModel = sourceModel.String
		target.upstreamModelEnabled = modelEnabled.Valid && modelEnabled.Int64 == 1
		target.endpointFound = endpointFoundID.Valid
		target.endpointName = endpointName.String
		target.baseURL = baseURL.String
		target.endpointEnabled = endpointEnabled.Valid && endpointEnabled.Int64 == 1
		if endpointUpstreamID.Valid {
			target.endpointUpstreamID = endpointUpstreamID.Int64
		}
		target.credentialFound = credentialFoundID.Valid
		target.credentialName = credentialName.String
		target.credentialEnabled = credentialEnabled.Valid && credentialEnabled.Int64 == 1
		target.credentialState = credentialState.String
		target.credentialConfigured = credentialConfigured.Valid && credentialConfigured.Int64 == 1
		if credentialUpstreamID.Valid {
			target.credentialUpstreamID = credentialUpstreamID.Int64
		}
		inventory.legacyTargetsByRoute[target.routeID] = append(inventory.legacyTargetsByRoute[target.routeID], target)
	}
	if err := targetRows.Err(); err != nil {
		return fmt.Errorf("iterate legacy targets: %w", err)
	}
	return nil
}

func loadPartialLegacyTargets(ctx context.Context, tx *sql.Tx, inventory *sourceInventory) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,route_id,upstream_model_id,endpoint_id,credential_id,position,enabled
FROM route_targets ORDER BY route_id,position,id`)
	if err != nil {
		return fmt.Errorf("load partial legacy targets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var target sourceLegacyTarget
		var enabled int
		if err := rows.Scan(&target.id, &target.routeID, &target.upstreamModelID, &target.endpointID,
			&target.credentialID, &target.position, &enabled); err != nil {
			return fmt.Errorf("scan partial legacy target: %w", err)
		}
		target.enabled = enabled == 1
		inventory.legacyTargetsByRoute[target.routeID] = append(inventory.legacyTargetsByRoute[target.routeID], target)
	}
	return rows.Err()
}

func nullInt64Pointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	copy := value.Int64
	return &copy
}
