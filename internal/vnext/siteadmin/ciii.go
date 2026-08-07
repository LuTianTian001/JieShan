package siteadmin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ciiiAuthMePath  = "/api/v1/auth/me"
	ciiiProfilePath = "/api/v1/user/profile"
	ciiiRefreshPath = "/api/v1/auth/refresh"
	ciiiUsagePath   = "/api/v1/usage"
	ciiiMaxBody     = 8 << 20
)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type CiiiAdapter struct {
	client HTTPDoer
	now    func() time.Time

	refreshMu sync.Mutex
	rotations map[[32]byte]Secrets
}

func NewCiiiAdapter(client HTTPDoer) (*CiiiAdapter, error) {
	if client == nil {
		return nil, errors.New("Ciii adapter requires a secured HTTP client")
	}
	return &CiiiAdapter{
		client:    client,
		now:       time.Now,
		rotations: make(map[[32]byte]Secrets),
	}, nil
}

func (*CiiiAdapter) Kind() string { return "ciii" }

func (*CiiiAdapter) Capabilities() Capabilities {
	return Capabilities{SessionRefresh: true, Balance: true, Usage: true}
}

func (adapter *CiiiAdapter) RefreshSession(ctx context.Context, connection Connection) (SessionUpdate, error) {
	current := adapter.resolveRotation(connection.Secrets)
	next, err := adapter.refresh(ctx, connection.Origin, current)
	if err != nil {
		return SessionUpdate{}, err
	}
	return SessionUpdate{Secrets: next, Changed: !sameSecrets(connection.Secrets, next), RefreshedAt: adapter.now().UTC()}, nil
}

func (adapter *CiiiAdapter) ReadBalance(ctx context.Context, connection Connection) (BalanceSnapshot, *SessionUpdate, error) {
	original := connection.Secrets
	current := adapter.resolveRotation(original)
	data, status, next, err := adapter.requestData(ctx, connection.Origin, current, http.MethodGet, ciiiAuthMePath, nil, nil)
	if status == http.StatusNotFound {
		data, _, next, err = adapter.requestData(ctx, connection.Origin, next, http.MethodGet, ciiiProfilePath, nil, nil)
	}
	update := adapter.changedSession(original, next)
	if err != nil {
		return BalanceSnapshot{}, update, err
	}
	profile, ok := data.(map[string]any)
	if !ok {
		return BalanceSnapshot{}, update, errors.New("Ciii balance response is malformed")
	}
	available := exactValue(firstValue(profile, "balance", "available_balance", "quota"))
	if available == "" {
		return BalanceSnapshot{}, update, errors.New("Ciii balance response has no exact balance")
	}
	unit := textValue(firstValue(profile, "currency", "unit"))
	if unit == "" {
		unit = "USD"
	}
	snapshot := BalanceSnapshot{
		AccountID:   textValue(firstValue(profile, "id", "user_id")),
		AccountName: textValue(firstValue(profile, "username", "email", "name")),
		Available:   Amount{Value: available, Unit: unit},
		CapturedAt:  adapter.now().UTC(),
	}
	if used := exactValue(firstValue(profile, "used_balance", "total_used", "used")); used != "" {
		snapshot.Used = &Amount{Value: used, Unit: unit}
	}
	if err := snapshot.Validate(); err != nil {
		return BalanceSnapshot{}, update, fmt.Errorf("Ciii balance response: %w", err)
	}
	return snapshot, update, nil
}

func (adapter *CiiiAdapter) ReadUsage(ctx context.Context, connection Connection, query UsageQuery) (UsagePage, *SessionUpdate, error) {
	if err := query.Validate(); err != nil {
		return UsagePage{}, nil, err
	}
	page, err := parseCiiiCursor(query.Cursor)
	if err != nil {
		return UsagePage{}, nil, err
	}
	values := url.Values{
		"page":      []string{strconv.Itoa(page)},
		"page_size": []string{strconv.Itoa(query.Limit)},
		"timezone":  []string{"UTC"},
	}
	setURLValue(values, "model", query.Model)
	setURLValue(values, "api_key_id", query.APIKey)
	setURLValue(values, "request_id", query.RequestID)
	setURLValue(values, "status", query.Status)
	if !query.From.IsZero() {
		values.Set("start_date", query.From.UTC().Format("2006-01-02"))
	}
	if !query.To.IsZero() {
		values.Set("end_date", query.To.UTC().Format("2006-01-02"))
	}

	original := connection.Secrets
	current := adapter.resolveRotation(original)
	data, _, next, err := adapter.requestData(ctx, connection.Origin, current, http.MethodGet, ciiiUsagePath, values, nil)
	update := adapter.changedSession(original, next)
	if err != nil {
		return UsagePage{}, update, err
	}
	container, ok := data.(map[string]any)
	if !ok {
		return UsagePage{}, update, errors.New("Ciii usage response is malformed")
	}
	rawItems, ok := firstValue(container, "items").([]any)
	if !ok {
		return UsagePage{}, update, errors.New("Ciii usage response has no item list")
	}
	records := make([]UsageRecord, 0, len(rawItems))
	for index, rawItem := range rawItems {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return UsagePage{}, update, fmt.Errorf("Ciii usage item %d is malformed", index)
		}
		record, err := adapter.parseUsageRecord(item)
		if err != nil {
			return UsagePage{}, update, fmt.Errorf("Ciii usage item %d: %w", index, err)
		}
		records = append(records, record)
	}
	pages := intValue(firstValue(container, "pages"))
	total := int64Value(firstValue(container, "total"))
	hasMore := (pages > 0 && page < pages) || (pages == 0 && int64(page*query.Limit) < total) ||
		(pages == 0 && total == 0 && len(records) == query.Limit)
	nextCursor := ""
	if hasMore {
		nextCursor = strconv.Itoa(page + 1)
	}
	return UsagePage{
		Records:    records,
		NextCursor: nextCursor,
		HasMore:    hasMore,
		FetchedAt:  adapter.now().UTC(),
	}, update, nil
}

func (adapter *CiiiAdapter) parseUsageRecord(item map[string]any) (UsageRecord, error) {
	occurredAt, err := timestampValue(firstValue(item, "created_at", "createdAt", "timestamp"))
	if err != nil {
		return UsageRecord{}, err
	}
	apiKey, _ := firstValue(item, "api_key").(map[string]any)
	record := UsageRecord{
		RemoteID:          textValue(firstValue(item, "id")),
		RequestID:         textValue(firstValue(item, "request_id", "requestId")),
		UpstreamRequestID: textValue(firstValue(item, "upstream_request_id", "upstreamRequestId")),
		OccurredAt:        occurredAt,
		Model:             textValue(firstValue(item, "model")),
		UpstreamModel:     textValue(firstValue(item, "upstream_model", "upstream_model_name")),
		Status:            textValue(firstValue(item, "status", "state")),
		HTTPStatus:        parseOptionalInt(firstValue(item, "status_code", "http_status")),
		DurationMS:        parseOptionalInt64(firstValue(item, "duration_ms", "latency_ms")),
		APIKeyName:        firstNonEmpty(textValue(firstValue(item, "api_key_name")), textValue(firstValue(apiKey, "name"))),
		Tokens: TokenUsage{
			Input:      parseOptionalInt64(firstValue(item, "input_tokens", "prompt_tokens")),
			Output:     parseOptionalInt64(firstValue(item, "output_tokens", "completion_tokens")),
			CacheRead:  parseOptionalInt64(firstValue(item, "cache_read_tokens", "cached_tokens")),
			CacheWrite: parseOptionalInt64(firstValue(item, "cache_creation_tokens", "cache_write_tokens")),
			Reasoning:  parseOptionalInt64(firstValue(item, "reasoning_tokens")),
			Total:      parseOptionalInt64(firstValue(item, "total_tokens")),
		},
	}
	charge := exactValue(firstValue(item, "actual_cost", "billed_cost", "total_cost", "quota"))
	if charge != "" {
		unit := textValue(firstValue(item, "currency", "unit"))
		if unit == "" {
			unit = "USD"
		}
		record.Charge = &Amount{Value: charge, Unit: unit}
	}
	if err := record.Validate(); err != nil {
		return UsageRecord{}, err
	}
	return record, nil
}

func (adapter *CiiiAdapter) requestData(
	ctx context.Context,
	origin string,
	secrets Secrets,
	method, path string,
	query url.Values,
	payload any,
) (any, int, Secrets, error) {
	current := adapter.resolveRotation(secrets)
	status, body, err := adapter.do(ctx, origin, current, method, path, query, payload, path != ciiiRefreshPath)
	if err != nil {
		return nil, status, current, err
	}
	if status == http.StatusUnauthorized && path != ciiiRefreshPath && strings.TrimSpace(current.RefreshToken) != "" {
		current, err = adapter.refresh(ctx, origin, current)
		if err != nil {
			return nil, status, current, err
		}
		status, body, err = adapter.do(ctx, origin, current, method, path, query, payload, true)
		if err != nil {
			return nil, status, current, err
		}
	}
	data, err := unwrapCiii(status, body)
	return data, status, current, err
}

func (adapter *CiiiAdapter) refresh(ctx context.Context, origin string, current Secrets) (Secrets, error) {
	refreshToken := strings.TrimSpace(current.RefreshToken)
	if refreshToken == "" {
		return Secrets{}, errors.New("Ciii refresh token is required")
	}
	adapter.refreshMu.Lock()
	defer adapter.refreshMu.Unlock()
	key := sha256.Sum256([]byte(refreshToken))
	if rotated, ok := adapter.rotations[key]; ok {
		return rotated, nil
	}
	status, body, err := adapter.do(ctx, origin, current, http.MethodPost, ciiiRefreshPath, nil,
		map[string]string{"refresh_token": refreshToken}, false)
	if err != nil {
		return Secrets{}, err
	}
	data, err := unwrapCiiiOrDirect(status, body)
	if err != nil {
		return Secrets{}, err
	}
	object, ok := data.(map[string]any)
	if !ok {
		return Secrets{}, errors.New("Ciii refresh response is malformed")
	}
	accessToken := textValue(firstValue(object, "access_token", "accessToken"))
	if accessToken == "" {
		return Secrets{}, errors.New("Ciii refresh response has no access token")
	}
	next := current
	next.AccessToken = accessToken
	next.Authorization = "Bearer " + accessToken
	if value := textValue(firstValue(object, "refresh_token", "refreshToken")); value != "" {
		next.RefreshToken = value
	}
	if expiresAt, parseErr := refreshExpiry(object, adapter.now().UTC()); parseErr == nil {
		next.ExpiresAt = expiresAt
	}
	adapter.rotations[key] = next
	return next, nil
}

func (adapter *CiiiAdapter) do(
	ctx context.Context,
	origin string,
	secrets Secrets,
	method, path string,
	query url.Values,
	payload any,
	authenticated bool,
) (int, []byte, error) {
	requestURL, err := siteAdminURL(origin, path, query)
	if err != nil {
		return 0, nil, err
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, errors.New("encode Ciii request")
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return 0, nil, errors.New("create Ciii request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "JieShan/vnext-siteadmin")
	request.Header.Set("X-User-UI-Request", "1")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		authorization := bearerValue(secrets)
		if authorization == "" {
			return 0, nil, errors.New("Ciii access token is required")
		}
		request.Header.Set("Authorization", authorization)
	}
	response, err := adapter.client.Do(request)
	if err != nil {
		return 0, nil, errors.New("Ciii request failed")
	}
	if response == nil || response.Body == nil {
		return 0, nil, errors.New("Ciii response is empty")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, ciiiMaxBody+1))
	if err != nil {
		return response.StatusCode, nil, errors.New("read Ciii response")
	}
	if len(data) > ciiiMaxBody {
		return response.StatusCode, nil, errors.New("Ciii response exceeds size limit")
	}
	return response.StatusCode, data, nil
}

func (adapter *CiiiAdapter) resolveRotation(secrets Secrets) Secrets {
	adapter.refreshMu.Lock()
	defer adapter.refreshMu.Unlock()
	current := secrets
	for range 8 {
		refreshToken := strings.TrimSpace(current.RefreshToken)
		if refreshToken == "" {
			break
		}
		next, ok := adapter.rotations[sha256.Sum256([]byte(refreshToken))]
		if !ok || sameSecrets(current, next) {
			break
		}
		current = next
	}
	return current
}

func (adapter *CiiiAdapter) changedSession(original, current Secrets) *SessionUpdate {
	if sameSecrets(original, current) {
		return nil
	}
	return &SessionUpdate{Secrets: current, Changed: true, RefreshedAt: adapter.now().UTC()}
}

type ciiiRemoteError struct {
	Status int
	Code   string
}

func (err ciiiRemoteError) Error() string {
	return fmt.Sprintf("Ciii rejected the request with HTTP %d", err.Status)
}

func unwrapCiii(status int, body []byte) (any, error) {
	decoded, err := decodeJSONValue(body)
	if err != nil {
		return nil, errors.New("Ciii returned invalid JSON")
	}
	envelope, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("Ciii response is malformed")
	}
	codeValue, hasCode := envelope["code"]
	if status < 200 || status >= 300 {
		return nil, ciiiRemoteError{Status: status, Code: textValue(codeValue)}
	}
	if !hasCode || textValue(codeValue) != "0" {
		return nil, ciiiRemoteError{Status: status, Code: textValue(codeValue)}
	}
	return envelope["data"], nil
}

func unwrapCiiiOrDirect(status int, body []byte) (any, error) {
	decoded, err := decodeJSONValue(body)
	if err != nil {
		return nil, errors.New("Ciii returned invalid JSON")
	}
	if status < 200 || status >= 300 {
		object, _ := decoded.(map[string]any)
		return nil, ciiiRemoteError{Status: status, Code: textValue(firstValue(object, "code"))}
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("Ciii refresh response is malformed")
	}
	if _, hasCode := object["code"]; !hasCode {
		return object, nil
	}
	return unwrapCiii(status, body)
}

func siteAdminURL(origin, path string, query url.Values) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("site administration origin is invalid")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("site administration origin must use HTTP or HTTPS")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + strings.TrimLeft(path, "/")
	parsed.RawPath = ""
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func decodeJSONValue(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("multiple JSON values")
	}
	return value, nil
}

func firstValue(values map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, exists := values[key]; exists && value != nil {
			return value
		}
	}
	return nil
}

func textValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return ""
	}
}

func exactValue(value any) string {
	text := textValue(value)
	if text == "" || !decimalPattern.MatchString(text) {
		return ""
	}
	return text
}

func int64Value(value any) int64 {
	parsed, _ := strconv.ParseInt(textValue(value), 10, 64)
	return parsed
}

func intValue(value any) int {
	parsed, _ := strconv.Atoi(textValue(value))
	return parsed
}

func parseOptionalInt(value any) *int {
	text := textValue(value)
	if text == "" {
		return nil
	}
	parsed, err := strconv.Atoi(text)
	if err != nil {
		return nil
	}
	return &parsed
}

func parseOptionalInt64(value any) *int64 {
	text := textValue(value)
	if text == "" {
		return nil
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return nil
	}
	return &parsed
}

func timestampValue(value any) (time.Time, error) {
	text := textValue(value)
	if text == "" {
		return time.Time{}, errors.New("usage timestamp is missing")
	}
	if integer, err := strconv.ParseInt(text, 10, 64); err == nil {
		if integer > 10_000_000_000 {
			return time.UnixMilli(integer).UTC(), nil
		}
		return time.Unix(integer, 0).UTC(), nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, errors.New("usage timestamp is invalid")
}

func refreshExpiry(values map[string]any, now time.Time) (time.Time, error) {
	if value := firstValue(values, "token_expires_at", "tokenExpiresAt", "expires_at", "expiresAt"); value != nil {
		return timestampValue(value)
	}
	seconds := int64Value(firstValue(values, "expires_in", "expiresIn"))
	if seconds <= 0 {
		return time.Time{}, errors.New("refresh expiry is missing")
	}
	return now.Add(time.Duration(seconds) * time.Second).UTC(), nil
}

func bearerValue(secrets Secrets) string {
	value := strings.TrimSpace(secrets.AccessToken)
	if value == "" {
		value = strings.TrimSpace(secrets.Authorization)
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

func sameSecrets(left, right Secrets) bool {
	return left.Authorization == right.Authorization && left.AccessToken == right.AccessToken &&
		left.RefreshToken == right.RefreshToken && left.Cookie == right.Cookie && left.ExpiresAt.Equal(right.ExpiresAt)
}

func parseCiiiCursor(cursor string) (int, error) {
	if strings.TrimSpace(cursor) == "" {
		return 1, nil
	}
	page, err := strconv.Atoi(strings.TrimSpace(cursor))
	if err != nil || page < 1 {
		return 0, errors.New("Ciii usage cursor is invalid")
	}
	return page, nil
}

func setURLValue(values url.Values, key, value string) {
	if value = strings.TrimSpace(value); value != "" {
		values.Set(key, value)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
