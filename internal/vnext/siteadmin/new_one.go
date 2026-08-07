package siteadmin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	newAPIAdapterKind     = "new_api"
	oneAPIAdapterKind     = "one_api"
	newOneSelfPath        = "/api/user/self"
	newOneUsagePath       = "/api/log/self"
	newOneMaxBody         = 8 << 20
	oneAPIDefaultPageSize = 10
)

// NewOneAdapter implements only the stable management surface shared by New
// API and One API: exact upstream balance and raw usage metadata. It does not
// import subscription, check-in, or inference-token behavior from legacy code.
type NewOneAdapter struct {
	kind   string
	client HTTPDoer
	now    func() time.Time
}

func NewNewAPIAdapter(client HTTPDoer) (*NewOneAdapter, error) {
	return newNewOneAdapter(newAPIAdapterKind, client)
}

func NewOneAPIAdapter(client HTTPDoer) (*NewOneAdapter, error) {
	return newNewOneAdapter(oneAPIAdapterKind, client)
}

func newNewOneAdapter(kind string, client HTTPDoer) (*NewOneAdapter, error) {
	if client == nil {
		return nil, errors.New("New API/One API adapter requires a secured HTTP client")
	}
	return &NewOneAdapter{kind: kind, client: client, now: time.Now}, nil
}

func (adapter *NewOneAdapter) Kind() string { return adapter.kind }

func (*NewOneAdapter) Capabilities() Capabilities {
	return Capabilities{Balance: true, Usage: true}
}

func (adapter *NewOneAdapter) ReadBalance(
	ctx context.Context,
	connection Connection,
) (BalanceSnapshot, *SessionUpdate, error) {
	data, err := adapter.requestData(ctx, connection, newOneSelfPath, nil)
	if err != nil {
		return BalanceSnapshot{}, nil, err
	}
	profile, ok := data.(map[string]any)
	if !ok {
		return BalanceSnapshot{}, nil, errors.New("New API/One API balance response is malformed")
	}
	available := exactValue(firstValue(profile, "quota", "balance", "available_quota"))
	if available == "" {
		return BalanceSnapshot{}, nil, errors.New("New API/One API balance response has no exact quota")
	}
	snapshot := BalanceSnapshot{
		AccountID:   textValue(firstValue(profile, "id", "user_id")),
		AccountName: textValue(firstValue(profile, "username", "display_name", "email")),
		Available:   Amount{Value: available, Unit: "quota"},
		CapturedAt:  adapter.now().UTC(),
	}
	if used := exactValue(firstValue(profile, "used_quota", "used")); used != "" {
		snapshot.Used = &Amount{Value: used, Unit: "quota"}
	}
	if err := snapshot.Validate(); err != nil {
		return BalanceSnapshot{}, nil, fmt.Errorf("New API/One API balance response: %w", err)
	}
	return snapshot, nil, nil
}

func (adapter *NewOneAdapter) ReadUsage(
	ctx context.Context,
	connection Connection,
	query UsageQuery,
) (UsagePage, *SessionUpdate, error) {
	if err := query.Validate(); err != nil {
		return UsagePage{}, nil, err
	}
	page, err := parseNewOneCursor(query.Cursor)
	if err != nil {
		return UsagePage{}, nil, err
	}
	pageSize := query.Limit
	values := url.Values{}
	if adapter.kind == oneAPIAdapterKind {
		values.Set("p", strconv.Itoa(page-1))
		pageSize = oneAPIDefaultPageSize
	} else {
		values.Set("p", strconv.Itoa(page))
		values.Set("page_size", strconv.Itoa(pageSize))
	}
	if !query.From.IsZero() {
		values.Set("start_timestamp", strconv.FormatInt(query.From.UTC().Unix(), 10))
	}
	if !query.To.IsZero() {
		values.Set("end_timestamp", strconv.FormatInt(query.To.UTC().Unix(), 10))
	}
	setURLValue(values, "model_name", query.Model)
	setURLValue(values, "token_name", query.APIKey)
	setURLValue(values, "request_id", query.RequestID)
	if status := strings.TrimSpace(query.Status); status != "" {
		if _, parseErr := strconv.Atoi(status); parseErr == nil {
			values.Set("type", status)
		} else {
			values.Set("status", status)
		}
	}

	data, err := adapter.requestData(ctx, connection, newOneUsagePath, values)
	if err != nil {
		return UsagePage{}, nil, err
	}
	rawItems, total, responsePage, responsePageSize, err := newOneUsageItems(data)
	if err != nil {
		return UsagePage{}, nil, err
	}
	if responsePage > 0 {
		page = responsePage
	}
	if responsePageSize > 0 {
		pageSize = responsePageSize
	}
	records := make([]UsageRecord, 0, len(rawItems))
	for index, raw := range rawItems {
		item, ok := raw.(map[string]any)
		if !ok {
			return UsagePage{}, nil, fmt.Errorf("New API/One API usage item %d is malformed", index)
		}
		record, parseErr := adapter.parseUsageRecord(item)
		if parseErr != nil {
			return UsagePage{}, nil, fmt.Errorf("New API/One API usage item %d: %w", index, parseErr)
		}
		records = append(records, record)
	}
	hasMore := total > int64(page*pageSize) || (total == 0 && len(records) == pageSize)
	nextCursor := ""
	if hasMore {
		nextCursor = strconv.Itoa(page + 1)
	}
	return UsagePage{
		Records: records, NextCursor: nextCursor, HasMore: hasMore, FetchedAt: adapter.now().UTC(),
	}, nil, nil
}

func (adapter *NewOneAdapter) parseUsageRecord(item map[string]any) (UsageRecord, error) {
	other := newOneOther(firstValue(item, "other"))
	occurredAt, err := timestampValue(firstValue(item, "created_at", "createdAt", "timestamp"))
	if err != nil {
		return UsageRecord{}, err
	}
	input := parseOptionalInt64(firstValue(item, "prompt_tokens", "input_tokens"))
	output := parseOptionalInt64(firstValue(item, "completion_tokens", "output_tokens"))
	total := parseOptionalInt64(firstValue(item, "total_tokens"))
	if total == nil && (input != nil || output != nil) {
		value := int64(0)
		if input != nil {
			value += *input
		}
		if output != nil {
			value += *output
		}
		total = &value
	}
	duration := parseOptionalInt64(firstValue(item, "duration_ms", "elapsed_time"))
	if adapter.kind == newAPIAdapterKind {
		if seconds := parseOptionalInt64(firstValue(item, "use_time")); seconds != nil {
			value := *seconds * 1000
			duration = &value
		}
	}
	record := UsageRecord{
		RemoteID:          textValue(firstValue(item, "id")),
		RequestID:         textValue(firstValue(item, "request_id")),
		UpstreamRequestID: textValue(firstValue(item, "upstream_request_id")),
		OccurredAt:        occurredAt,
		Model:             textValue(firstValue(item, "model_name", "model")),
		UpstreamModel:     textValue(firstValue(other, "upstream_model_name", "upstream_model")),
		Status:            textValue(firstValue(item, "status", "type")),
		HTTPStatus:        parseOptionalInt(firstValue(item, "status_code", "http_status")),
		DurationMS:        duration,
		APIKeyName:        textValue(firstValue(item, "token_name", "api_key_name")),
		Tokens: TokenUsage{
			Input: input, Output: output,
			CacheRead:  parseOptionalInt64(firstValue(other, "cache_tokens", "cache_read_tokens")),
			CacheWrite: parseOptionalInt64(firstValue(other, "cache_creation_tokens", "cache_write_tokens")),
			Reasoning:  parseOptionalInt64(firstValue(other, "reasoning_tokens")),
			Total:      total,
		},
	}
	if quota := exactValue(firstValue(item, "quota", "actual_cost", "total_cost")); quota != "" {
		record.Charge = &Amount{Value: quota, Unit: "quota"}
	}
	if err := record.Validate(); err != nil {
		return UsageRecord{}, err
	}
	return record, nil
}

func (adapter *NewOneAdapter) requestData(
	ctx context.Context,
	connection Connection,
	path string,
	query url.Values,
) (any, error) {
	requestURL, err := siteAdminURL(connection.Origin, path, query)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, errors.New("create New API/One API request")
	}
	authorization := adapter.authorization(connection.Secrets)
	if authorization == "" {
		return nil, errors.New("New API/One API management authorization is required")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", authorization)
	request.Header.Set("User-Agent", "JieShan/vnext-siteadmin")
	request.Header.Set("X-User-UI-Request", "1")
	if cookie := strings.TrimSpace(connection.Secrets.Cookie); cookie != "" {
		request.Header.Set("Cookie", cookie)
	}
	response, err := adapter.client.Do(request)
	if err != nil {
		return nil, errors.New("New API/One API request failed")
	}
	if response == nil || response.Body == nil {
		return nil, errors.New("New API/One API response is empty")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, newOneMaxBody+1))
	if err != nil {
		return nil, errors.New("read New API/One API response")
	}
	if len(body) > newOneMaxBody {
		return nil, errors.New("New API/One API response exceeds size limit")
	}
	return unwrapNewOne(response.StatusCode, body)
}

func (adapter *NewOneAdapter) authorization(secrets Secrets) string {
	value := strings.TrimSpace(secrets.Authorization)
	if value == "" {
		value = strings.TrimSpace(secrets.AccessToken)
	}
	if value == "" {
		return ""
	}
	if adapter.kind == newAPIAdapterKind {
		fields := strings.Fields(value)
		if len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") {
			return "Bearer " + fields[1]
		}
		return "Bearer " + value
	}
	return value
}

func unwrapNewOne(status int, body []byte) (any, error) {
	decoded, err := decodeJSONValue(body)
	if err != nil {
		return nil, errors.New("New API/One API returned invalid JSON")
	}
	envelope, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("New API/One API response is malformed")
	}
	success, hasSuccess := envelope["success"].(bool)
	if status < http.StatusOK || status >= http.StatusMultipleChoices || !hasSuccess || !success {
		return nil, newOneRemoteError{Status: status, Code: textValue(firstValue(envelope, "code"))}
	}
	return envelope["data"], nil
}

type newOneRemoteError struct {
	Status int
	Code   string
}

func (err newOneRemoteError) Error() string {
	return fmt.Sprintf("New API/One API rejected the request with HTTP %d", err.Status)
}

func newOneUsageItems(data any) ([]any, int64, int, int, error) {
	if items, ok := data.([]any); ok {
		return items, 0, 0, 0, nil
	}
	container, ok := data.(map[string]any)
	if !ok {
		return nil, 0, 0, 0, errors.New("New API/One API usage response is malformed")
	}
	items, ok := firstValue(container, "items", "logs", "records").([]any)
	if !ok {
		return nil, 0, 0, 0, errors.New("New API/One API usage response has no item list")
	}
	return items, int64Value(firstValue(container, "total")), intValue(firstValue(container, "page")),
		intValue(firstValue(container, "page_size", "pageSize")), nil
}

func newOneOther(value any) map[string]any {
	if direct, ok := value.(map[string]any); ok {
		return direct
	}
	text := textValue(value)
	if text == "" {
		return nil
	}
	decoded, err := decodeJSONValue(bytes.TrimSpace([]byte(text)))
	if err != nil {
		return nil
	}
	object, _ := decoded.(map[string]any)
	return object
}

func parseNewOneCursor(cursor string) (int, error) {
	if strings.TrimSpace(cursor) == "" {
		return 1, nil
	}
	page, err := strconv.Atoi(strings.TrimSpace(cursor))
	if err != nil || page < 1 {
		return 0, errors.New("New API/One API usage cursor is invalid")
	}
	return page, nil
}

var _ BalanceReader = (*NewOneAdapter)(nil)
var _ UsageReader = (*NewOneAdapter)(nil)
