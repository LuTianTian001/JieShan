package vnextmigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	vnextsecretbox "github.com/LuTianTian001/JieShan/internal/vnext/secretbox"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const migratedCipherVersion = int64(1)

type endpointSurfaceKey struct {
	sourceID int64
	surface  string
}

type endpointSignature struct {
	siteID   int64
	baseURL  string
	protocol string
	surface  string
}

type providerSignature struct {
	endpointID int64
	model      string
}

type publishedSurfaceKey struct {
	sourceTargetID int64
	surface        string
}

type pendingAccess struct {
	siteID       int64
	endpointID   int64
	credentialID int64
	providerID   int64
	availability string
	lastChecked  *int64
	revision     int64
	updatedAt    int64
}

type migrationAccountConnectionInput struct {
	sourceKind  string
	sourceID    int64
	siteID      int64
	adapterKind string
	origin      string
	authCipher  []byte
	enabled     bool
	lastAttempt *int64
	lastSuccess *int64
	lastError   string
	createdAt   int64
	updatedAt   int64
}

type migrationApplier struct {
	ctx          context.Context
	tx           *sql.Tx
	source       migrationSource
	legacyCipher *legacyCipher
	box          *vnextsecretbox.Box
	priceSKUs    map[string]string
	options      MigrationOptions
	result       MigrationResult

	siteMap              map[int64]int64
	endpointMap          map[endpointSurfaceKey]int64
	credentialMap        map[int64]int64
	siteModelMap         map[endpointSurfaceKey]int64
	publishedModelMap    map[int64]int64
	publishedModelByName map[string]int64
	publishedTargetMap   map[publishedSurfaceKey]int64
	profileMap           map[int64]int64
	accountMap           map[int64]int64
	legacyAccountMap     map[int64]int64
	accountBySite        map[int64]int64
	legacySiteMap        map[int64]int64
	legacyEndpointMap    map[endpointSurfaceKey]int64
	legacyCredentialMap  map[int64]int64
	endpointBySignature  map[endpointSignature]int64
	providerBySignature  map[providerSignature]int64
	providerEndpoint     map[int64]int64
	v3Provider           map[int64]bool
	legacyExclusive      map[int64]map[int64]bool
	endpointBindings     map[int64][]int64
	endpointBindingSet   map[int64]map[int64]bool
	pendingAccess        map[[2]int64]pendingAccess
	nextEndpointPosition map[int64]int
	usedSiteNames        map[string]bool
	usedCredentialNames  map[int64]map[string]bool
	usedProfileNames     map[string]bool
	usedKeyNames         map[string]bool
}

func applyMigration(
	ctx context.Context,
	db *sql.DB,
	source migrationSource,
	legacyCipher *legacyCipher,
	box *vnextsecretbox.Box,
	priceSKUs map[string]string,
	options MigrationOptions,
) (MigrationResult, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return MigrationResult{}, fmt.Errorf("begin VNext import transaction: %w", err)
	}
	defer tx.Rollback()
	applier := &migrationApplier{
		ctx: ctx, tx: tx, source: source, legacyCipher: legacyCipher, box: box,
		priceSKUs: priceSKUs, options: options,
		result:  MigrationResult{Warnings: []string{}},
		siteMap: make(map[int64]int64), endpointMap: make(map[endpointSurfaceKey]int64),
		credentialMap: make(map[int64]int64), siteModelMap: make(map[endpointSurfaceKey]int64),
		publishedModelMap: make(map[int64]int64), publishedModelByName: make(map[string]int64),
		publishedTargetMap: make(map[publishedSurfaceKey]int64), profileMap: make(map[int64]int64),
		accountMap: make(map[int64]int64), legacyAccountMap: make(map[int64]int64),
		accountBySite: make(map[int64]int64), legacySiteMap: make(map[int64]int64),
		legacyEndpointMap: make(map[endpointSurfaceKey]int64), legacyCredentialMap: make(map[int64]int64),
		endpointBySignature: make(map[endpointSignature]int64), providerBySignature: make(map[providerSignature]int64),
		providerEndpoint: make(map[int64]int64), v3Provider: make(map[int64]bool),
		legacyExclusive: make(map[int64]map[int64]bool), endpointBindings: make(map[int64][]int64),
		endpointBindingSet: make(map[int64]map[int64]bool), pendingAccess: make(map[[2]int64]pendingAccess),
		nextEndpointPosition: make(map[int64]int), usedSiteNames: make(map[string]bool),
		usedCredentialNames: make(map[int64]map[string]bool), usedProfileNames: make(map[string]bool),
		usedKeyNames: make(map[string]bool),
	}
	steps := []func() error{
		applier.applyRuntimeSettings,
		applier.insertSites,
		applier.insertCredentials,
		applier.insertEndpoints,
		applier.insertProviderInventory,
		applier.prepareLegacyResources,
		applier.insertPublishedModelsAndRoutes,
		applier.persistEndpointBindings,
		applier.persistCredentialAccess,
		applier.insertRoutingProfiles,
		applier.insertDownstreamKeys,
		applier.insertAccounts,
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return MigrationResult{}, err
		}
	}
	if err := applier.validateDestination(); err != nil {
		return MigrationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return MigrationResult{}, fmt.Errorf("commit VNext import transaction: %w", err)
	}
	return applier.result, nil
}

func (applier *migrationApplier) applyRuntimeSettings() error {
	settings := applier.source.runtimeSettings
	if settings == nil {
		return nil
	}
	input := vnextstore.RuntimeSettingsWrite{
		FailureThreshold:   settings.failureThreshold,
		FailureWindow:      time.Duration(settings.failureWindowSeconds) * time.Second,
		Cooldown:           time.Duration(settings.cooldownSeconds) * time.Second,
		ProbeInterval:      time.Duration(settings.probeIntervalSeconds) * time.Second,
		FirstOutputTimeout: time.Duration(settings.firstOutputTimeoutSecond) * time.Second,
		StreamIdleTimeout:  time.Duration(settings.streamIdleTimeoutSecond) * time.Second,
		RequestTimeout:     time.Duration(settings.requestDeadlineSeconds) * time.Second,
		MaxAttempts:        settings.maxAttempts,
		LogRetentionDays:   settings.logRetentionDays,
	}
	if err := vnextstore.ValidateRuntimeSettingsWrite(input); err != nil {
		return fmt.Errorf("legacy runtime settings are not valid under VNext: %w", err)
	}
	_, err := applier.tx.ExecContext(applier.ctx, `UPDATE runtime_settings SET
failure_threshold=?,failure_window_ms=?,cooldown_ms=?,probe_interval_ms=?,first_output_timeout_ms=?,
stream_idle_timeout_ms=?,request_timeout_ms=?,max_attempts=?,log_retention_days=?,updated_at=? WHERE singleton_id=1`,
		input.FailureThreshold, input.FailureWindow.Milliseconds(), input.Cooldown.Milliseconds(),
		input.ProbeInterval.Milliseconds(), input.FirstOutputTimeout.Milliseconds(), input.StreamIdleTimeout.Milliseconds(),
		input.RequestTimeout.Milliseconds(), input.MaxAttempts, input.LogRetentionDays, nonNegativeTime(settings.updatedAt))
	if err != nil {
		return fmt.Errorf("migrate runtime settings: %w", err)
	}
	return nil
}

func (applier *migrationApplier) insertSites() error {
	for _, source := range applier.source.sites {
		name := applier.uniqueSiteName(source.name, source.id)
		result, err := applier.tx.ExecContext(applier.ctx, `INSERT INTO sites(
name,dashboard_url,enabled,revision,created_at,updated_at) VALUES (?,?,?,?,?,?)`,
			name, nullableText(source.dashboardURL), boolIntMigration(source.enabled), positiveRevision(source.revision),
			nonNegativeTime(source.createdAt), nonNegativeTime(source.updatedAt))
		if err != nil {
			return fmt.Errorf("migrate site %d: %w", source.id, err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		applier.siteMap[source.id] = id
		applier.result.Sites++
	}
	return nil
}

func (applier *migrationApplier) insertCredentials() error {
	for _, source := range applier.source.credentials {
		siteID, ok := applier.siteMap[source.siteID]
		if !ok {
			return fmt.Errorf("credential %d references missing site %d", source.id, source.siteID)
		}
		credentialID, err := applier.insertCredential(siteID, source.name, source.secretCipher, source.enabled,
			source.runtimeState, source.cooldownUntil, source.revision, source.createdAt, source.updatedAt)
		if err != nil {
			return fmt.Errorf("migrate credential %d: %w", source.id, err)
		}
		applier.credentialMap[source.id] = credentialID
	}
	return nil
}

func (applier *migrationApplier) insertCredential(
	siteID int64,
	name string,
	ciphertext []byte,
	enabled bool,
	runtimeState string,
	cooldownUntil *int64,
	revision int64,
	createdAt int64,
	updatedAt int64,
) (int64, error) {
	plaintext, err := applier.legacyCipher.open(ciphertext)
	if err != nil {
		return 0, err
	}
	defer clearBytes(plaintext)
	if len(strings.TrimSpace(string(plaintext))) == 0 {
		return 0, errors.New("decrypted API key is empty")
	}
	name = applier.uniqueCredentialName(siteID, name)
	result, err := applier.tx.ExecContext(applier.ctx, `INSERT INTO site_credentials(
site_id,name,secret_cipher,cipher_version,enabled,revision,created_at,updated_at)
VALUES (?,?,X'00',?,?,?, ?,?)`, siteID, name, migratedCipherVersion, boolIntMigration(enabled),
		positiveRevision(revision), nonNegativeTime(createdAt), nonNegativeTime(updatedAt))
	if err != nil {
		return 0, err
	}
	credentialID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	sealed, err := applier.box.Seal(vnextsecretbox.PurposeSiteCredential,
		vnextsecretbox.Identity{RecordID: credentialID, OwnerID: siteID}, plaintext)
	if err != nil {
		return 0, err
	}
	defer clearBytes(sealed)
	if _, err := applier.tx.ExecContext(applier.ctx, `UPDATE site_credentials SET secret_cipher=? WHERE id=?`, sealed, credentialID); err != nil {
		return 0, err
	}
	state, coolingUntil, status, errorCode := migrateCredentialState(runtimeState, cooldownUntil, applier.nowMS())
	if _, err := applier.tx.ExecContext(applier.ctx, `INSERT INTO credential_runtime_state(
credential_id,state,cooling_until,last_http_status,last_error_code,revision,updated_at) VALUES (?,?,?,?,?,1,?)`,
		credentialID, state, coolingUntil, status, nullableText(errorCode), nonNegativeTime(updatedAt)); err != nil {
		return 0, err
	}
	applier.result.Credentials++
	return credentialID, nil
}

func (applier *migrationApplier) insertEndpoints() error {
	credentialsBySite := make(map[int64][]migrationCredential)
	for _, credential := range applier.source.credentials {
		credentialsBySite[credential.siteID] = append(credentialsBySite[credential.siteID], credential)
	}
	for _, source := range applier.source.endpoints {
		siteID, ok := applier.siteMap[source.siteID]
		if !ok {
			return fmt.Errorf("endpoint %d references missing site %d", source.id, source.siteID)
		}
		pairs, err := mapProtocolSurfaces(source.protocol, applier.options.OpenAISurface)
		if err != nil {
			return fmt.Errorf("endpoint %d protocol: %w", source.id, err)
		}
		for _, pair := range pairs {
			endpointID, err := applier.insertOrReuseEndpoint(siteID, source.name, source.baseURL, pair.protocol,
				pair.surface, source.compatibilityProfile, source.authScheme, source.customHeaders, source.enabled,
				source.revision, source.createdAt, source.updatedAt)
			if err != nil {
				return fmt.Errorf("migrate endpoint %d: %w", source.id, err)
			}
			applier.endpointMap[endpointSurfaceKey{sourceID: source.id, surface: pair.surface}] = endpointID
			for _, credential := range credentialsBySite[source.siteID] {
				if credentialID := applier.credentialMap[credential.id]; credentialID > 0 {
					applier.addEndpointBinding(endpointID, credentialID)
				}
			}
		}
	}
	return nil
}

type protocolSurface struct {
	protocol string
	surface  string
}

func mapProtocolSurfaces(source string, policy OpenAISurfacePolicy) ([]protocolSurface, error) {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "openai_chat_completions", "chat_completions", string(vnextprotocol.OpenAIChatCompletions):
		return []protocolSurface{{protocol: string(vnextprotocol.OpenAI), surface: string(vnextprotocol.OpenAIChatCompletions)}}, nil
	case "openai_responses", "responses", string(vnextprotocol.OpenAIResponses):
		return []protocolSurface{{protocol: string(vnextprotocol.OpenAI), surface: string(vnextprotocol.OpenAIResponses)}}, nil
	case "anthropic", "anthropic_messages", "messages", string(vnextprotocol.AnthropicMessages):
		return []protocolSurface{{protocol: string(vnextprotocol.Anthropic), surface: string(vnextprotocol.AnthropicMessages)}}, nil
	case "gemini", "gemini_generate_content", "generate_content", "generatecontent", string(vnextprotocol.GeminiGenerateContent):
		return []protocolSurface{{protocol: string(vnextprotocol.Gemini), surface: string(vnextprotocol.GeminiGenerateContent)}}, nil
	case "openai", "compatible", "openai-compatible", "openai_compatible":
		switch policy {
		case OpenAISurfaceChat:
			return []protocolSurface{{protocol: string(vnextprotocol.OpenAI), surface: string(vnextprotocol.OpenAIChatCompletions)}}, nil
		case OpenAISurfaceResponses:
			return []protocolSurface{{protocol: string(vnextprotocol.OpenAI), surface: string(vnextprotocol.OpenAIResponses)}}, nil
		case OpenAISurfaceBoth:
			return []protocolSurface{
				{protocol: string(vnextprotocol.OpenAI), surface: string(vnextprotocol.OpenAIChatCompletions)},
				{protocol: string(vnextprotocol.OpenAI), surface: string(vnextprotocol.OpenAIResponses)},
			}, nil
		}
	}
	return nil, fmt.Errorf("unsupported legacy inference protocol %q", source)
}

func (applier *migrationApplier) insertOrReuseEndpoint(
	siteID int64,
	name, baseURL, protocol, surface, adapterKind, authScheme, headers string,
	enabled bool,
	revision, createdAt, updatedAt int64,
) (int64, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if err := validateMigrationBaseURL(baseURL); err != nil {
		return 0, err
	}
	signature := endpointSignature{siteID: siteID, baseURL: strings.ToLower(baseURL), protocol: protocol, surface: surface}
	if existing := applier.endpointBySignature[signature]; existing > 0 {
		return existing, nil
	}
	adapterKind = strings.ToLower(strings.TrimSpace(adapterKind))
	if adapterKind == "" {
		adapterKind = "generic"
	}
	authScheme, err := normalizeMigrationAuthScheme(protocol, authScheme)
	if err != nil {
		return 0, err
	}
	headerTemplate, secretHeaders, err := splitMigrationHeaders(headers)
	if err != nil {
		return 0, err
	}
	position := applier.nextEndpointPosition[siteID]
	applier.nextEndpointPosition[siteID] = position + 1
	result, err := applier.tx.ExecContext(applier.ctx, `INSERT INTO site_endpoints(
site_id,name,base_url,wire_protocol,surface,adapter_kind,auth_scheme,header_template_json,
secret_headers_cipher,cipher_version,position,enabled,revision,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,NULL,0,?,?,?,?,?)`, siteID, fallbackName(name, "Primary"), baseURL, protocol, surface,
		adapterKind, authScheme, headerTemplate, position, boolIntMigration(enabled), positiveRevision(revision),
		nonNegativeTime(createdAt), nonNegativeTime(updatedAt))
	if err != nil {
		return 0, err
	}
	endpointID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if len(secretHeaders) > 0 {
		sealed, sealErr := applier.box.Seal(vnextsecretbox.PurposeSiteSecretHeaders,
			vnextsecretbox.Identity{RecordID: endpointID, OwnerID: siteID}, secretHeaders)
		clearBytes(secretHeaders)
		if sealErr != nil {
			return 0, sealErr
		}
		defer clearBytes(sealed)
		if _, err := applier.tx.ExecContext(applier.ctx, `UPDATE site_endpoints SET secret_headers_cipher=?,cipher_version=? WHERE id=?`,
			sealed, migratedCipherVersion, endpointID); err != nil {
			return 0, err
		}
	}
	applier.endpointBySignature[signature] = endpointID
	applier.result.Endpoints++
	return endpointID, nil
}

func (applier *migrationApplier) insertProviderInventory() error {
	endpointByID := make(map[int64]migrationEndpoint, len(applier.source.endpoints))
	for _, endpoint := range applier.source.endpoints {
		endpointByID[endpoint.id] = endpoint
	}
	for _, source := range applier.source.siteModels {
		endpoint, ok := endpointByID[source.endpointID]
		if !ok || endpoint.siteID != source.siteID {
			return fmt.Errorf("site model %d references missing or mismatched endpoint %d", source.id, source.endpointID)
		}
		pairs, err := mapProtocolSurfaces(endpoint.protocol, applier.options.OpenAISurface)
		if err != nil {
			return fmt.Errorf("site model %d protocol: %w", source.id, err)
		}
		for _, pair := range pairs {
			endpointID := applier.endpointMap[endpointSurfaceKey{sourceID: source.endpointID, surface: pair.surface}]
			if endpointID <= 0 {
				return fmt.Errorf("site model %d has no migrated endpoint for %s", source.id, pair.surface)
			}
			providerID, err := applier.insertOrReuseProviderTarget(endpointID, applier.siteMap[source.siteID], source.name,
				source.displayName, source.enabled, source.lastSeenAt, source.revision, source.createdAt, source.updatedAt)
			if err != nil {
				return fmt.Errorf("migrate site model %d: %w", source.id, err)
			}
			applier.siteModelMap[endpointSurfaceKey{sourceID: source.id, surface: pair.surface}] = providerID
			applier.v3Provider[providerID] = true
		}
	}
	return nil
}

func (applier *migrationApplier) insertOrReuseProviderTarget(
	endpointID, siteID int64,
	model, displayName string,
	enabled bool,
	lastSeenAt *int64,
	revision, createdAt, updatedAt int64,
) (int64, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return 0, errors.New("source model is empty")
	}
	signature := providerSignature{endpointID: endpointID, model: model}
	if existing := applier.providerBySignature[signature]; existing > 0 {
		return existing, nil
	}
	result, err := applier.tx.ExecContext(applier.ctx, `INSERT INTO provider_model_targets(
site_id,endpoint_id,source_model,display_name,enabled,revision,last_seen_at,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,?)`, siteID, endpointID, model, nullableText(displayName), boolIntMigration(enabled),
		positiveRevision(revision), lastSeenAt, nonNegativeTime(createdAt), nonNegativeTime(updatedAt))
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	applier.providerBySignature[signature] = id
	applier.providerEndpoint[id] = endpointID
	applier.result.ProviderModelTargets++
	return id, nil
}

func (applier *migrationApplier) prepareLegacyResources() error {
	upstreamByID := make(map[int64]migrationLegacyUpstream, len(applier.source.legacyUpstreams))
	for _, upstream := range applier.source.legacyUpstreams {
		upstreamByID[upstream.id] = upstream
		if mapping, ok := applier.source.legacySiteMappings[upstream.id]; ok {
			if siteID := applier.siteMap[mapping.siteID]; siteID > 0 {
				applier.legacySiteMap[upstream.id] = siteID
				continue
			}
			return fmt.Errorf("legacy upstream %d maps to missing site %d", upstream.id, mapping.siteID)
		}
		name := applier.uniqueSiteName(upstream.name, upstream.id)
		result, err := applier.tx.ExecContext(applier.ctx, `INSERT INTO sites(
name,dashboard_url,enabled,revision,created_at,updated_at) VALUES (?,?,?,?,?,?)`, name,
			nullableText(upstream.dashboardURL), boolIntMigration(upstream.enabled), 1,
			nonNegativeTime(upstream.createdAt), nonNegativeTime(upstream.updatedAt))
		if err != nil {
			return fmt.Errorf("migrate legacy upstream %d: %w", upstream.id, err)
		}
		siteID, _ := result.LastInsertId()
		applier.legacySiteMap[upstream.id] = siteID
		applier.result.Sites++
	}
	for _, source := range applier.source.legacyCredentials {
		mapping := applier.source.legacySiteMappings[source.upstreamID]
		if mapping.credentialID != nil {
			if credentialID := applier.credentialMap[*mapping.credentialID]; credentialID > 0 {
				applier.legacyCredentialMap[source.id] = credentialID
				continue
			}
		}
		siteID := applier.legacySiteMap[source.upstreamID]
		if siteID <= 0 {
			return fmt.Errorf("legacy credential %d references missing upstream %d", source.id, source.upstreamID)
		}
		credentialID, err := applier.insertCredential(siteID, source.name, source.secretCipher, source.enabled,
			source.runtimeState, nil, 1, source.createdAt, source.updatedAt)
		if err != nil {
			return fmt.Errorf("migrate legacy credential %d: %w", source.id, err)
		}
		applier.legacyCredentialMap[source.id] = credentialID
	}
	for _, source := range applier.source.legacyEndpoints {
		upstream, ok := upstreamByID[source.upstreamID]
		if !ok {
			return fmt.Errorf("legacy endpoint %d references missing upstream %d", source.id, source.upstreamID)
		}
		pairs, err := mapProtocolSurfaces(upstream.protocol, applier.options.OpenAISurface)
		if err != nil {
			return fmt.Errorf("legacy endpoint %d protocol: %w", source.id, err)
		}
		mapping := applier.source.legacySiteMappings[source.upstreamID]
		for _, pair := range pairs {
			if mapping.endpointID != nil {
				if endpointID := applier.endpointMap[endpointSurfaceKey{sourceID: *mapping.endpointID, surface: pair.surface}]; endpointID > 0 {
					applier.legacyEndpointMap[endpointSurfaceKey{sourceID: source.id, surface: pair.surface}] = endpointID
					if mapping.credentialID != nil {
						if credentialID := applier.credentialMap[*mapping.credentialID]; credentialID > 0 {
							applier.addEndpointBinding(endpointID, credentialID)
						}
					}
					continue
				}
			}
			endpointID, err := applier.insertOrReuseEndpoint(applier.legacySiteMap[source.upstreamID], source.name,
				source.baseURL, pair.protocol, pair.surface, "generic", "", upstream.customHeaders,
				source.enabled && upstream.enabled, 1, source.createdAt, source.updatedAt)
			if err != nil {
				return fmt.Errorf("migrate legacy endpoint %d: %w", source.id, err)
			}
			applier.legacyEndpointMap[endpointSurfaceKey{sourceID: source.id, surface: pair.surface}] = endpointID
		}
	}
	return nil
}

func (applier *migrationApplier) insertPublishedModelsAndRoutes() error {
	routeTargetsByModel := make(map[int64][]migrationRouteTarget)
	for _, target := range applier.source.routeTargets {
		routeTargetsByModel[target.publishedModelID] = append(routeTargetsByModel[target.publishedModelID], target)
	}
	for _, model := range applier.source.publishedModels {
		modelID, err := applier.insertPublishedModel(model.name, applier.priceSKUs[model.name], model.enabled,
			model.revision, model.createdAt, model.updatedAt)
		if err != nil {
			return fmt.Errorf("migrate published model %d: %w", model.id, err)
		}
		applier.publishedModelMap[model.id] = modelID
		if model.monitorEnabled {
			interval := applier.probeIntervalMS()
			now := applier.nowMS()
			if _, err := applier.tx.ExecContext(applier.ctx, `INSERT INTO model_monitor_settings(
published_model_id,enabled,interval_ms,history_limit,next_probe_at,revision,created_at,updated_at)
VALUES (?,1,?,288,?,1,?,?)`, modelID, interval, now, now, now); err != nil {
				return fmt.Errorf("migrate monitor selection for model %d: %w", model.id, err)
			}
		}
		position := 0
		seenProvider := make(map[int64]bool)
		for _, target := range routeTargetsByModel[model.id] {
			if !target.enabled {
				continue
			}
			for _, surface := range orderedSurfaceNames() {
				providerID := applier.siteModelMap[endpointSurfaceKey{sourceID: target.siteModelID, surface: surface}]
				if providerID <= 0 || seenProvider[providerID] {
					continue
				}
				publishedTargetID, err := applier.insertPublishedTarget(modelID, providerID, position, target.revision,
					target.createdAt, target.updatedAt)
				if err != nil {
					return fmt.Errorf("migrate route target %d: %w", target.id, err)
				}
				applier.publishedTargetMap[publishedSurfaceKey{sourceTargetID: target.id, surface: surface}] = publishedTargetID
				seenProvider[providerID] = true
				position++
			}
		}
	}
	legacyRouteTargets := make(map[int64][]migrationLegacyRouteTarget)
	for _, target := range applier.source.legacyRouteTargets {
		legacyRouteTargets[target.routeID] = append(legacyRouteTargets[target.routeID], target)
	}
	legacyModels := make(map[int64]migrationLegacyModel, len(applier.source.legacyModels))
	legacyEndpoints := make(map[int64]migrationLegacyEndpoint, len(applier.source.legacyEndpoints))
	legacyUpstreams := make(map[int64]migrationLegacyUpstream, len(applier.source.legacyUpstreams))
	for _, item := range applier.source.legacyModels {
		legacyModels[item.id] = item
	}
	for _, item := range applier.source.legacyEndpoints {
		legacyEndpoints[item.id] = item
	}
	for _, item := range applier.source.legacyUpstreams {
		legacyUpstreams[item.id] = item
	}
	for _, route := range applier.source.legacyRoutes {
		if _, shadowed := applier.publishedModelByName[route.name]; shadowed {
			continue
		}
		modelID, err := applier.insertPublishedModel(route.name, applier.priceSKUs[route.name], route.enabled,
			route.revision, route.createdAt, route.updatedAt)
		if err != nil {
			return fmt.Errorf("migrate legacy route %d: %w", route.id, err)
		}
		position := 0
		seenProvider := make(map[int64]bool)
		for _, target := range legacyRouteTargets[route.id] {
			if !target.enabled {
				continue
			}
			legacyModel, ok := legacyModels[target.modelID]
			if !ok {
				return fmt.Errorf("legacy route target %d references missing model %d", target.id, target.modelID)
			}
			legacyEndpoint, ok := legacyEndpoints[target.endpointID]
			if !ok || legacyEndpoint.upstreamID != legacyModel.upstreamID {
				return fmt.Errorf("legacy route target %d has a missing or mismatched endpoint", target.id)
			}
			legacyUpstream, ok := legacyUpstreams[legacyModel.upstreamID]
			if !ok {
				return fmt.Errorf("legacy route target %d references missing upstream", target.id)
			}
			credentialID := applier.legacyCredentialMap[target.credentialID]
			if credentialID <= 0 {
				return fmt.Errorf("legacy route target %d references missing credential %d", target.id, target.credentialID)
			}
			pairs, err := mapProtocolSurfaces(legacyUpstream.protocol, applier.options.OpenAISurface)
			if err != nil {
				return err
			}
			sourceModel := strings.TrimSpace(target.sourceModelOverride)
			if sourceModel == "" {
				sourceModel = legacyModel.name
			}
			for _, pair := range pairs {
				endpointID := applier.legacyEndpointMap[endpointSurfaceKey{sourceID: target.endpointID, surface: pair.surface}]
				if endpointID <= 0 {
					return fmt.Errorf("legacy route target %d has no migrated endpoint for %s", target.id, pair.surface)
				}
				applier.addEndpointBinding(endpointID, credentialID)
				providerID, err := applier.insertOrReuseProviderTarget(endpointID, applier.legacySiteMap[legacyModel.upstreamID],
					sourceModel, "", legacyModel.enabled, legacyModel.lastSeenAt, 1, target.createdAt, target.updatedAt)
				if err != nil {
					return err
				}
				if !applier.v3Provider[providerID] {
					if applier.legacyExclusive[providerID] == nil {
						applier.legacyExclusive[providerID] = make(map[int64]bool)
					}
					applier.legacyExclusive[providerID][credentialID] = true
				}
				if seenProvider[providerID] {
					continue
				}
				if _, err := applier.insertPublishedTarget(modelID, providerID, position, 1, target.createdAt, target.updatedAt); err != nil {
					return fmt.Errorf("migrate legacy route target %d: %w", target.id, err)
				}
				seenProvider[providerID] = true
				position++
			}
		}
	}
	return nil
}

func (applier *migrationApplier) insertPublishedModel(
	name, priceSKU string,
	enabled bool,
	revision, createdAt, updatedAt int64,
) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.TrimSpace(priceSKU) == "" {
		return 0, errors.New("published model name and official price SKU are required")
	}
	result, err := applier.tx.ExecContext(applier.ctx, `INSERT INTO published_models(
public_name,official_price_sku,enabled,revision,created_at,updated_at) VALUES (?,?,?,?,?,?)`,
		name, priceSKU, boolIntMigration(enabled), positiveRevision(revision), nonNegativeTime(createdAt), nonNegativeTime(updatedAt))
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	applier.publishedModelByName[name] = id
	applier.result.PublishedModels++
	return id, nil
}

func (applier *migrationApplier) insertPublishedTarget(
	modelID, providerID int64,
	position int,
	revision, createdAt, updatedAt int64,
) (int64, error) {
	result, err := applier.tx.ExecContext(applier.ctx, `INSERT INTO published_model_targets(
published_model_id,provider_model_target_id,position,revision,created_at,updated_at) VALUES (?,?,?,?,?,?)`,
		modelID, providerID, position, positiveRevision(revision), nonNegativeTime(createdAt), nonNegativeTime(updatedAt))
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err == nil {
		applier.result.PublishedModelTargets++
	}
	return id, err
}

func (applier *migrationApplier) persistEndpointBindings() error {
	for endpointID, credentials := range applier.endpointBindings {
		var siteID int64
		if err := applier.tx.QueryRowContext(applier.ctx, `SELECT site_id FROM site_endpoints WHERE id=?`, endpointID).Scan(&siteID); err != nil {
			return err
		}
		for position, credentialID := range credentials {
			if _, err := applier.tx.ExecContext(applier.ctx, `INSERT INTO credential_endpoint_bindings(
site_id,endpoint_id,credential_id,position,enabled,created_at,updated_at) VALUES (?,?,?,?,1,?,?)`,
				siteID, endpointID, credentialID, position, applier.nowMS(), applier.nowMS()); err != nil {
				return fmt.Errorf("migrate endpoint %d credential binding: %w", endpointID, err)
			}
		}
	}
	return nil
}

func (applier *migrationApplier) persistCredentialAccess() error {
	for _, source := range applier.source.credentialAccess {
		credentialID := applier.credentialMap[source.credentialID]
		if credentialID <= 0 {
			return fmt.Errorf("credential access references missing credential %d", source.credentialID)
		}
		for _, surface := range orderedSurfaceNames() {
			providerID := applier.siteModelMap[endpointSurfaceKey{sourceID: source.siteModelID, surface: surface}]
			if providerID <= 0 {
				continue
			}
			availability := normalizeAvailability(source.availability)
			applier.pendingAccess[[2]int64{credentialID, providerID}] = pendingAccess{
				siteID: applier.siteMap[source.siteID], endpointID: applier.providerEndpoint[providerID],
				credentialID: credentialID, providerID: providerID, availability: availability,
				lastChecked: source.lastChecked, revision: source.revision, updatedAt: source.updatedAt,
			}
		}
	}
	for providerID, allowed := range applier.legacyExclusive {
		endpointID := applier.providerEndpoint[providerID]
		var siteID int64
		if err := applier.tx.QueryRowContext(applier.ctx, `SELECT site_id FROM provider_model_targets WHERE id=?`, providerID).Scan(&siteID); err != nil {
			return err
		}
		for _, credentialID := range applier.endpointBindings[endpointID] {
			availability := "unsupported"
			if allowed[credentialID] {
				availability = "supported"
			}
			key := [2]int64{credentialID, providerID}
			if _, exists := applier.pendingAccess[key]; !exists {
				applier.pendingAccess[key] = pendingAccess{siteID: siteID, endpointID: endpointID,
					credentialID: credentialID, providerID: providerID, availability: availability,
					revision: 1, updatedAt: applier.nowMS()}
			}
		}
	}
	keys := make([][2]int64, 0, len(applier.pendingAccess))
	for key := range applier.pendingAccess {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	for _, key := range keys {
		item := applier.pendingAccess[key]
		if _, err := applier.tx.ExecContext(applier.ctx, `INSERT INTO credential_target_access(
site_id,endpoint_id,credential_id,provider_model_target_id,availability,last_http_status,last_error_code,
last_checked_at,revision,updated_at) VALUES (?,?,?,?,?,NULL,NULL,?,?,?)`, item.siteID, item.endpointID,
			item.credentialID, item.providerID, item.availability, item.lastChecked,
			positiveRevision(item.revision), nonNegativeTime(item.updatedAt)); err != nil {
			return fmt.Errorf("migrate credential/model access: %w", err)
		}
	}
	return nil
}

func (applier *migrationApplier) insertRoutingProfiles() error {
	defaultName := strings.ToLower("Default")
	applier.usedProfileNames[defaultName] = true
	for _, source := range applier.source.profiles {
		name := applier.uniqueProfileName(source.name, source.id)
		result, err := applier.tx.ExecContext(applier.ctx, `INSERT INTO routing_profiles(
name,is_default,revision,created_at,updated_at) VALUES (?,0,?,?,?)`, name, positiveRevision(source.revision),
			nonNegativeTime(source.createdAt), nonNegativeTime(source.updatedAt))
		if err != nil {
			return fmt.Errorf("migrate routing profile %d: %w", source.id, err)
		}
		id, _ := result.LastInsertId()
		applier.profileMap[source.id] = id
		applier.result.RoutingProfiles++
	}
	groups := make(map[[2]int64][]migrationProfileTarget)
	for _, target := range applier.source.profileTargets {
		groups[[2]int64{target.profileID, target.publishedModelID}] = append(groups[[2]int64{target.profileID, target.publishedModelID}], target)
	}
	groupKeys := make([][2]int64, 0, len(groups))
	for key := range groups {
		groupKeys = append(groupKeys, key)
	}
	sort.Slice(groupKeys, func(i, j int) bool {
		if groupKeys[i][0] != groupKeys[j][0] {
			return groupKeys[i][0] < groupKeys[j][0]
		}
		return groupKeys[i][1] < groupKeys[j][1]
	})
	for _, key := range groupKeys {
		profileID := applier.profileMap[key[0]]
		modelID := applier.publishedModelMap[key[1]]
		if profileID <= 0 || modelID <= 0 {
			return fmt.Errorf("routing profile override references missing profile/model %d/%d", key[0], key[1])
		}
		createdAt, updatedAt := applier.nowMS(), applier.nowMS()
		if len(groups[key]) > 0 {
			createdAt = nonNegativeTime(groups[key][0].createdAt)
			updatedAt = nonNegativeTime(groups[key][0].updatedAt)
		}
		if _, err := applier.tx.ExecContext(applier.ctx, `INSERT INTO routing_profile_model_routes(
routing_profile_id,published_model_id,enabled,targets_overridden,revision,created_at,updated_at)
VALUES (?,?,1,1,1,?,?)`, profileID, modelID, createdAt, updatedAt); err != nil {
			return err
		}
		position := 0
		seen := make(map[int64]bool)
		for _, sourceTarget := range groups[key] {
			for _, surface := range orderedSurfaceNames() {
				publishedTargetID := applier.publishedTargetMap[publishedSurfaceKey{sourceTargetID: sourceTarget.routeTargetID, surface: surface}]
				if publishedTargetID <= 0 || seen[publishedTargetID] {
					continue
				}
				if _, err := applier.tx.ExecContext(applier.ctx, `INSERT INTO routing_profile_route_targets(
routing_profile_id,published_model_id,published_model_target_id,position,created_at,updated_at) VALUES (?,?,?,?,?,?)`,
					profileID, modelID, publishedTargetID, position, nonNegativeTime(sourceTarget.createdAt), nonNegativeTime(sourceTarget.updatedAt)); err != nil {
					return err
				}
				seen[publishedTargetID] = true
				position++
			}
		}
	}
	return nil
}

func (applier *migrationApplier) insertDownstreamKeys() error {
	modelNames := make([]string, 0, len(applier.publishedModelByName))
	for name := range applier.publishedModelByName {
		modelNames = append(modelNames, name)
	}
	sort.Slice(modelNames, func(i, j int) bool { return strings.ToLower(modelNames[i]) < strings.ToLower(modelNames[j]) })
	for _, source := range applier.source.downstreamKeys {
		if len(source.digest) != sha256.Size {
			return fmt.Errorf("downstream key %d has digest length %d, want %d", source.id, len(source.digest), sha256.Size)
		}
		baseProfileID := int64(0)
		if source.profileID != nil {
			baseProfileID = applier.profileMap[*source.profileID]
		}
		allowedModels, valid, _ := parseAllowedModels(source.allowedModelsRaw)
		profileID := baseProfileID
		if valid && len(allowedModels) > 0 {
			var err error
			profileID, err = applier.createAllowlistProfile(source, baseProfileID, allowedModels, modelNames)
			if err != nil {
				return err
			}
		}
		quota, err := microToNanoPointer(source.quotaMicroUSD)
		if err != nil {
			return fmt.Errorf("downstream key %d quota: %w", source.id, err)
		}
		used, err := microToNano(source.usedMicroUSD)
		if err != nil {
			return fmt.Errorf("downstream key %d used amount: %w", source.id, err)
		}
		reserved, err := microToNano(source.reservedMicroUSD)
		if err != nil {
			return fmt.Errorf("downstream key %d reserved amount: %w", source.id, err)
		}
		if quota != nil && (used > *quota || reserved > *quota-used) {
			return fmt.Errorf("downstream key %d has an invalid quota state", source.id)
		}
		name := applier.uniqueKeyName(source.name, source.id)
		var storedProfile any
		if profileID > 0 {
			storedProfile = profileID
		}
		if _, err := applier.tx.ExecContext(applier.ctx, `INSERT INTO downstream_keys(
name,key_prefix,key_digest,encrypted_secret,reveal_version,routing_profile_id,enabled,quota_nano_usd,
used_nano_usd,reserved_nano_usd,rpm_limit,expires_at,last_used_at,revision,created_at,updated_at)
VALUES (?,?,?,NULL,0,?,?,?,?,?,?,?,?,1,?,?)`, name, strings.TrimSpace(source.prefix), source.digest,
			storedProfile, boolIntMigration(source.enabled), quota, used, reserved, maxInt(source.rpmLimit, 0),
			source.expiresAt, source.lastUsedAt, nonNegativeTime(source.createdAt), nonNegativeTime(source.updatedAt)); err != nil {
			return fmt.Errorf("migrate downstream key %d: %w", source.id, err)
		}
		applier.result.DownstreamKeys++
		applier.result.NonRevealableDownstreamKeys++
	}
	return nil
}

func (applier *migrationApplier) createAllowlistProfile(
	key migrationDownstreamKey,
	baseProfileID int64,
	allowed []string,
	modelNames []string,
) (int64, error) {
	name := applier.uniqueProfileName("Key "+key.name+" allowlist", key.id)
	now := applier.nowMS()
	result, err := applier.tx.ExecContext(applier.ctx, `INSERT INTO routing_profiles(
name,is_default,revision,created_at,updated_at) VALUES (?,0,1,?,?)`, name, now, now)
	if err != nil {
		return 0, err
	}
	profileID, _ := result.LastInsertId()
	applier.result.RoutingProfiles++
	if baseProfileID > 0 {
		if _, err := applier.tx.ExecContext(applier.ctx, `INSERT INTO routing_profile_model_routes(
routing_profile_id,published_model_id,enabled,targets_overridden,revision,created_at,updated_at)
SELECT ?,published_model_id,enabled,targets_overridden,revision,created_at,updated_at
FROM routing_profile_model_routes WHERE routing_profile_id=?`, profileID, baseProfileID); err != nil {
			return 0, err
		}
		if _, err := applier.tx.ExecContext(applier.ctx, `INSERT INTO routing_profile_route_targets(
routing_profile_id,published_model_id,published_model_target_id,position,created_at,updated_at)
SELECT ?,published_model_id,published_model_target_id,position,created_at,updated_at
FROM routing_profile_route_targets WHERE routing_profile_id=?`, profileID, baseProfileID); err != nil {
			return 0, err
		}
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, model := range allowed {
		allowedSet[model] = true
	}
	for _, modelName := range modelNames {
		if allowedSet[modelName] {
			continue
		}
		modelID := applier.publishedModelByName[modelName]
		if _, err := applier.tx.ExecContext(applier.ctx, `INSERT INTO routing_profile_model_routes(
routing_profile_id,published_model_id,enabled,targets_overridden,revision,created_at,updated_at)
VALUES (?,?,0,0,1,?,?)
ON CONFLICT(routing_profile_id,published_model_id) DO UPDATE SET enabled=0,targets_overridden=0,updated_at=excluded.updated_at`,
			profileID, modelID, now, now); err != nil {
			return 0, err
		}
		if _, err := applier.tx.ExecContext(applier.ctx, `DELETE FROM routing_profile_route_targets
WHERE routing_profile_id=? AND published_model_id=?`, profileID, modelID); err != nil {
			return 0, err
		}
	}
	return profileID, nil
}

func (applier *migrationApplier) insertAccounts() error {
	for _, source := range applier.source.accounts {
		siteID := applier.siteMap[source.siteID]
		if siteID <= 0 {
			return fmt.Errorf("site account %d references missing site %d", source.id, source.siteID)
		}
		if existing := applier.accountBySite[siteID]; existing > 0 {
			return fmt.Errorf("site account %d conflicts with another account for migrated site %d", source.id, siteID)
		}
		connectionID, err := applier.insertAccountConnection(migrationAccountConnectionInput{
			sourceKind: "site account", sourceID: source.id, siteID: siteID,
			adapterKind: source.adapterKind, origin: source.origin, authCipher: source.authCipher,
			enabled: source.enabled, lastAttempt: source.lastAttemptAt, lastSuccess: source.lastSuccessAt,
			lastError: source.lastErrorCode, createdAt: source.createdAt, updatedAt: source.updatedAt,
		})
		if err != nil {
			return err
		}
		applier.accountMap[source.id] = connectionID
	}
	for _, source := range applier.source.legacyAccounts {
		siteID := applier.legacySiteMap[source.upstreamID]
		if siteID <= 0 {
			return fmt.Errorf("legacy upstream account %d references missing upstream %d", source.id, source.upstreamID)
		}
		if existing := applier.accountBySite[siteID]; existing > 0 {
			applier.legacyAccountMap[source.id] = existing
			applier.result.Warnings = append(applier.result.Warnings, fmt.Sprintf(
				"legacy upstream account %d reused the newer site account connection for upstream %d; its older credentials were not imported",
				source.id, source.upstreamID))
			continue
		}
		connectionID, err := applier.insertAccountConnection(migrationAccountConnectionInput{
			sourceKind: "legacy upstream account", sourceID: source.id, siteID: siteID,
			adapterKind: source.adapterKind, origin: source.origin, authCipher: source.authCipher,
			enabled: source.enabled, lastAttempt: source.lastAttemptAt, lastSuccess: source.lastSuccessAt,
			lastError: source.lastErrorCode, createdAt: source.createdAt, updatedAt: source.updatedAt,
		})
		if err != nil {
			return err
		}
		applier.legacyAccountMap[source.id] = connectionID
	}
	if err := applier.insertAccountSnapshots("site account", applier.accountMap, applier.source.accountSnapshots); err != nil {
		return err
	}
	if err := applier.insertAccountUsage("site account", applier.accountMap, applier.source.accountUsage); err != nil {
		return err
	}
	if err := applier.insertAccountSnapshots("legacy upstream account", applier.legacyAccountMap, applier.source.legacyAccountSnapshots); err != nil {
		return err
	}
	return applier.insertAccountUsage("legacy upstream account", applier.legacyAccountMap, applier.source.legacyAccountUsage)
}

func (applier *migrationApplier) insertAccountConnection(input migrationAccountConnectionInput) (int64, error) {
	adapterKind := strings.ToLower(strings.TrimSpace(input.adapterKind))
	origin := strings.TrimRight(strings.TrimSpace(input.origin), "/")
	if input.siteID <= 0 || adapterKind == "" || origin == "" {
		return 0, fmt.Errorf("%s %d has an invalid site, adapter, or origin", input.sourceKind, input.sourceID)
	}
	plaintext, err := applier.legacyCipher.open(input.authCipher)
	if err != nil {
		return 0, fmt.Errorf("decrypt %s %d: %w", input.sourceKind, input.sourceID, err)
	}
	converted, convertErr := convertLegacyAccountSecret(plaintext)
	clearBytes(plaintext)
	if convertErr != nil {
		return 0, fmt.Errorf("convert %s %d: %w", input.sourceKind, input.sourceID, convertErr)
	}
	lastSession, lastBalance, lastUsage := input.lastSuccess, input.lastSuccess, input.lastSuccess
	var lastOperation, lastCode any
	var lastErrorAt *int64
	if strings.TrimSpace(input.lastError) != "" && input.lastAttempt != nil {
		lastOperation = "legacy_sync"
		lastCode = strings.TrimSpace(input.lastError)
		lastErrorAt = input.lastAttempt
	}
	result, err := applier.tx.ExecContext(applier.ctx, `INSERT INTO site_account_connections(
site_id,adapter_kind,origin,secrets_cipher,cipher_version,enabled,last_session_refresh_at,last_balance_refresh_at,
last_usage_refresh_at,last_error_operation,last_error_code,last_error_at,revision,created_at,updated_at)
VALUES (?,?,?,X'00',?,?,?,?,?,?,?,?,1,?,?)`, input.siteID, adapterKind, origin, migratedCipherVersion,
		boolIntMigration(input.enabled), lastSession, lastBalance, lastUsage, lastOperation, lastCode, lastErrorAt,
		nonNegativeTime(input.createdAt), nonNegativeTime(input.updatedAt))
	if err != nil {
		clearBytes(converted)
		return 0, fmt.Errorf("migrate %s %d: %w", input.sourceKind, input.sourceID, err)
	}
	connectionID, err := result.LastInsertId()
	if err != nil {
		clearBytes(converted)
		return 0, fmt.Errorf("read migrated %s %d id: %w", input.sourceKind, input.sourceID, err)
	}
	sealed, sealErr := applier.box.Seal(vnextsecretbox.PurposeSiteAdministration,
		vnextsecretbox.Identity{RecordID: connectionID, OwnerID: input.siteID}, converted)
	clearBytes(converted)
	if sealErr != nil {
		return 0, fmt.Errorf("seal migrated %s %d: %w", input.sourceKind, input.sourceID, sealErr)
	}
	update, err := applier.tx.ExecContext(applier.ctx, `UPDATE site_account_connections SET secrets_cipher=? WHERE id=?`, sealed, connectionID)
	clearBytes(sealed)
	if err != nil {
		return 0, fmt.Errorf("store migrated %s %d secret: %w", input.sourceKind, input.sourceID, err)
	}
	if changed, changedErr := update.RowsAffected(); changedErr != nil || changed != 1 {
		if changedErr != nil {
			return 0, changedErr
		}
		return 0, fmt.Errorf("store migrated %s %d secret: connection was not updated", input.sourceKind, input.sourceID)
	}
	applier.accountBySite[input.siteID] = connectionID
	applier.result.SiteAccountConnections++
	return connectionID, nil
}

func (applier *migrationApplier) insertAccountSnapshots(
	sourceKind string,
	accountMap map[int64]int64,
	sources []migrationAccountSnapshot,
) error {
	for _, source := range sources {
		connectionID := accountMap[source.accountID]
		if connectionID <= 0 {
			return fmt.Errorf("%s snapshot %d references missing account %d", sourceKind, source.id, source.accountID)
		}
		siteID, adapterKind, err := applier.accountIdentity(connectionID)
		if err != nil {
			return err
		}
		balance, ok, err := parseLegacyBalanceSnapshot(source.json)
		if err != nil {
			return fmt.Errorf("parse %s snapshot %d: %w", sourceKind, source.id, err)
		}
		if !ok {
			applier.result.Warnings = append(applier.result.Warnings,
				fmt.Sprintf("%s snapshot %d had no exact balance and was not imported", sourceKind, source.id))
			continue
		}
		if _, err := applier.tx.ExecContext(applier.ctx, `INSERT INTO site_balance_snapshots(
site_account_connection_id,site_id,adapter_kind,account_remote_id,account_name,available_value,available_unit,
used_value,used_unit,captured_at,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, connectionID, siteID, adapterKind,
			nullableText(balance.accountID), nullableText(balance.accountName), balance.availableValue, balance.availableUnit,
			balance.usedValue, balance.usedUnit, source.capturedAt, source.capturedAt); err != nil {
			return err
		}
		applier.result.BalanceSnapshots++
	}
	return nil
}

func (applier *migrationApplier) insertAccountUsage(
	sourceKind string,
	accountMap map[int64]int64,
	sources []migrationAccountUsage,
) error {
	for _, source := range sources {
		connectionID := accountMap[source.accountID]
		if connectionID <= 0 {
			return fmt.Errorf("%s usage %d references missing account %d", sourceKind, source.id, source.accountID)
		}
		siteID, adapterKind, err := applier.accountIdentity(connectionID)
		if err != nil {
			return err
		}
		usage, err := parseLegacyUsage(source)
		if err != nil {
			return fmt.Errorf("parse %s usage %d: %w", sourceKind, source.id, err)
		}
		dedupKey := strings.TrimSpace(source.dedupKey)
		if dedupKey == "" {
			return fmt.Errorf("%s usage %d has an empty dedup key", sourceKind, source.id)
		}
		insert, err := applier.tx.ExecContext(applier.ctx, `INSERT OR IGNORE INTO site_usage_records(
site_account_connection_id,site_id,adapter_kind,dedup_key,remote_id,request_id,upstream_request_id,occurred_at,
model,upstream_model,status,http_status,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,
reasoning_tokens,total_tokens,charge_value,charge_unit,duration_ms,api_key_name,source_fetched_at,created_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, connectionID, siteID, adapterKind, dedupKey,
			nullableText(source.externalID), nullableText(usage.requestID), nullableText(usage.upstreamRequestID), usage.occurredAt,
			nullableText(firstNonEmpty(source.modelName, usage.model)), nullableText(usage.upstreamModel), nullableText(usage.status),
			usage.httpStatus, usage.inputTokens, usage.outputTokens, usage.cacheReadTokens, usage.cacheWriteTokens,
			usage.reasoningTokens, usage.totalTokens, usage.chargeValue, usage.chargeUnit, usage.durationMS,
			nullableText(usage.apiKeyName), source.syncedAt, source.syncedAt)
		if err != nil {
			return fmt.Errorf("migrate %s usage %d: %w", sourceKind, source.id, err)
		}
		changed, err := insert.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 1 {
			applier.result.SiteUsageRecords++
		}
	}
	return nil
}

func (applier *migrationApplier) accountIdentity(connectionID int64) (int64, string, error) {
	var siteID int64
	var adapter string
	if err := applier.tx.QueryRowContext(applier.ctx, `SELECT site_id,adapter_kind FROM site_account_connections WHERE id=?`, connectionID).
		Scan(&siteID, &adapter); err != nil {
		return 0, "", err
	}
	return siteID, adapter, nil
}

func (applier *migrationApplier) validateDestination() error {
	checks := []struct {
		table string
		want  int
	}{
		{"downstream_keys", len(applier.source.downstreamKeys)},
		{"site_account_connections", len(applier.accountBySite)},
		{"site_balance_snapshots", applier.result.BalanceSnapshots},
		{"site_usage_records", applier.result.SiteUsageRecords},
	}
	for _, check := range checks {
		var count int
		if err := applier.tx.QueryRowContext(applier.ctx, "SELECT COUNT(*) FROM "+check.table).Scan(&count); err != nil {
			return err
		}
		if count != check.want {
			return fmt.Errorf("destination validation failed for %s: got %d rows, want %d", check.table, count, check.want)
		}
	}
	if err := applier.tx.QueryRowContext(applier.ctx, `PRAGMA foreign_key_check`).Scan(new(any)); err != sql.ErrNoRows {
		if err == nil {
			return errors.New("destination foreign key check reported a violation")
		}
		return fmt.Errorf("destination foreign key check: %w", err)
	}
	return nil
}

func (applier *migrationApplier) addEndpointBinding(endpointID, credentialID int64) {
	if endpointID <= 0 || credentialID <= 0 {
		return
	}
	if applier.endpointBindingSet[endpointID] == nil {
		applier.endpointBindingSet[endpointID] = make(map[int64]bool)
	}
	if applier.endpointBindingSet[endpointID][credentialID] {
		return
	}
	applier.endpointBindingSet[endpointID][credentialID] = true
	applier.endpointBindings[endpointID] = append(applier.endpointBindings[endpointID], credentialID)
}

func (applier *migrationApplier) nowMS() int64 { return migrationNowMS(applier.options.NowMS) }

func (applier *migrationApplier) probeIntervalMS() int64 {
	if applier.source.runtimeSettings != nil {
		return applier.source.runtimeSettings.probeIntervalSeconds * 1000
	}
	return vnextstore.DefaultProbeInterval.Milliseconds()
}

func (applier *migrationApplier) uniqueSiteName(name string, legacyID int64) string {
	name = fallbackName(name, fmt.Sprintf("Migrated site %d", legacyID))
	return uniqueName(name, fmt.Sprintf("legacy %d", legacyID), applier.usedSiteNames)
}

func (applier *migrationApplier) uniqueCredentialName(siteID int64, name string) string {
	if applier.usedCredentialNames[siteID] == nil {
		applier.usedCredentialNames[siteID] = make(map[string]bool)
	}
	return uniqueName(fallbackName(name, "Default"), "migrated", applier.usedCredentialNames[siteID])
}

func (applier *migrationApplier) uniqueProfileName(name string, legacyID int64) string {
	return uniqueName(fallbackName(name, fmt.Sprintf("Migrated profile %d", legacyID)),
		fmt.Sprintf("legacy %d", legacyID), applier.usedProfileNames)
}

func (applier *migrationApplier) uniqueKeyName(name string, legacyID int64) string {
	return uniqueName(fallbackName(name, fmt.Sprintf("Migrated key %d", legacyID)),
		fmt.Sprintf("legacy %d", legacyID), applier.usedKeyNames)
}

func uniqueName(name, suffix string, used map[string]bool) string {
	name = strings.TrimSpace(name)
	key := strings.ToLower(name)
	if !used[key] {
		used[key] = true
		return name
	}
	base := name + " (" + suffix + ")"
	for index := 1; ; index++ {
		candidate := base
		if index > 1 {
			candidate = fmt.Sprintf("%s %d", base, index)
		}
		key = strings.ToLower(candidate)
		if !used[key] {
			used[key] = true
			return candidate
		}
	}
}

func orderedSurfaceNames() []string {
	return []string{
		string(vnextprotocol.OpenAIChatCompletions), string(vnextprotocol.OpenAIResponses),
		string(vnextprotocol.AnthropicMessages), string(vnextprotocol.GeminiGenerateContent),
	}
}

func normalizeMigrationAuthScheme(protocol, source string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(source))
	switch value {
	case "":
		parsed, err := vnextprotocol.ParseProtocol(protocol)
		if err != nil {
			return "", err
		}
		result, err := vnextprotocol.DefaultAuthScheme(parsed)
		return string(result), err
	case "bearer":
		return string(vnextprotocol.AuthBearer), nil
	case "x-api-key", "x_api_key", "api-key", "api_key":
		return string(vnextprotocol.AuthXAPIKey), nil
	case "x-goog-api-key", "x_goog_api_key":
		return string(vnextprotocol.AuthXGoogAPIKey), nil
	case "query-key", "query_key":
		return string(vnextprotocol.AuthQueryKey), nil
	default:
		return "", fmt.Errorf("unsupported legacy auth scheme %q", source)
	}
}

func splitMigrationHeaders(raw string) (string, []byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "{}"
	}
	var decoded any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return "", nil, errors.New("custom headers must be a JSON object")
	}
	headers, ok := decoded.(map[string]any)
	if !ok {
		if decoded == nil {
			headers = map[string]any{}
		} else if list, isList := decoded.([]any); isList && len(list) == 0 {
			headers = map[string]any{}
		} else {
			return "", nil, errors.New("custom headers must be a JSON object")
		}
	}
	publicHeaders := make(map[string]any)
	secretHeaders := make(map[string]any)
	for name, value := range headers {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "api-key", "x-goog-api-key":
			secretHeaders[name] = value
		default:
			publicHeaders[name] = value
		}
	}
	publicJSON, err := json.Marshal(publicHeaders)
	if err != nil {
		return "", nil, err
	}
	if len(secretHeaders) == 0 {
		return string(publicJSON), nil, nil
	}
	secretJSON, err := json.Marshal(secretHeaders)
	return string(publicJSON), secretJSON, err
}

func validateMigrationBaseURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("base URL must be absolute HTTP(S) without credentials, query, or fragment")
	}
	return nil
}

func migrateCredentialState(source string, cooldownUntil *int64, nowMS int64) (string, any, any, string) {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "invalid", "revoked":
		return "invalid", nil, 401, "migrated_invalid"
	case "exhausted":
		return "exhausted", nil, 402, "migrated_exhausted"
	case "rate_limited", "cooling":
		if cooldownUntil != nil && *cooldownUntil > nowMS {
			return "cooling", *cooldownUntil, 429, "migrated_rate_limit"
		}
	}
	return "active", nil, nil, ""
}

func normalizeAvailability(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "supported":
		return "supported"
	case "unsupported":
		return "unsupported"
	case "forbidden":
		return "forbidden"
	default:
		return "unknown"
	}
}

func microToNano(value int64) (int64, error) {
	if value < 0 {
		return 0, errors.New("value cannot be negative")
	}
	if value > math.MaxInt64/1000 {
		return 0, errors.New("value overflows nano-USD")
	}
	return value * 1000, nil
}

func microToNanoPointer(value *int64) (*int64, error) {
	if value == nil {
		return nil, nil
	}
	converted, err := microToNano(*value)
	if err != nil {
		return nil, err
	}
	return &converted, nil
}

type migratedBalance struct {
	accountID, accountName, availableValue, availableUnit string
	usedValue, usedUnit                                   *string
}

func parseLegacyBalanceSnapshot(raw string) (migratedBalance, bool, error) {
	var root map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return migratedBalance{}, false, err
	}
	account := root
	if nested, ok := root["account"].(map[string]any); ok {
		account = nested
	}
	available := firstString(account, "balance", "available", "available_value")
	if available == "" {
		return migratedBalance{}, false, nil
	}
	if _, err := strconv.ParseFloat(available, 64); err != nil {
		return migratedBalance{}, false, errors.New("balance is not an exact decimal")
	}
	unit := firstString(account, "currency", "unit")
	if unit == "" {
		unit = "USD"
	}
	result := migratedBalance{
		accountID: firstString(account, "account_id", "id"), accountName: firstString(account, "username", "name", "email"),
		availableValue: available, availableUnit: unit,
	}
	if used := firstString(account, "used", "used_value"); used != "" {
		result.usedValue = &used
		usedUnit := unit
		result.usedUnit = &usedUnit
	}
	return result, true, nil
}

type migratedUsage struct {
	requestID, upstreamRequestID, model, upstreamModel, status, apiKeyName                     string
	occurredAt                                                                                 int64
	httpStatus                                                                                 *int
	inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens, reasoningTokens, totalTokens *int64
	chargeValue, chargeUnit                                                                    *string
	durationMS                                                                                 *int64
}

func parseLegacyUsage(source migrationAccountUsage) (migratedUsage, error) {
	var raw map[string]any
	decoder := json.NewDecoder(strings.NewReader(source.rawJSON))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return migratedUsage{}, err
	}
	occurredAt := source.syncedAt
	if source.occurredAt != nil {
		occurredAt = *source.occurredAt
	}
	if occurredAt < 0 {
		return migratedUsage{}, errors.New("occurredAt cannot be negative")
	}
	usage := migratedUsage{
		requestID: firstString(raw, "request_id", "requestId"), upstreamRequestID: firstString(raw, "upstream_request_id", "upstreamRequestId"),
		model: firstString(raw, "model"), upstreamModel: firstString(raw, "upstream_model", "upstreamModel"),
		status: firstString(raw, "status", "state"), apiKeyName: firstString(raw, "api_key_name", "apiKeyName", "token_name"), occurredAt: occurredAt,
	}
	usage.httpStatus = optionalInt(raw, "status_code", "http_status", "httpStatus")
	usage.inputTokens = optionalInt64(raw, "prompt_tokens", "input_tokens", "inputTokens")
	usage.outputTokens = optionalInt64(raw, "completion_tokens", "output_tokens", "outputTokens")
	usage.cacheReadTokens = optionalInt64(raw, "cache_read_tokens", "cacheReadTokens")
	usage.cacheWriteTokens = optionalInt64(raw, "cache_creation_tokens", "cache_write_tokens", "cacheWriteTokens")
	usage.reasoningTokens = optionalInt64(raw, "reasoning_tokens", "reasoningTokens")
	usage.totalTokens = optionalInt64(raw, "total_tokens", "totalTokens")
	usage.durationMS = optionalInt64(raw, "duration_ms", "durationMs", "latency_ms")
	charge := firstNonEmpty(firstString(raw, "actual_cost", "actualCost"), firstString(raw, "total_cost", "totalCost"), source.amount)
	if charge != "" {
		unit := firstNonEmpty(source.unit, firstString(raw, "currency", "unit"), "USD")
		usage.chargeValue = &charge
		usage.chargeUnit = &unit
	}
	return usage, nil
}

func optionalInt(values map[string]any, names ...string) *int {
	value := optionalInt64(values, names...)
	if value == nil || *value < 0 || *value > math.MaxInt32 {
		return nil
	}
	result := int(*value)
	return &result
}

func optionalInt64(values map[string]any, names ...string) *int64 {
	value, ok := firstValue(values, names...)
	if !ok {
		return nil
	}
	var result int64
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return nil
		}
		result = parsed
	case float64:
		if typed != float64(int64(typed)) {
			return nil
		}
		result = int64(typed)
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return nil
		}
		result = parsed
	default:
		return nil
	}
	if result < 0 {
		return nil
	}
	return &result
}

func positiveRevision(value int64) int64 {
	if value > 0 {
		return value
	}
	return 1
}

func nonNegativeTime(value int64) int64 {
	if value >= 0 {
		return value
	}
	return 0
}

func nullableText(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func boolIntMigration(value bool) int {
	if value {
		return 1
	}
	return 0
}

func fallbackName(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
