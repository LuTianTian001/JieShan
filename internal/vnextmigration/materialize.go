package vnextmigration

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
)

const (
	generationV3     = "v3"
	generationLegacy = "legacy"

	resolutionV3Profile                = "v3_profile"
	resolutionV3Default                = "v3_default"
	resolutionV3ProfileDefaultFallback = "v3_profile_default_fallback"
	resolutionV3MissingProfileFallback = "v3_missing_profile_default_fallback"
	resolutionLegacyGlobal             = "legacy_global"
)

func materializeReport(inventory sourceInventory, schema schemaInfo, nowMS int64) Report {
	report := Report{
		FormatVersion: ReportFormatVersion,
		GeneratedAtMS: nowMS,
		Source: SourceReport{
			SchemaVersion:      schema.version,
			HasLegacyRouting:   schema.hasTable("routes"),
			HasV3Routing:       schema.hasTable("published_models"),
			HasRoutingProfiles: schema.hasTable("routing_profiles") && schema.hasTable("routing_profile_model_targets"),
		},
		Keys:   make([]KeyReport, 0, len(inventory.keys)),
		Issues: append([]Issue(nil), inventory.issues...),
	}
	modelNames := orderedModelNames(inventory)
	for _, source := range inventory.keys {
		report.Keys = append(report.Keys, materializeKey(inventory, source, modelNames, nowMS))
	}
	report.Summary = summarize(report)
	return report
}

func materializeKey(inventory sourceInventory, source sourceKey, modelNames []string, nowMS int64) KeyReport {
	allowedModels, allowlistValid, allowlistIssue := parseAllowedModels(source.allowedModelsRaw)
	key := KeyReport{
		LegacyID:            source.id,
		Name:                source.name,
		Prefix:              source.prefix,
		Enabled:             source.enabled,
		ExpiresAtMS:         cloneInt64Pointer(source.expiresAt),
		AllowedModels:       allowedModels,
		AllowedModelsMode:   "all",
		RoutingProfileID:    cloneInt64Pointer(source.profileID),
		SecretRevealable:    false,
		NonRevealableReason: "the legacy database stores only a one-way downstream key digest",
		Models:              []ModelReport{},
		Issues: []Issue{{
			Code:     "downstream_key_not_revealable",
			Severity: SeverityInfo,
			Message:  "The existing key can remain valid by digest, but its plaintext cannot be recovered and must be rotated before reveal is possible.",
		}},
	}
	if allowlistIssue != nil {
		key.Issues = append(key.Issues, *allowlistIssue)
	}
	if allowlistValid && len(allowedModels) > 0 {
		key.AllowedModelsMode = "allowlist"
	}
	if source.profileID != nil {
		key.RoutingProfileName = inventory.profiles[*source.profileID]
		if key.RoutingProfileName == "" {
			key.Issues = append(key.Issues, Issue{
				Code:     "routing_profile_missing_fail_open",
				Severity: SeverityError,
				Message:  fmt.Sprintf("Routing profile %d is missing; the current runtime silently falls back to global default targets.", *source.profileID),
			})
		}
	}

	allowedSet := make(map[string]struct{}, len(allowedModels))
	seenAllowed := make(map[string]struct{}, len(allowedModels))
	for _, model := range allowedModels {
		allowedSet[model] = struct{}{}
		if _, duplicate := seenAllowed[model]; duplicate {
			key.Issues = append(key.Issues, Issue{Code: "duplicate_allowed_model", Severity: SeverityWarning,
				Message: fmt.Sprintf("allowedModels contains duplicate entry %q.", model)})
		}
		seenAllowed[model] = struct{}{}
	}

	knownNames := make(map[string]struct{}, len(modelNames))
	for _, name := range modelNames {
		knownNames[name] = struct{}{}
		v3Model, hasV3 := inventory.v3Models[name]
		legacyRoute, hasLegacy := inventory.legacyRoutes[name]
		generation := generationLegacy
		enabled := hasLegacy && legacyRoute.enabled
		if hasV3 {
			generation = generationV3
			enabled = v3Model.enabled
		}
		reasons := make([]string, 0, 2)
		if allowlistValid && len(allowedSet) > 0 {
			if _, allowed := allowedSet[name]; !allowed {
				reasons = append(reasons, "not_allowed")
			}
		}
		if !enabled {
			reasons = append(reasons, "disabled")
			if hasV3 && hasLegacy && legacyRoute.enabled {
				reasons = append(reasons, "v3_disabled_blocks_legacy")
			}
		}
		if len(reasons) > 0 {
			key.ExcludedModels = append(key.ExcludedModels, ExcludedModel{PublicName: name, Generation: generation, Reasons: reasons})
			continue
		}
		if hasV3 {
			key.Models = append(key.Models, materializeV3Model(inventory, source, v3Model, legacyRoute, hasLegacy, nowMS))
			continue
		}
		key.Models = append(key.Models, materializeLegacyModel(inventory, legacyRoute))
	}
	if allowlistValid && len(allowedSet) > 0 {
		for _, allowed := range allowedModels {
			if _, known := knownNames[allowed]; !known {
				key.UnresolvedAllowlist = append(key.UnresolvedAllowlist, allowed)
			}
		}
		if len(key.UnresolvedAllowlist) > 0 {
			key.Issues = append(key.Issues, Issue{
				Code:     "allowed_models_unresolved",
				Severity: SeverityWarning,
				Message:  fmt.Sprintf("allowedModels contains %d name(s) that do not resolve to V3 or legacy models.", len(key.UnresolvedAllowlist)),
			})
		}
	}
	return key
}

func materializeV3Model(inventory sourceInventory, key sourceKey, model sourceV3Model, legacy sourceLegacyRoute, hasLegacy bool, nowMS int64) ModelReport {
	modelID := model.id
	report := ModelReport{
		PublicName:        model.name,
		Generation:        generationV3,
		PublishedModelID:  &modelID,
		Revision:          model.revision,
		Enabled:           model.enabled,
		ExplicitPriceSKU:  strings.TrimSpace(model.priceSKU),
		EffectivePriceSKU: model.name,
		Targets:           []TargetReport{},
	}
	if report.ExplicitPriceSKU != "" {
		report.EffectivePriceSKU = report.ExplicitPriceSKU
	} else {
		report.ExplicitPriceSKUMissing = true
		report.Issues = append(report.Issues, Issue{
			Code:     "price_sku_missing",
			Severity: SeverityWarning,
			Message:  "No explicit official price SKU is configured; the current runtime falls back to the public model name.",
		})
	}
	if hasLegacy {
		legacyID := legacy.id
		report.ShadowedLegacyRouteID = &legacyID
		report.Issues = append(report.Issues, Issue{
			Code:     "legacy_route_shadowed_by_v3",
			Severity: SeverityInfo,
			Message:  fmt.Sprintf("Legacy route %d has the same name and is unreachable while this V3 model exists.", legacy.id),
		})
	}

	selected := make([]sourceProfileTarget, 0)
	if key.profileID == nil {
		report.ResolutionSource = resolutionV3Default
		report.ImplicitInheritance = true
		selected = defaultV3Targets(inventory, model.id)
	} else if profileName, exists := inventory.profiles[*key.profileID]; !exists {
		report.ResolutionSource = resolutionV3MissingProfileFallback
		report.ImplicitInheritance = true
		selected = defaultV3Targets(inventory, model.id)
		report.Issues = append(report.Issues, Issue{
			Code:     "routing_profile_missing_fail_open",
			Severity: SeverityError,
			Message:  fmt.Sprintf("Missing routing profile %d causes this model to inherit every enabled default target.", *key.profileID),
		})
	} else if overrides := inventory.profileTargets[profileModelKey{profileID: *key.profileID, modelID: model.id}]; len(overrides) > 0 {
		profileID := *key.profileID
		report.ResolutionSource = resolutionV3Profile
		report.AppliedRoutingProfileID = &profileID
		report.AppliedRoutingProfileName = profileName
		selected = append(selected, overrides...)
	} else {
		report.ResolutionSource = resolutionV3ProfileDefaultFallback
		report.ImplicitInheritance = true
		selected = defaultV3Targets(inventory, model.id)
		report.Issues = append(report.Issues, Issue{
			Code:     "routing_profile_model_fail_open",
			Severity: SeverityWarning,
			Message:  fmt.Sprintf("Routing profile %q has no explicit targets for this model, so the current runtime silently inherits the global default order.", profileName),
		})
	}
	for _, selectedTarget := range selected {
		target, exists := inventory.v3Targets[selectedTarget.targetID]
		targetReport := materializeV3Target(inventory, model, selectedTarget, target, exists, nowMS)
		report.Targets = append(report.Targets, targetReport)
		if targetReport.Routable {
			report.Routable = true
		}
	}
	if !report.Routable {
		report.Issues = append(report.Issues, Issue{
			Code:     "no_routable_targets",
			Severity: SeverityError,
			Message:  "The model is visible to the downstream key but resolves to no currently usable target.",
		})
	}
	return report
}

func defaultV3Targets(inventory sourceInventory, modelID int64) []sourceProfileTarget {
	ids := inventory.v3TargetsByModel[modelID]
	targets := make([]sourceProfileTarget, 0, len(ids))
	for _, id := range ids {
		target := inventory.v3Targets[id]
		targets = append(targets, sourceProfileTarget{targetID: id, position: target.position})
	}
	return targets
}

func materializeV3Target(inventory sourceInventory, model sourceV3Model, selected sourceProfileTarget, target sourceV3Target, exists bool, nowMS int64) TargetReport {
	report := TargetReport{
		LegacyID: target.id,
		Position: selected.position,
		Enabled:  target.enabled,
	}
	if !exists {
		report.LegacyID = selected.targetID
		report.EndpointMissing = true
		report.CredentialMissing = true
		report.Issues = append(report.Issues, Issue{Code: "route_target_missing", Severity: SeverityError,
			Message: fmt.Sprintf("Profile target %d no longer exists; the runtime applies the profile but produces no route for this entry.", selected.targetID)})
		return report
	}
	siteID := target.siteID
	endpointID := target.endpointID
	siteModelID := target.siteModelID
	report.SiteID = &siteID
	report.SiteName = target.siteName
	report.EndpointID = &endpointID
	report.EndpointName = target.endpointName
	report.BaseURL = target.baseURL
	applyProtocolMapping(&report, target.protocol)
	report.SiteModelID = &siteModelID
	report.SourceModel = target.sourceModel

	credentials := inventory.v3CredentialsBySite[target.siteID]
	report.CredentialCount = len(credentials)
	for _, credential := range credentials {
		availability := inventory.credentialAccess[credentialModelKey{credentialID: credential.id, modelID: target.siteModelID}]
		eligible := credential.enabled && credential.configured && availability != "unsupported" && v3CredentialRuntimeEligible(credential, nowMS)
		report.Credentials = append(report.Credentials, CredentialReport{
			LegacyID: credential.id, Name: credential.name, Enabled: credential.enabled,
			RuntimeState: credential.runtimeState, Configured: credential.configured, Eligible: eligible,
		})
		if eligible {
			report.EligibleCredentialCount++
		}
	}
	report.EndpointMissing = !target.endpointFound
	report.CredentialMissing = report.EligibleCredentialCount == 0
	report.Routable = target.enabled && target.siteFound && target.siteEnabled && target.endpointFound && target.endpointEnabled &&
		target.siteModelFound && target.siteModelEnabled && routeEligibleV3Protocol(target.protocol) && !report.CredentialMissing

	if target.publishedModelID != model.id {
		report.Issues = append(report.Issues, Issue{Code: "target_model_mismatch", Severity: SeverityError,
			Message: fmt.Sprintf("Target %d belongs to published model %d, not %d.", target.id, target.publishedModelID, model.id)})
	}
	if !target.enabled {
		report.Issues = append(report.Issues, Issue{Code: "target_disabled", Severity: SeverityWarning, Message: "The route target is disabled."})
	}
	if !target.siteFound {
		report.Issues = append(report.Issues, Issue{Code: "site_missing", Severity: SeverityError, Message: "The route target references a missing site."})
	} else if !target.siteEnabled {
		report.Issues = append(report.Issues, Issue{Code: "site_disabled", Severity: SeverityWarning, Message: "The target site is disabled."})
	}
	if !target.endpointFound {
		report.Issues = append(report.Issues, Issue{Code: "endpoint_missing", Severity: SeverityError, Message: "The route target references a missing endpoint."})
	} else if !target.endpointEnabled {
		report.Issues = append(report.Issues, Issue{Code: "endpoint_disabled", Severity: SeverityWarning, Message: "The target endpoint is disabled."})
	}
	if !target.siteModelFound {
		report.Issues = append(report.Issues, Issue{Code: "site_model_missing", Severity: SeverityError, Message: "The route target references a missing source model."})
	} else if !target.siteModelEnabled {
		report.Issues = append(report.Issues, Issue{Code: "site_model_disabled", Severity: SeverityWarning, Message: "The source model is disabled."})
	}
	if report.CredentialMissing {
		code := "credential_unavailable"
		message := "No configured credential is currently eligible for this target."
		if report.CredentialCount == 0 {
			code = "credential_missing"
			message = "The target site has no inference credential."
		}
		report.Issues = append(report.Issues, Issue{Code: code, Severity: SeverityError, Message: message})
	}
	if target.endpointFound && !routeEligibleV3Protocol(target.protocol) {
		report.Issues = append(report.Issues, Issue{Code: "protocol_not_route_eligible", Severity: SeverityError,
			Message: fmt.Sprintf("Protocol %q is discovery-only or unsupported by the current gateway.", target.protocol)})
	}
	return report
}

func materializeLegacyModel(inventory sourceInventory, route sourceLegacyRoute) ModelReport {
	routeID := route.id
	report := ModelReport{
		PublicName:              route.name,
		Generation:              generationLegacy,
		LegacyRouteID:           &routeID,
		Revision:                route.revision,
		Enabled:                 route.enabled,
		EffectivePriceSKU:       route.name,
		ExplicitPriceSKUMissing: true,
		ResolutionSource:        resolutionLegacyGlobal,
		ImplicitInheritance:     true,
		Targets:                 []TargetReport{},
		Issues: []Issue{{
			Code:     "price_sku_implicit",
			Severity: SeverityWarning,
			Message:  "Legacy routes have no explicit official price SKU; the public model name is the only available migration candidate.",
		}},
	}
	for _, target := range inventory.legacyTargetsByRoute[route.id] {
		targetReport := materializeLegacyTarget(target)
		report.Targets = append(report.Targets, targetReport)
		if targetReport.Routable {
			report.Routable = true
		}
	}
	if !report.Routable {
		report.Issues = append(report.Issues, Issue{Code: "no_routable_targets", Severity: SeverityError,
			Message: "The legacy model is visible to the downstream key but resolves to no currently usable target."})
	}
	return report
}

func materializeLegacyTarget(target sourceLegacyTarget) TargetReport {
	upstreamID := target.upstreamID
	endpointID := target.endpointID
	credentialID := target.credentialID
	report := TargetReport{
		LegacyID:        target.id,
		Position:        target.position,
		UpstreamID:      &upstreamID,
		UpstreamName:    target.upstreamName,
		EndpointID:      &endpointID,
		EndpointName:    target.endpointName,
		BaseURL:         target.baseURL,
		SourceProtocol:  target.protocol,
		SourceModel:     target.sourceModel,
		CredentialID:    &credentialID,
		CredentialName:  target.credentialName,
		Enabled:         target.enabled,
		EndpointMissing: !target.endpointFound,
		CredentialMissing: !target.credentialFound || !target.credentialEnabled || !target.credentialConfigured ||
			target.credentialState == "invalid" || target.credentialState == "revoked",
	}
	applyProtocolMapping(&report, target.protocol)
	if target.credentialFound {
		eligible := !report.CredentialMissing
		report.CredentialCount = 1
		if eligible {
			report.EligibleCredentialCount = 1
		}
		report.Credentials = []CredentialReport{{LegacyID: target.credentialID, Name: target.credentialName,
			Enabled: target.credentialEnabled, RuntimeState: target.credentialState, Configured: target.credentialConfigured, Eligible: eligible}}
	}
	report.Routable = target.enabled && target.upstreamFound && target.upstreamEnabled && target.upstreamModelFound &&
		target.upstreamModelEnabled && target.endpointFound && target.endpointEnabled && !report.CredentialMissing &&
		routeEligibleLegacyProtocol(target.protocol)
	if !target.enabled {
		report.Issues = append(report.Issues, Issue{Code: "target_disabled", Severity: SeverityWarning, Message: "The legacy route target is disabled."})
	}
	if !target.upstreamFound {
		report.Issues = append(report.Issues, Issue{Code: "upstream_missing", Severity: SeverityError, Message: "The legacy target references a missing upstream."})
	} else if !target.upstreamEnabled {
		report.Issues = append(report.Issues, Issue{Code: "upstream_disabled", Severity: SeverityWarning, Message: "The legacy upstream is disabled."})
	}
	if !target.upstreamModelFound {
		report.Issues = append(report.Issues, Issue{Code: "upstream_model_missing", Severity: SeverityError, Message: "The legacy target references a missing upstream model."})
	} else if !target.upstreamModelEnabled {
		report.Issues = append(report.Issues, Issue{Code: "upstream_model_disabled", Severity: SeverityWarning, Message: "The legacy upstream model is disabled."})
	}
	if !target.endpointFound {
		report.Issues = append(report.Issues, Issue{Code: "endpoint_missing", Severity: SeverityError, Message: "The legacy target references a missing endpoint."})
	} else if !target.endpointEnabled {
		report.Issues = append(report.Issues, Issue{Code: "endpoint_disabled", Severity: SeverityWarning, Message: "The legacy endpoint is disabled."})
	}
	if report.CredentialMissing {
		code := "credential_unavailable"
		message := "The legacy credential is disabled, invalid, revoked, or has no configured secret."
		if !target.credentialFound {
			code = "credential_missing"
			message = "The legacy target references a missing credential."
		}
		report.Issues = append(report.Issues, Issue{Code: code, Severity: SeverityError, Message: message})
	}
	if target.upstreamFound && !routeEligibleLegacyProtocol(target.protocol) {
		report.Issues = append(report.Issues, Issue{Code: "protocol_not_route_eligible", Severity: SeverityError,
			Message: fmt.Sprintf("Legacy protocol %q is not route-eligible.", target.protocol)})
	}
	if target.endpointFound && target.endpointUpstreamID != target.upstreamID {
		report.Issues = append(report.Issues, Issue{Code: "endpoint_upstream_mismatch", Severity: SeverityError,
			Message: "The endpoint belongs to a different upstream than the target model."})
	}
	if target.credentialFound && target.credentialUpstreamID != target.upstreamID {
		report.Issues = append(report.Issues, Issue{Code: "credential_upstream_mismatch", Severity: SeverityError,
			Message: "The credential belongs to a different upstream than the target model."})
	}
	return report
}

func parseAllowedModels(raw string) ([]string, bool, *Issue) {
	var models []string
	if err := json.Unmarshal([]byte(raw), &models); err != nil {
		return []string{}, false, &Issue{
			Code:     "allowed_models_invalid_fail_open",
			Severity: SeverityError,
			Message:  "allowedModels is invalid JSON; the current runtime treats it as an empty list and therefore allows every model.",
		}
	}
	if models == nil {
		models = []string{}
	}
	return models, true, nil
}

func orderedModelNames(inventory sourceInventory) []string {
	names := make(map[string]struct{}, len(inventory.v3Models)+len(inventory.legacyRoutes))
	for name := range inventory.v3Models {
		names[name] = struct{}{}
	}
	for name := range inventory.legacyRoutes {
		names[name] = struct{}{}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := strings.ToLower(result[i]), strings.ToLower(result[j])
		if left == right {
			return result[i] < result[j]
		}
		return left < right
	})
	return result
}

func v3CredentialRuntimeEligible(credential sourceV3Credential, nowMS int64) bool {
	if credential.runtimeState == "active" {
		return true
	}
	return credential.runtimeState == "rate_limited" && credential.cooldownUntil != nil && *credential.cooldownUntil <= nowMS
}

func routeEligibleV3Protocol(protocol string) bool {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "openai", "compatible", "openai_chat_completions", "openai_responses":
		return true
	default:
		return false
	}
}

func routeEligibleLegacyProtocol(protocol string) bool {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "openai", "compatible":
		return true
	default:
		return false
	}
}

func applyProtocolMapping(report *TargetReport, source string) {
	report.SourceProtocol = source
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "openai_chat_completions", "chat_completions", string(vnextprotocol.OpenAIChatCompletions):
		report.WireProtocol = string(vnextprotocol.OpenAI)
		report.Surface = string(vnextprotocol.OpenAIChatCompletions)
	case "openai_responses", "responses", string(vnextprotocol.OpenAIResponses):
		report.WireProtocol = string(vnextprotocol.OpenAI)
		report.Surface = string(vnextprotocol.OpenAIResponses)
	case "anthropic", "anthropic_messages", "messages", string(vnextprotocol.AnthropicMessages):
		report.WireProtocol = string(vnextprotocol.Anthropic)
		report.Surface = string(vnextprotocol.AnthropicMessages)
	case "gemini", "gemini_generate_content", "generate_content", "generatecontent", string(vnextprotocol.GeminiGenerateContent):
		report.WireProtocol = string(vnextprotocol.Gemini)
		report.Surface = string(vnextprotocol.GeminiGenerateContent)
	case "openai", "compatible", "openai-compatible", "openai_compatible":
		report.WireProtocol = string(vnextprotocol.OpenAI)
		report.ProtocolMappingAmbiguous = true
		report.SurfaceCandidates = []string{
			string(vnextprotocol.OpenAIChatCompletions),
			string(vnextprotocol.OpenAIResponses),
		}
		report.Issues = append(report.Issues, Issue{
			Code:     "protocol_surface_ambiguous",
			Severity: SeverityError,
			Message:  "The legacy protocol does not identify whether this endpoint serves Chat Completions, Responses, or both; migration must choose explicit surface records.",
		})
	default:
		report.Issues = append(report.Issues, Issue{
			Code:     "protocol_mapping_unknown",
			Severity: SeverityError,
			Message:  fmt.Sprintf("Protocol %q cannot be mapped to a VNext wire protocol and surface.", source),
		})
	}
}

func summarize(report Report) Summary {
	summary := Summary{KeyCount: len(report.Keys), IssueCount: len(report.Issues)}
	for _, key := range report.Keys {
		if !key.SecretRevealable {
			summary.NonRevealableKeyCount++
		}
		summary.IssueCount += len(key.Issues)
		for _, model := range key.Models {
			summary.ModelCount++
			if model.Routable {
				summary.RoutableModelCount++
			}
			if model.ImplicitInheritance {
				summary.ImplicitRouteCount++
			}
			summary.IssueCount += len(model.Issues)
			for _, target := range model.Targets {
				summary.TargetCount++
				if target.Routable {
					summary.RoutableTargetCount++
				}
				summary.IssueCount += len(target.Issues)
			}
		}
	}
	return summary
}

func cloneInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
