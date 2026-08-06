package accountadapter

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	newOneStatusPath = "/api/status"
	newOneSelfPath   = "/api/user/self"
	newAPISubPath    = "/api/subscription/self"
	newOneLogPath    = "/api/log/self"
)

func (a *adapter) newOneSnapshot(ctx context.Context, connection Connection) (Snapshot, *Credentials, error) {
	quotaPerUnit, err := a.quotaPerUnit(ctx, connection)
	if err != nil {
		return Snapshot{}, nil, err
	}
	data, err := a.newOneData(ctx, connection, "snapshot", newOneSelfPath, true)
	if err != nil {
		return Snapshot{}, nil, err
	}
	profile := object(data)
	if profile == nil {
		return Snapshot{}, nil, remoteError(a.kind, "snapshot", http.StatusOK, "MALFORMED_RESPONSE", "profile data is not an object", connection.Credentials)
	}
	quota := amountValue(lookup(profile, "quota"))
	usedQuota := amountValue(lookup(profile, "used_quota"))
	return Snapshot{
		AccountID:    stringValue(lookup(profile, "id", "user_id")),
		Username:     stringValue(lookup(profile, "username", "display_name", "email")),
		Status:       stringValue(lookup(profile, "status")),
		Currency:     "quota",
		Balance:      quota,
		Used:         usedQuota,
		QuotaPerUnit: quotaPerUnit,
		RequestCount: int64Value(lookup(profile, "request_count")),
	}, nil, nil
}

func (a *adapter) newAPISubscriptions(ctx context.Context, connection Connection) ([]Subscription, *Credentials, error) {
	quotaPerUnit, err := a.quotaPerUnit(ctx, connection)
	if err != nil {
		return nil, nil, err
	}
	data, err := a.newOneData(ctx, connection, "subscriptions", newAPISubPath, true)
	if err != nil {
		return nil, nil, err
	}
	container := object(data)
	if container == nil {
		return nil, nil, remoteError(a.kind, "subscriptions", http.StatusOK, "MALFORMED_RESPONSE", "subscription data is not an object", connection.Credentials)
	}
	items, ok := container["all_subscriptions"]
	if !ok {
		items = container["subscriptions"]
	}
	rawItems := array(items)
	if rawItems == nil {
		return nil, nil, remoteError(a.kind, "subscriptions", http.StatusOK, "MALFORMED_RESPONSE", "subscription list is missing", connection.Credentials)
	}
	result := make([]Subscription, 0, len(rawItems))
	for _, raw := range rawItems {
		wrapper := object(raw)
		if wrapper == nil {
			continue
		}
		subscription := object(lookup(wrapper, "subscription"))
		if subscription == nil {
			subscription = wrapper
		}
		plan := object(lookup(wrapper, "plan"))
		if plan == nil {
			plan = object(lookup(subscription, "plan"))
		}
		totalRaw := amountValue(lookup(subscription, "amount_total", "total_amount"))
		usedRaw := amountValue(lookup(subscription, "amount_used", "used_amount"))
		remainingRaw := subtractAmount(totalRaw, usedRaw)
		result = append(result, Subscription{
			ID:              stringValue(lookup(subscription, "id")),
			Name:            firstNonEmpty(stringValue(lookup(plan, "title", "name")), stringValue(lookup(subscription, "title", "upgrade_group"))),
			Status:          stringValue(lookup(subscription, "status")),
			Currency:        "quota",
			QuotaPerUnit:    quotaPerUnit,
			StartsAt:        timestampValue(lookup(subscription, "start_time", "starts_at")),
			ExpiresAt:       timestampValue(lookup(subscription, "end_time", "expires_at")),
			NextResetAt:     timestampValue(lookup(subscription, "next_reset_time", "next_reset_at")),
			AmountTotal:     totalRaw,
			AmountUsed:      usedRaw,
			AmountRemaining: remainingRaw,
			GroupID:         stringValue(lookup(subscription, "group_id")),
			GroupName:       stringValue(lookup(subscription, "upgrade_group", "group")),
		})
	}
	return result, nil, nil
}

func (a *adapter) newOneUsage(ctx context.Context, connection Connection, query UsageQuery) (UsagePage, *Credentials, error) {
	quotaPerUnit, err := a.quotaPerUnit(ctx, connection)
	if err != nil {
		return UsagePage{}, nil, err
	}
	page, requestedPageSize := normalizedPage(query)
	values := url.Values{}
	pageSize := requestedPageSize
	if a.kind == KindOneAPI {
		values.Set("p", strconv.Itoa(page-1))
		pageSize = defaultPageSize
	} else {
		values.Set("p", strconv.Itoa(page))
		values.Set("page_size", strconv.Itoa(pageSize))
		setQuery(values, "group", query.Group)
		setQuery(values, "request_id", query.RequestID)
		setQuery(values, "upstream_request_id", query.UpstreamRequestID)
	}
	if query.Type != 0 {
		values.Set("type", strconv.Itoa(query.Type))
	}
	if query.StartUnix != 0 {
		values.Set("start_timestamp", strconv.FormatInt(query.StartUnix, 10))
	}
	if query.EndUnix != 0 {
		values.Set("end_timestamp", strconv.FormatInt(query.EndUnix, 10))
	}
	setQuery(values, "model_name", query.Model)
	setQuery(values, "token_name", query.TokenName)

	data, err := a.newOneDataWithQuery(ctx, connection, "usage", newOneLogPath, values)
	if err != nil {
		return UsagePage{}, nil, err
	}
	var rawItems []any
	result := UsagePage{Page: page, PageSize: pageSize, Unit: "quota", QuotaPerUnit: quotaPerUnit}
	if a.kind == KindNewAPI {
		container := object(data)
		if container == nil {
			return UsagePage{}, nil, remoteError(a.kind, "usage", http.StatusOK, "MALFORMED_RESPONSE", "usage data is not an object", connection.Credentials)
		}
		rawItems = array(lookup(container, "items"))
		result.Total = int64Value(lookup(container, "total"))
		if parsedPage := intValue(lookup(container, "page")); parsedPage > 0 {
			result.Page = parsedPage
		}
		if parsedSize := intValue(lookup(container, "page_size")); parsedSize > 0 {
			result.PageSize = parsedSize
		}
		if result.PageSize > 0 && result.Total > 0 {
			result.Pages = int((result.Total + int64(result.PageSize) - 1) / int64(result.PageSize))
		}
	} else {
		rawItems = array(data)
	}
	if rawItems == nil {
		return UsagePage{}, nil, remoteError(a.kind, "usage", http.StatusOK, "MALFORMED_RESPONSE", "usage items are not a list", connection.Credentials)
	}
	result.Items = make([]UsageItem, 0, len(rawItems))
	for _, raw := range rawItems {
		item := newOneUsageItem(a.kind, object(raw))
		if item != nil {
			result.Items = append(result.Items, *item)
		}
	}
	result.HasMore = (result.Pages > 0 && result.Page < result.Pages) ||
		(result.Pages == 0 && len(result.Items) == result.PageSize)
	return result, nil, nil
}

func (a *adapter) newOneData(ctx context.Context, connection Connection, operation, path string, authenticated bool) (any, error) {
	return a.newOneDataWithQueryAndAuth(ctx, connection, operation, path, nil, authenticated)
}

func (a *adapter) newOneDataWithQuery(ctx context.Context, connection Connection, operation, path string, query url.Values) (any, error) {
	return a.newOneDataWithQueryAndAuth(ctx, connection, operation, path, query, true)
}

func (a *adapter) newOneDataWithQueryAndAuth(ctx context.Context, connection Connection, operation, path string, query url.Values, authenticated bool) (any, error) {
	headers := map[string]string{}
	if authenticated {
		authorization := a.managementAuthorization(connection.Credentials)
		if authorization == "" {
			return nil, fmt.Errorf("%w: authorization is required", ErrInvalidConnection)
		}
		headers["Authorization"] = authorization
	}
	status, body, err := a.request(ctx, connection, http.MethodGet, path, query, nil, headers, connection.Credentials)
	if err != nil {
		return nil, err
	}
	return a.unwrapNewOne(operation, status, body, connection.Credentials)
}

func (a *adapter) unwrapNewOne(operation string, status int, body []byte, credentials ...Credentials) (any, error) {
	decoded, err := decodeJSON(body)
	if err != nil {
		return nil, remoteError(a.kind, operation, status, "MALFORMED_RESPONSE", "upstream returned invalid JSON", credentials...)
	}
	envelope := object(decoded)
	if envelope == nil {
		return nil, remoteError(a.kind, operation, status, "MALFORMED_RESPONSE", "upstream response is not an object", credentials...)
	}
	success, hasSuccess := envelope["success"].(bool)
	code := stringValue(lookup(envelope, "code"))
	message := firstNonEmpty(stringValue(lookup(envelope, "message", "detail")), http.StatusText(status))
	if status < 200 || status >= 300 {
		return nil, remoteError(a.kind, operation, status, code, message, credentials...)
	}
	if !hasSuccess {
		return nil, remoteError(a.kind, operation, status, "MALFORMED_RESPONSE", "upstream response has no success flag", credentials...)
	}
	if !success {
		return nil, remoteError(a.kind, operation, status, code, message, credentials...)
	}
	return envelope["data"], nil
}

func (a *adapter) quotaPerUnit(ctx context.Context, connection Connection) (string, error) {
	cacheKey := string(a.kind) + "|" + strings.TrimRight(strings.TrimSpace(connection.Origin), "/")
	now := time.Now()
	a.quotaMu.Lock()
	cached, ok := a.quotaCache[cacheKey]
	a.quotaMu.Unlock()
	if ok && now.Before(cached.expiresAt) {
		return cached.value, nil
	}
	data, err := a.newOneData(ctx, connection, "status", newOneStatusPath, false)
	if err != nil {
		return "", err
	}
	status := object(data)
	quotaPerUnit := amountValue(lookup(status, "quota_per_unit"))
	if strings.TrimSpace(quotaPerUnit) == "" {
		return "", remoteError(a.kind, "status", http.StatusOK, "MALFORMED_AMOUNT", "quota_per_unit is missing or invalid", connection.Credentials)
	}
	a.quotaMu.Lock()
	a.quotaCache[cacheKey] = quotaCacheEntry{value: quotaPerUnit, expiresAt: now.Add(5 * time.Minute)}
	a.quotaMu.Unlock()
	return quotaPerUnit, nil
}

func (a *adapter) managementAuthorization(credentials Credentials) string {
	if authorization := strings.TrimSpace(credentials.Authorization); authorization != "" {
		if a.kind == KindNewAPI {
			return bearerAuthorization(Credentials{Authorization: authorization})
		}
		return authorization
	}
	accessToken := strings.TrimSpace(credentials.AccessToken)
	if accessToken == "" {
		return ""
	}
	if a.kind == KindNewAPI {
		return "Bearer " + accessToken
	}
	return accessToken
}

func newOneUsageItem(kind Kind, values map[string]any) *UsageItem {
	if values == nil {
		return nil
	}
	other := decodeOther(lookup(values, "other"))
	quota := amountValue(lookup(values, "quota"))
	prompt := int64Value(lookup(values, "prompt_tokens", "input_tokens"))
	completion := int64Value(lookup(values, "completion_tokens", "output_tokens"))
	durationMS := int64Value(lookup(values, "elapsed_time", "duration_ms"))
	if kind == KindNewAPI {
		durationMS = int64Value(lookup(values, "use_time")) * 1000
	}
	return &UsageItem{
		ID:                  stringValue(lookup(values, "id")),
		RequestID:           stringValue(lookup(values, "request_id")),
		UpstreamRequestID:   stringValue(lookup(values, "upstream_request_id")),
		Model:               stringValue(lookup(values, "model_name", "model")),
		UpstreamModel:       stringValue(lookup(other, "upstream_model_name", "upstream_model")),
		ReasoningEffort:     stringValue(lookup(other, "reasoning_effort")),
		PromptTokens:        prompt,
		CompletionTokens:    completion,
		CacheReadTokens:     int64Value(lookup(other, "cache_tokens", "cache_read_tokens")),
		CacheCreationTokens: int64Value(lookup(other, "cache_creation_tokens", "cache_write_tokens")),
		TotalTokens:         prompt + completion,
		Quota:               quota,
		ModelMultiplier:     amountValue(lookup(other, "model_ratio", "model_multiplier")),
		GroupMultiplier:     amountValue(lookup(other, "group_ratio", "group_multiplier")),
		Type:                stringValue(lookup(values, "type")),
		BillingType:         stringValue(lookup(other, "billing_type")),
		BillingMode:         stringValue(lookup(other, "billing_mode")),
		Endpoint:            stringValue(lookup(other, "request_path", "inbound_endpoint")),
		IPAddress:           stringValue(lookup(values, "ip", "ip_address")),
		APIKeyID:            stringValue(lookup(values, "token_id", "api_key_id")),
		APIKeyName:          stringValue(lookup(values, "token_name", "api_key_name")),
		GroupID:             stringValue(lookup(values, "group_id")),
		GroupName:           stringValue(lookup(values, "group", "group_name")),
		DurationMS:          durationMS,
		FirstTokenMS:        int64Value(lookup(other, "frt", "first_token_ms")),
		Stream:              boolValue(lookup(values, "is_stream", "stream")),
		CreatedAt:           timestampValue(lookup(values, "created_at")),
		Content:             stringValue(lookup(values, "content")),
	}
}

func decodeOther(value any) map[string]any {
	if direct := object(value); direct != nil {
		return direct
	}
	text := strings.TrimSpace(stringValue(value))
	if text == "" {
		return nil
	}
	decoded, err := decodeJSON([]byte(text))
	if err != nil {
		return nil
	}
	return object(decoded)
}
