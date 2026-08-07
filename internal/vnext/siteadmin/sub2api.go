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
	sub2APIAdapterKind = "sub2api"
	sub2APIRefreshPath = "/api/v1/auth/refresh"
	sub2APISelfPath    = "/api/v1/auth/me"
	sub2APIUsagePath   = "/api/v1/usage"
	sub2APIMaxBody     = 8 << 20
)

// Sub2APIAdapter implements only the user management endpoints declared by
// Wei-Shaw/sub2api. It intentionally shares no Ciii endpoint assumptions.
type Sub2APIAdapter struct {
	client HTTPDoer
	now    func() time.Time

	refreshMu sync.Mutex
	rotations map[[32]byte]Secrets
}

func NewSub2APIAdapter(client HTTPDoer) (*Sub2APIAdapter, error) {
	if client == nil {
		return nil, errors.New("Sub2API adapter requires a secured HTTP client")
	}
	return &Sub2APIAdapter{client: client, now: time.Now, rotations: make(map[[32]byte]Secrets)}, nil
}

func (*Sub2APIAdapter) Kind() string { return sub2APIAdapterKind }

func (*Sub2APIAdapter) Capabilities() Capabilities {
	return Capabilities{SessionRefresh: true, Balance: true, Usage: true}
}

func (adapter *Sub2APIAdapter) RefreshSession(ctx context.Context, connection Connection) (SessionUpdate, error) {
	current := adapter.resolveRotation(connection.Secrets)
	next, err := adapter.refresh(ctx, connection.Origin, current)
	if err != nil {
		return SessionUpdate{}, err
	}
	return SessionUpdate{
		Secrets: next, Changed: !sameSecrets(connection.Secrets, next), RefreshedAt: adapter.now().UTC(),
	}, nil
}

func (adapter *Sub2APIAdapter) ReadBalance(
	ctx context.Context,
	connection Connection,
) (BalanceSnapshot, *SessionUpdate, error) {
	original := connection.Secrets
	data, _, next, err := adapter.requestData(ctx, connection.Origin, original, sub2APISelfPath, nil)
	update := adapter.changedSession(original, next)
	if err != nil {
		return BalanceSnapshot{}, update, err
	}
	profile, ok := data.(map[string]any)
	if !ok {
		return BalanceSnapshot{}, update, errors.New("Sub2API balance response is malformed")
	}
	available := exactValue(firstValue(profile, "balance"))
	if available == "" {
		return BalanceSnapshot{}, update, errors.New("Sub2API balance response has no exact balance")
	}
	snapshot := BalanceSnapshot{
		AccountID:   textValue(firstValue(profile, "id")),
		AccountName: firstNonEmpty(textValue(firstValue(profile, "username")), textValue(firstValue(profile, "email"))),
		Available:   Amount{Value: available, Unit: "USD"},
		CapturedAt:  adapter.now().UTC(),
	}
	if err := snapshot.Validate(); err != nil {
		return BalanceSnapshot{}, update, fmt.Errorf("Sub2API balance response: %w", err)
	}
	return snapshot, update, nil
}

func (adapter *Sub2APIAdapter) ReadUsage(
	ctx context.Context,
	connection Connection,
	query UsageQuery,
) (UsagePage, *SessionUpdate, error) {
	if err := query.Validate(); err != nil {
		return UsagePage{}, nil, err
	}
	page, err := parseSub2APICursor(query.Cursor)
	if err != nil {
		return UsagePage{}, nil, err
	}
	values := url.Values{
		"page":       []string{strconv.Itoa(page)},
		"page_size":  []string{strconv.Itoa(query.Limit)},
		"sort_by":    []string{"created_at"},
		"sort_order": []string{"desc"},
		"timezone":   []string{"UTC"},
	}
	setURLValue(values, "model", query.Model)
	if apiKeyID, parseErr := strconv.ParseInt(strings.TrimSpace(query.APIKey), 10, 64); parseErr == nil && apiKeyID > 0 {
		values.Set("api_key_id", strconv.FormatInt(apiKeyID, 10))
	}
	if !query.From.IsZero() {
		values.Set("start_date", query.From.UTC().Format("2006-01-02"))
	}
	if !query.To.IsZero() {
		values.Set("end_date", query.To.UTC().Format("2006-01-02"))
	}

	original := connection.Secrets
	data, _, next, err := adapter.requestData(ctx, connection.Origin, original, sub2APIUsagePath, values)
	update := adapter.changedSession(original, next)
	if err != nil {
		return UsagePage{}, update, err
	}
	container, ok := data.(map[string]any)
	if !ok {
		return UsagePage{}, update, errors.New("Sub2API usage response is malformed")
	}
	rawItems, ok := firstValue(container, "items").([]any)
	if !ok {
		return UsagePage{}, update, errors.New("Sub2API usage response has no item list")
	}
	records := make([]UsageRecord, 0, len(rawItems))
	for index, raw := range rawItems {
		item, ok := raw.(map[string]any)
		if !ok {
			return UsagePage{}, update, fmt.Errorf("Sub2API usage item %d is malformed", index)
		}
		record, parseErr := adapter.parseUsageRecord(item)
		if parseErr != nil {
			return UsagePage{}, update, fmt.Errorf("Sub2API usage item %d: %w", index, parseErr)
		}
		records = append(records, record)
	}
	responsePage := intValue(firstValue(container, "page"))
	if responsePage > 0 {
		page = responsePage
	}
	pageSize := intValue(firstValue(container, "page_size"))
	if pageSize <= 0 {
		pageSize = query.Limit
	}
	pages := intValue(firstValue(container, "pages"))
	total := int64Value(firstValue(container, "total"))
	hasMore := (pages > 0 && page < pages) || (pages == 0 && total > int64(page*pageSize)) ||
		(pages == 0 && total == 0 && len(records) == pageSize)
	nextCursor := ""
	if hasMore {
		nextCursor = strconv.Itoa(page + 1)
	}
	return UsagePage{
		Records: records, NextCursor: nextCursor, HasMore: hasMore, FetchedAt: adapter.now().UTC(),
	}, update, nil
}

func (adapter *Sub2APIAdapter) parseUsageRecord(item map[string]any) (UsageRecord, error) {
	occurredAt, err := timestampValue(firstValue(item, "created_at"))
	if err != nil {
		return UsageRecord{}, err
	}
	input := parseOptionalInt64(firstValue(item, "input_tokens"))
	output := parseOptionalInt64(firstValue(item, "output_tokens"))
	total := int64(0)
	hasTotal := false
	if input != nil {
		total += *input
		hasTotal = true
	}
	if output != nil {
		total += *output
		hasTotal = true
	}
	var totalPointer *int64
	if hasTotal {
		totalPointer = &total
	}
	apiKey, _ := firstValue(item, "api_key").(map[string]any)
	record := UsageRecord{
		RemoteID:   textValue(firstValue(item, "id")),
		RequestID:  textValue(firstValue(item, "request_id")),
		OccurredAt: occurredAt,
		Model:      textValue(firstValue(item, "model")),
		DurationMS: parseOptionalInt64(firstValue(item, "duration_ms")),
		APIKeyName: textValue(firstValue(apiKey, "name")),
		Tokens: TokenUsage{
			Input: input, Output: output,
			CacheRead:  parseOptionalInt64(firstValue(item, "cache_read_tokens")),
			CacheWrite: parseOptionalInt64(firstValue(item, "cache_creation_tokens")),
			Total:      totalPointer,
		},
	}
	if charge := exactValue(firstValue(item, "actual_cost")); charge != "" {
		record.Charge = &Amount{Value: charge, Unit: "USD"}
	}
	if err := record.Validate(); err != nil {
		return UsageRecord{}, err
	}
	return record, nil
}

func (adapter *Sub2APIAdapter) requestData(
	ctx context.Context,
	origin string,
	secrets Secrets,
	path string,
	query url.Values,
) (any, int, Secrets, error) {
	current := adapter.resolveRotation(secrets)
	status, body, err := adapter.do(ctx, origin, current, http.MethodGet, path, query, nil, true)
	if err != nil {
		return nil, status, current, err
	}
	if status == http.StatusUnauthorized && strings.TrimSpace(current.RefreshToken) != "" {
		current, err = adapter.refresh(ctx, origin, current)
		if err != nil {
			return nil, status, current, err
		}
		status, body, err = adapter.do(ctx, origin, current, http.MethodGet, path, query, nil, true)
		if err != nil {
			return nil, status, current, err
		}
	}
	data, err := unwrapSub2API(status, body)
	return data, status, current, err
}

func (adapter *Sub2APIAdapter) refresh(ctx context.Context, origin string, current Secrets) (Secrets, error) {
	refreshToken := strings.TrimSpace(current.RefreshToken)
	if refreshToken == "" {
		return Secrets{}, errors.New("Sub2API refresh token is required")
	}
	adapter.refreshMu.Lock()
	defer adapter.refreshMu.Unlock()
	key := sha256.Sum256([]byte(refreshToken))
	if rotated, ok := adapter.rotations[key]; ok {
		return rotated, nil
	}
	status, body, err := adapter.do(ctx, origin, current, http.MethodPost, sub2APIRefreshPath, nil,
		map[string]string{"refresh_token": refreshToken}, false)
	if err != nil {
		return Secrets{}, err
	}
	data, err := unwrapSub2API(status, body)
	if err != nil {
		return Secrets{}, err
	}
	object, ok := data.(map[string]any)
	if !ok {
		return Secrets{}, errors.New("Sub2API refresh response is malformed")
	}
	accessToken := textValue(firstValue(object, "access_token"))
	if accessToken == "" {
		return Secrets{}, errors.New("Sub2API refresh response has no access token")
	}
	next := current
	next.AccessToken = accessToken
	next.Authorization = "Bearer " + accessToken
	if value := textValue(firstValue(object, "refresh_token")); value != "" {
		next.RefreshToken = value
	}
	if seconds := int64Value(firstValue(object, "expires_in")); seconds > 0 {
		next.ExpiresAt = adapter.now().UTC().Add(time.Duration(seconds) * time.Second)
	}
	adapter.rotations[key] = next
	return next, nil
}

func (adapter *Sub2APIAdapter) do(
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
			return 0, nil, errors.New("encode Sub2API request")
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return 0, nil, errors.New("create Sub2API request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "JieShan/vnext-siteadmin")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		authorization := bearerValue(secrets)
		if authorization == "" {
			return 0, nil, errors.New("Sub2API access token is required")
		}
		request.Header.Set("Authorization", authorization)
	}
	response, err := adapter.client.Do(request)
	if err != nil {
		return 0, nil, errors.New("Sub2API request failed")
	}
	if response == nil || response.Body == nil {
		return 0, nil, errors.New("Sub2API response is empty")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, sub2APIMaxBody+1))
	if err != nil {
		return response.StatusCode, nil, errors.New("read Sub2API response")
	}
	if len(data) > sub2APIMaxBody {
		return response.StatusCode, nil, errors.New("Sub2API response exceeds size limit")
	}
	return response.StatusCode, data, nil
}

func (adapter *Sub2APIAdapter) resolveRotation(secrets Secrets) Secrets {
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

func (adapter *Sub2APIAdapter) changedSession(original, current Secrets) *SessionUpdate {
	if sameSecrets(original, current) {
		return nil
	}
	return &SessionUpdate{Secrets: current, Changed: true, RefreshedAt: adapter.now().UTC()}
}

type sub2APIRemoteError struct {
	Status int
	Code   string
}

func (err sub2APIRemoteError) Error() string {
	return fmt.Sprintf("Sub2API rejected the request with HTTP %d", err.Status)
}

func unwrapSub2API(status int, body []byte) (any, error) {
	decoded, err := decodeJSONValue(body)
	if err != nil {
		return nil, errors.New("Sub2API returned invalid JSON")
	}
	envelope, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("Sub2API response is malformed")
	}
	code := textValue(firstValue(envelope, "code"))
	if status < http.StatusOK || status >= http.StatusMultipleChoices || code != "0" {
		return nil, sub2APIRemoteError{Status: status, Code: code}
	}
	return envelope["data"], nil
}

func parseSub2APICursor(cursor string) (int, error) {
	if strings.TrimSpace(cursor) == "" {
		return 1, nil
	}
	page, err := strconv.Atoi(strings.TrimSpace(cursor))
	if err != nil || page < 1 {
		return 0, errors.New("Sub2API usage cursor is invalid")
	}
	return page, nil
}

var _ SessionRefresher = (*Sub2APIAdapter)(nil)
var _ BalanceReader = (*Sub2APIAdapter)(nil)
var _ UsageReader = (*Sub2APIAdapter)(nil)
