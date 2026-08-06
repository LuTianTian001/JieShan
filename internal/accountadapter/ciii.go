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
	ciiiAuthMePath              = "/api/v1/auth/me"
	ciiiProfilePath             = "/api/v1/user/profile"
	ciiiActiveSubscriptionsPath = "/api/v1/subscriptions/active"
	ciiiSubscriptionsPath       = "/api/v1/subscriptions"
	ciiiUsagePath               = "/api/v1/usage"
	ciiiRefreshPath             = "/api/v1/auth/refresh"
)

func (a *adapter) ciiiSnapshot(ctx context.Context, connection Connection) (Snapshot, *Credentials, error) {
	data, rotated, err := a.ciiiData(ctx, connection, "snapshot", ciiiAuthMePath, nil)
	if isRemoteStatus(err, http.StatusNotFound) {
		priorRotation := rotated
		if rotated != nil {
			connection.Credentials = *rotated
			connection.Credentials.RefreshToken = ""
		}
		var fallbackRotation *Credentials
		data, fallbackRotation, err = a.ciiiData(ctx, connection, "snapshot", ciiiProfilePath, nil)
		if fallbackRotation != nil {
			rotated = fallbackRotation
		} else {
			rotated = priorRotation
		}
	}
	if err != nil {
		return Snapshot{}, rotated, err
	}
	profile := object(data)
	if profile == nil {
		return Snapshot{}, rotated, remoteError(a.kind, "snapshot", http.StatusOK, "MALFORMED_RESPONSE", "profile data is not an object", connection.Credentials)
	}
	currency := stringValue(lookup(profile, "currency"))
	if currency == "" {
		currency = "USD"
	}
	return Snapshot{
		AccountID:    stringValue(lookup(profile, "id", "user_id")),
		Username:     stringValue(lookup(profile, "username", "email", "name")),
		Status:       stringValue(lookup(profile, "status")),
		Currency:     currency,
		Balance:      amountValue(lookup(profile, "balance", "available_balance", "quota")),
		Used:         amountValue(lookup(profile, "used_balance", "total_used", "used")),
		Frozen:       amountValue(lookup(profile, "frozen_balance", "frozen")),
		RequestCount: int64Value(lookup(profile, "request_count", "requests")),
	}, rotated, nil
}

func (a *adapter) ciiiSubscriptions(ctx context.Context, connection Connection) ([]Subscription, *Credentials, error) {
	data, rotated, err := a.ciiiData(ctx, connection, "subscriptions", ciiiActiveSubscriptionsPath, nil)
	if isRemoteStatus(err, http.StatusNotFound) {
		priorRotation := rotated
		if rotated != nil {
			connection.Credentials = *rotated
			connection.Credentials.RefreshToken = ""
		}
		var fallbackRotation *Credentials
		data, fallbackRotation, err = a.ciiiData(ctx, connection, "subscriptions", ciiiSubscriptionsPath, nil)
		if fallbackRotation != nil {
			rotated = fallbackRotation
		} else {
			rotated = priorRotation
		}
	}
	if err != nil {
		return nil, rotated, err
	}
	items := responseItems(data, "items", "subscriptions")
	if items == nil {
		return nil, rotated, remoteError(a.kind, "subscriptions", http.StatusOK, "MALFORMED_RESPONSE", "subscription data is not a list", connection.Credentials)
	}
	result := make([]Subscription, 0, len(items))
	for _, value := range items {
		item := object(value)
		if item == nil {
			continue
		}
		group := object(lookup(item, "group"))
		name := stringValue(lookup(item, "name", "title"))
		if name == "" {
			name = stringValue(lookup(group, "name", "title"))
		}
		total := amountValue(lookup(item, "amount_total", "total_amount", "total_quota"))
		used := amountValue(lookup(item, "amount_used", "used_amount", "used_quota"))
		currency := stringValue(lookup(item, "currency"))
		if currency == "" {
			currency = "USD"
		}
		result = append(result, Subscription{
			ID:              stringValue(lookup(item, "id")),
			Name:            name,
			Status:          stringValue(lookup(item, "status")),
			Currency:        currency,
			StartsAt:        timestampValue(lookup(item, "starts_at", "start_time")),
			ExpiresAt:       timestampValue(lookup(item, "expires_at", "end_time")),
			NextResetAt:     timestampValue(lookup(item, "next_reset_at", "next_reset_time")),
			AmountTotal:     total,
			AmountUsed:      used,
			AmountRemaining: subtractAmount(total, used),
			GroupID:         firstNonEmpty(stringValue(lookup(item, "group_id")), stringValue(lookup(group, "id"))),
			GroupName:       stringValue(lookup(group, "name", "title")),
			Platform:        stringValue(lookup(group, "platform")),
			RateMultiplier:  amountValue(lookup(group, "rate_multiplier", "rate")),
			Daily:           ciiiWindow(item, group, "daily"),
			Weekly:          ciiiWindow(item, group, "weekly"),
			Monthly:         ciiiWindow(item, group, "monthly"),
		})
	}
	return result, rotated, nil
}

func (a *adapter) ciiiUsage(ctx context.Context, connection Connection, query UsageQuery) (UsagePage, *Credentials, error) {
	page, pageSize := normalizedPage(query)
	values := url.Values{
		"page":      []string{strconv.Itoa(page)},
		"page_size": []string{strconv.Itoa(pageSize)},
		"timezone":  []string{"UTC"},
	}
	setQuery(values, "api_key_id", query.APIKeyID)
	setQuery(values, "model", query.Model)
	setQuery(values, "group_id", query.Group)
	setQuery(values, "request_type", query.RequestType)
	setQuery(values, "billing_mode", query.BillingMode)
	setQuery(values, "sort_by", query.SortBy)
	setQuery(values, "sort_order", query.SortOrder)
	if query.BillingType != nil {
		values.Set("billing_type", strconv.Itoa(*query.BillingType))
	}
	startDate := query.StartDate
	if startDate == "" && query.StartUnix > 0 {
		startDate = time.Unix(query.StartUnix, 0).UTC().Format("2006-01-02")
	}
	endDate := query.EndDate
	if endDate == "" && query.EndUnix > 0 {
		endDate = time.Unix(query.EndUnix, 0).UTC().Format("2006-01-02")
	}
	setQuery(values, "start_date", startDate)
	setQuery(values, "end_date", endDate)

	data, rotated, err := a.ciiiData(ctx, connection, "usage", ciiiUsagePath, values)
	if err != nil {
		return UsagePage{}, rotated, err
	}
	container := object(data)
	if container == nil {
		return UsagePage{}, rotated, remoteError(a.kind, "usage", http.StatusOK, "MALFORMED_RESPONSE", "usage data is not an object", connection.Credentials)
	}
	rawItems := array(lookup(container, "items"))
	if rawItems == nil {
		return UsagePage{}, rotated, remoteError(a.kind, "usage", http.StatusOK, "MALFORMED_RESPONSE", "usage items are not a list", connection.Credentials)
	}
	items := make([]UsageItem, 0, len(rawItems))
	for _, raw := range rawItems {
		if item := ciiiUsageItem(object(raw)); item != nil {
			items = append(items, *item)
		}
	}
	result := UsagePage{
		Items:    items,
		Total:    int64Value(lookup(container, "total")),
		Page:     intValue(lookup(container, "page")),
		PageSize: intValue(lookup(container, "page_size")),
		Pages:    intValue(lookup(container, "pages")),
		Unit:     "USD",
	}
	if result.Page < 1 {
		result.Page = page
	}
	if result.PageSize < 1 {
		result.PageSize = pageSize
	}
	result.HasMore = (result.Pages > 0 && result.Page < result.Pages) ||
		(result.Pages == 0 && len(result.Items) == result.PageSize)
	return result, rotated, nil
}

func (a *adapter) ciiiData(ctx context.Context, connection Connection, operation, path string, query url.Values) (any, *Credentials, error) {
	credentials := a.resolveRotatedCredentials(connection.Credentials)
	rotated := changedCredentials(connection.Credentials, credentials)
	status, body, err := a.ciiiRequest(ctx, connection, credentials, http.MethodGet, path, query, nil)
	if err != nil {
		return nil, rotated, err
	}
	if status == http.StatusUnauthorized && credentials.RefreshToken != "" {
		refreshed, refreshErr := a.refreshCiii(ctx, connection, credentials)
		if refreshErr != nil {
			return nil, rotated, refreshErr
		}
		credentials = refreshed
		rotated = changedCredentials(connection.Credentials, credentials)
		status, body, err = a.ciiiRequest(ctx, connection, credentials, http.MethodGet, path, query, nil)
		if err != nil {
			return nil, rotated, err
		}
	}
	data, err := a.unwrapCiii(operation, status, body, credentials)
	return data, rotated, err
}

func (a *adapter) ciiiRequest(ctx context.Context, connection Connection, credentials Credentials, method, path string, query url.Values, payload any) (int, []byte, error) {
	authorization := bearerAuthorization(credentials)
	if authorization == "" && path != ciiiRefreshPath {
		return 0, nil, fmt.Errorf("%w: access token is required", ErrInvalidConnection)
	}
	headers := map[string]string{"X-User-UI-Request": "1"}
	if authorization != "" && path != ciiiRefreshPath {
		headers["Authorization"] = authorization
	}
	return a.request(ctx, connection, method, path, query, payload, headers, credentials)
}

func (a *adapter) unwrapCiii(operation string, status int, body []byte, credentials ...Credentials) (any, error) {
	decoded, err := decodeJSON(body)
	if err != nil {
		return nil, remoteError(a.kind, operation, status, "MALFORMED_RESPONSE", "upstream returned invalid JSON", credentials...)
	}
	envelope := object(decoded)
	if envelope == nil {
		return nil, remoteError(a.kind, operation, status, "MALFORMED_RESPONSE", "upstream response is not an object", credentials...)
	}
	code, hasCode := envelope["code"]
	codeText := stringValue(code)
	message := firstNonEmpty(stringValue(lookup(envelope, "message", "msg", "detail")), http.StatusText(status))
	if status < 200 || status >= 300 {
		return nil, remoteError(a.kind, operation, status, codeText, message, credentials...)
	}
	if !hasCode {
		return nil, remoteError(a.kind, operation, status, "MALFORMED_RESPONSE", "upstream response has no business code", credentials...)
	}
	if codeText != "0" {
		return nil, remoteError(a.kind, operation, status, codeText, message, credentials...)
	}
	return envelope["data"], nil
}

func (a *adapter) refreshCiii(ctx context.Context, connection Connection, credentials Credentials) (Credentials, error) {
	if strings.TrimSpace(credentials.RefreshToken) == "" {
		return Credentials{}, fmt.Errorf("%w: refresh token is required", ErrInvalidConnection)
	}
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()

	key := credentialHash(credentials.RefreshToken)
	if cached, ok := a.rotations[key]; ok {
		return cached, nil
	}
	status, body, err := a.ciiiRequest(ctx, connection, credentials, http.MethodPost, ciiiRefreshPath, nil, map[string]string{
		"refresh_token": credentials.RefreshToken,
	})
	if err != nil {
		return Credentials{}, err
	}
	decoded, err := decodeJSON(body)
	if err != nil {
		return Credentials{}, remoteError(a.kind, "refresh", status, "MALFORMED_RESPONSE", "upstream returned invalid JSON", credentials)
	}
	root := object(decoded)
	if root == nil {
		return Credentials{}, remoteError(a.kind, "refresh", status, "MALFORMED_RESPONSE", "refresh response is not an object", credentials)
	}
	var data any = root
	if _, ok := root["code"]; ok {
		data, err = a.unwrapCiii("refresh", status, body, credentials)
		if err != nil {
			return Credentials{}, err
		}
	} else if status < 200 || status >= 300 {
		return Credentials{}, remoteError(a.kind, "refresh", status, stringValue(lookup(root, "code")), stringValue(lookup(root, "message", "detail")), credentials)
	}
	response := object(data)
	if response == nil {
		return Credentials{}, remoteError(a.kind, "refresh", status, "MALFORMED_RESPONSE", "refresh data is not an object", credentials)
	}
	accessToken := stringValue(lookup(response, "access_token"))
	refreshToken := stringValue(lookup(response, "refresh_token"))
	if accessToken == "" || refreshToken == "" {
		return Credentials{}, remoteError(a.kind, "refresh", status, "MALFORMED_RESPONSE", "refresh response is missing rotated credentials", credentials)
	}
	tokenType := stringValue(lookup(response, "token_type"))
	if tokenType == "" {
		tokenType = "Bearer"
	}
	next := credentials
	next.Authorization = tokenType + " " + accessToken
	next.AccessToken = accessToken
	next.RefreshToken = refreshToken
	if expiresIn := int64Value(lookup(response, "expires_in")); expiresIn > 0 {
		next.ExpiresAt = strconv.FormatInt(time.Now().Add(time.Duration(expiresIn)*time.Second).UnixMilli(), 10)
	}
	a.rotations[key] = next
	return next, nil
}

func (a *adapter) resolveRotatedCredentials(credentials Credentials) Credentials {
	if credentials.RefreshToken == "" {
		return credentials
	}
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()
	current := credentials
	for range 8 {
		next, ok := a.rotations[credentialHash(current.RefreshToken)]
		if !ok || !credentialsChanged(current, next) {
			break
		}
		current = next
	}
	return current
}

func ciiiWindow(item, group map[string]any, prefix string) UsageWindow {
	nested := object(lookup(item, prefix))
	used := amountValue(lookup(item, prefix+"_usage_usd", prefix+"_usage", prefix+"_used"))
	limit := amountValue(lookup(group, prefix+"_limit_usd", prefix+"_limit"))
	if nested != nil {
		used = firstNonEmpty(used, amountValue(lookup(nested, "used", "usage")))
		limit = firstNonEmpty(limit, amountValue(lookup(nested, "limit")))
	}
	return UsageWindow{
		Used:     used,
		Limit:    limit,
		StartsAt: timestampValue(lookup(item, prefix+"_window_start")),
		EndsAt:   timestampValue(lookup(item, prefix+"_window_end")),
		Label:    stringValue(lookup(item, prefix+"_window")),
	}
}

func ciiiUsageItem(values map[string]any) *UsageItem {
	if values == nil {
		return nil
	}
	apiKey := object(lookup(values, "api_key"))
	group := object(lookup(values, "group"))
	prompt := int64Value(lookup(values, "input_tokens", "prompt_tokens"))
	completion := int64Value(lookup(values, "output_tokens", "completion_tokens"))
	cacheRead := int64Value(lookup(values, "cache_read_tokens", "cached_tokens"))
	cacheCreation := int64Value(lookup(values, "cache_creation_tokens", "cache_write_tokens"))
	total := int64Value(lookup(values, "total_tokens"))
	if total == 0 {
		total = prompt + completion
	}
	return &UsageItem{
		ID:                  stringValue(lookup(values, "id")),
		RequestID:           stringValue(lookup(values, "request_id")),
		UpstreamRequestID:   stringValue(lookup(values, "upstream_request_id")),
		Model:               stringValue(lookup(values, "model")),
		UpstreamModel:       stringValue(lookup(values, "upstream_model", "upstream_model_name")),
		ReasoningEffort:     stringValue(lookup(values, "reasoning_effort")),
		PromptTokens:        prompt,
		CompletionTokens:    completion,
		CacheReadTokens:     cacheRead,
		CacheCreationTokens: cacheCreation,
		TotalTokens:         total,
		Quota:               amountValue(lookup(values, "quota")),
		TotalCost:           amountValue(lookup(values, "total_cost", "original_cost")),
		ActualCost:          amountValue(lookup(values, "actual_cost", "billed_cost")),
		RateMultiplier:      amountValue(lookup(values, "rate_multiplier")),
		ModelMultiplier:     amountValue(lookup(values, "model_multiplier", "model_ratio")),
		GroupMultiplier:     amountValue(lookup(values, "group_multiplier", "group_ratio")),
		Type:                stringValue(lookup(values, "request_type", "type")),
		BillingType:         stringValue(lookup(values, "billing_type")),
		BillingMode:         stringValue(lookup(values, "billing_mode")),
		Endpoint:            stringValue(lookup(values, "inbound_endpoint", "endpoint")),
		IPAddress:           stringValue(lookup(values, "ip_address", "client_ip")),
		APIKeyID:            firstNonEmpty(stringValue(lookup(values, "api_key_id")), stringValue(lookup(apiKey, "id"))),
		APIKeyName:          firstNonEmpty(stringValue(lookup(values, "api_key_name")), stringValue(lookup(apiKey, "name"))),
		GroupID:             firstNonEmpty(stringValue(lookup(values, "group_id")), stringValue(lookup(group, "id"))),
		GroupName:           firstNonEmpty(stringValue(lookup(values, "group_name")), stringValue(lookup(group, "name"))),
		DurationMS:          int64Value(lookup(values, "duration_ms", "latency_ms")),
		FirstTokenMS:        int64Value(lookup(values, "first_token_ms", "first_token_latency_ms")),
		StatusCode:          intValue(lookup(values, "status_code")),
		Stream:              boolValue(lookup(values, "stream", "is_stream")),
		CreatedAt:           timestampValue(lookup(values, "created_at")),
	}
}

func responseItems(data any, keys ...string) []any {
	if direct := array(data); direct != nil {
		return direct
	}
	container := object(data)
	for _, key := range keys {
		if items := array(lookup(container, key)); items != nil {
			return items
		}
	}
	return nil
}

func bearerAuthorization(credentials Credentials) string {
	value := strings.TrimSpace(credentials.Authorization)
	if value == "" {
		value = strings.TrimSpace(credentials.AccessToken)
	}
	if value == "" {
		return ""
	}
	fields := strings.Fields(value)
	if len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") {
		return "Bearer " + fields[1]
	}
	return "Bearer " + value
}

func changedCredentials(original, current Credentials) *Credentials {
	if !credentialsChanged(original, current) {
		return nil
	}
	copy := current
	return &copy
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func setQuery(values url.Values, key, value string) {
	if value = strings.TrimSpace(value); value != "" {
		values.Set(key, value)
	}
}
