package httpapi

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/LuTianTian001/JieShan/internal/billing"
	"github.com/LuTianTian001/JieShan/internal/store"
)

func upstreamDTO(item store.Upstream, models []store.UpstreamModel) map[string]any {
	state := "unknown"
	if !item.Enabled {
		state = "disabled"
	} else if item.CredentialState == "invalid" || item.CredentialState == "revoked" {
		state = "credential_error"
	}
	modelItems := make([]map[string]any, 0, len(models))
	var lastSync *int64
	for _, model := range models {
		discovered := item.UpdatedAt
		if model.LastSeenAt != nil {
			discovered = *model.LastSeenAt
			if lastSync == nil || discovered > *lastSync {
				copy := discovered
				lastSync = &copy
			}
		}
		modelItems = append(modelItems, map[string]any{"id": strconv.FormatInt(model.ID, 10), "name": model.ModelName, "enabled": model.Enabled, "discoveredAt": iso(discovered)})
	}
	result := map[string]any{
		"id": item.ID, "name": item.Name, "baseUrl": item.BaseURL, "protocol": item.Kind,
		"enabled": item.Enabled, "state": state, "latencyMs": nil, "modelCount": item.ModelCount,
		"credentialCount": 1, "lastSyncAt": isoOptional(lastSync),
		"balanceSupported": item.BalanceValue != "" || len(item.Subscription) > 0 || item.LastBalanceSyncAt != nil,
		"usageSupported":   false,
	}
	if models != nil {
		result["models"] = modelItems
	}
	if item.BalanceValue != "" {
		if amount, err := strconv.ParseFloat(item.BalanceValue, 64); err == nil {
			balance := map[string]any{"amount": amount, "currency": firstNonEmpty(item.BalanceCurrency, "USD"), "sourceLabel": "上游返回"}
			var subscription map[string]any
			if len(item.Subscription) > 0 && json.Unmarshal(item.Subscription, &subscription) == nil {
				if plan, ok := subscription["plan"].(string); ok {
					balance["plan"] = plan
				}
			}
			result["balance"] = balance
		}
	}
	return result
}

func routeDTO(route store.Route) map[string]any {
	targets := make([]map[string]any, 0, len(route.Targets))
	for _, target := range route.Targets {
		state := targetState(target)
		targets = append(targets, map[string]any{
			"id": target.ID, "upstreamId": target.UpstreamID, "upstreamName": target.UpstreamName,
			"credentialName": firstNonEmpty(target.CredentialName, "默认密钥"), "sourceModel": target.UpstreamModel,
			"state": state, "latencyMs": target.LastProbeLatency, "cooldownUntil": isoOptional(target.CooldownUntil),
			"lastFailure": emptyNil(target.LastErrorMessage),
		})
	}
	return map[string]any{"id": route.ID, "model": route.PublicModel, "displayName": firstNonEmpty(route.DisplayName, route.PublicModel), "enabled": route.Enabled, "monitored": route.MonitorEnabled, "revision": route.Revision, "targets": targets}
}

func targetState(target store.RouteTarget) string {
	if !target.Enabled {
		return "disabled"
	}
	if target.CredentialState == "invalid" || target.CredentialState == "revoked" {
		return "credential_error"
	}
	if target.CircuitPhase == "open" && target.CooldownUntil != nil && *target.CooldownUntil > time.Now().UnixMilli() {
		return "cooldown"
	}
	if target.CircuitPhase == "half_open" {
		return "probing"
	}
	if target.ConsecutiveFails > 0 {
		return "suspect"
	}
	if target.LastProbeStatus == "healthy" || target.CapabilityState == "supported" {
		return "healthy"
	}
	return "unknown"
}

func keyDTO(item store.DownstreamKey) map[string]any {
	var quota any
	if item.QuotaMicroUSD != nil {
		quota = float64(*item.QuotaMicroUSD) / 1_000_000
	}
	var rpm any
	if item.RPMLimit > 0 {
		rpm = item.RPMLimit
	}
	return map[string]any{
		"id": item.ID, "name": item.Name, "prefix": item.KeyPrefix, "enabled": item.Enabled,
		"quotaUsd": quota, "spentUsd": float64(item.UsedMicroUSD) / 1_000_000,
		"allowedModels": item.AllowedModels, "rpmLimit": rpm, "expiresAt": isoOptional(item.ExpiresAt),
		"lastUsedAt": isoOptional(item.LastUsedAt), "createdAt": iso(item.CreatedAt),
	}
}

func logDTO(item store.RequestLog, attempts []store.RequestAttempt) map[string]any {
	result := map[string]any{
		"id": item.ID, "startedAt": iso(item.StartedAt), "keyName": item.KeyName,
		"requestedModel": item.RequestedModel, "actualModel": item.ActualModel, "status": item.Status,
		"durationMs": int64Value(item.DurationMS), "ttftMs": item.FirstTokenMS,
		"inputTokens": int64Value(item.InputTokens), "cacheTokens": int64Value(item.CacheReadTokens),
		"outputTokens": int64Value(item.OutputTokens), "reasoningTokens": int64Value(item.ReasoningTokens),
		"costUsd": float64(item.CostMicroUSD) / 1_000_000, "switchCount": item.SwitchCount,
	}
	if item.ReasoningEffort != "" {
		result["reasoningEffort"] = item.ReasoningEffort
	}
	if item.ThinkingBudget != nil {
		result["thinkingBudget"] = *item.ThinkingBudget
	}
	if attempts != nil {
		attemptItems := make([]map[string]any, 0, len(attempts))
		for _, attempt := range attempts {
			attemptItems = append(attemptItems, map[string]any{
				"id": attempt.ID, "sequence": attempt.AttemptIndex + 1, "upstreamName": attempt.UpstreamName,
				"model": attempt.UpstreamModel, "state": attempt.Status, "startedAt": iso(attempt.CreatedAt),
				"durationMs": int64Value(attempt.LatencyMS), "ttftMs": attempt.FirstTokenMS,
				"statusCode": attempt.HTTPStatus, "switchReason": emptyNil(attempt.SwitchReason), "error": emptyNil(attempt.ErrorMessage),
			})
		}
		result["attempts"] = attemptItems
	}
	return result
}

func settingsDTO(item store.Settings) map[string]any {
	version := "unconfigured"
	updatedAt := iso(item.UpdatedAt)
	source := "官方价格目录不可用"
	if catalog, err := billing.BuiltinCatalog(); err == nil {
		version = catalog.Version
		source = "内置官方价格目录"
		if checked, parseErr := time.Parse("2006-01-02", catalog.CheckedAt); parseErr == nil {
			updatedAt = checked.UTC().Format(time.RFC3339)
		}
	}
	return map[string]any{
		"probeIntervalSeconds": item.ProbeIntervalSeconds, "failureThreshold": item.FailureThreshold,
		"cooldownSeconds": item.DefaultCooldownSeconds, "requestTimeoutSeconds": item.RequestDeadlineSeconds,
		"maxAttempts": item.MaxAttempts, "logRetentionDays": item.LogRetentionDays,
		"priceCatalogVersion": version, "priceCatalogUpdatedAt": updatedAt,
		"priceCatalogSource": source, "lastBackupAt": nil,
	}
}

func iso(ms int64) string { return time.UnixMilli(ms).UTC().Format(time.RFC3339) }
func isoOptional(value *int64) any {
	if value == nil || *value <= 0 {
		return nil
	}
	return iso(*value)
}
func emptyNil(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func int64Value(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
