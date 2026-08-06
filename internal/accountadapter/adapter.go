package accountadapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxResponseBytes = 8 << 20
	defaultPageSize  = 10
)

// Doer is implemented by http.Client and makes adapters straightforward to test.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

type quotaCacheEntry struct {
	value     string
	expiresAt time.Time
}

type adapter struct {
	kind Kind
	doer Doer

	refreshMu sync.Mutex
	rotations map[[32]byte]Credentials

	quotaMu    sync.Mutex
	quotaCache map[string]quotaCacheEntry
}

// New constructs an adapter for one supported upstream contract.
func New(kind Kind, doer Doer) (Adapter, error) {
	switch kind {
	case KindCiii, KindNewAPI, KindOneAPI:
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedKind, kind)
	}
	if doer == nil {
		doer = http.DefaultClient
	}
	return &adapter{
		kind:       kind,
		doer:       doer,
		rotations:  make(map[[32]byte]Credentials),
		quotaCache: make(map[string]quotaCacheEntry),
	}, nil
}

func (a *adapter) Kind() Kind { return a.kind }

func (a *adapter) Snapshot(ctx context.Context, connection Connection) (Snapshot, *Credentials, error) {
	switch a.kind {
	case KindCiii:
		return a.ciiiSnapshot(ctx, connection)
	case KindNewAPI, KindOneAPI:
		return a.newOneSnapshot(ctx, connection)
	default:
		return Snapshot{}, nil, ErrUnsupportedKind
	}
}

func (a *adapter) Subscriptions(ctx context.Context, connection Connection) ([]Subscription, *Credentials, error) {
	switch a.kind {
	case KindCiii:
		return a.ciiiSubscriptions(ctx, connection)
	case KindNewAPI:
		return a.newAPISubscriptions(ctx, connection)
	case KindOneAPI:
		return nil, nil, fmt.Errorf("%w: %s subscriptions", ErrUnsupported, KindOneAPI)
	default:
		return nil, nil, ErrUnsupportedKind
	}
}

func (a *adapter) Usage(ctx context.Context, connection Connection, query UsageQuery) (UsagePage, *Credentials, error) {
	switch a.kind {
	case KindCiii:
		return a.ciiiUsage(ctx, connection, query)
	case KindNewAPI, KindOneAPI:
		return a.newOneUsage(ctx, connection, query)
	default:
		return UsagePage{}, nil, ErrUnsupportedKind
	}
}

func (a *adapter) request(ctx context.Context, connection Connection, method, endpoint string, query url.Values, payload any, headers map[string]string, credentials ...Credentials) (int, []byte, error) {
	requestURL, err := buildURL(connection.Origin, endpoint, query)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %s", ErrInvalidConnection, sanitize(err.Error(), credentials...))
	}

	var body io.Reader
	if payload != nil {
		encoded, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return 0, nil, fmt.Errorf("encode request: %w", marshalErr)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return 0, nil, fmt.Errorf("create request: %s", sanitize(err.Error(), credentials...))
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "JieShan/account-adapter")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		if strings.TrimSpace(value) != "" {
			request.Header.Set(key, value)
		}
	}

	response, err := a.doer.Do(request)
	if err != nil {
		return 0, nil, wrapRedactedError("account request failed: ", err, credentials...)
	}
	if response == nil {
		return 0, nil, errorsNew("account request returned no response")
	}
	if response.Body == nil {
		return response.StatusCode, nil, errorsNew("account response has no body")
	}
	defer response.Body.Close()

	limited := io.LimitReader(response.Body, maxResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return response.StatusCode, nil, fmt.Errorf("read account response: %s", sanitize(err.Error(), credentials...))
	}
	if len(data) > maxResponseBytes {
		return response.StatusCode, nil, errorsNew("account response exceeds size limit")
	}
	return response.StatusCode, data, nil
}

func errorsNew(message string) error { return fmt.Errorf("%s", message) }

func buildURL(origin, endpoint string, query url.Values) (string, error) {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return "", errorsNew("origin is required")
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return "", errorsNew("origin is invalid")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errorsNew("origin must use http or https")
	}
	if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errorsNew("origin must contain only scheme, host, and optional base path")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + strings.TrimLeft(endpoint, "/")
	parsed.RawPath = ""
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func decodeJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errorsNew("multiple JSON values")
	}
	return value, nil
}

func object(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func array(value any) []any {
	result, _ := value.([]any)
	return result
}

func lookup(values map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := values[key]; ok && value != nil {
			return value
		}
	}
	return nil
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return ""
	}
}

func amountValue(value any) string {
	return strings.TrimSpace(stringValue(value))
}

func int64Value(value any) int64 {
	text := strings.TrimSpace(stringValue(value))
	if text == "" {
		return 0
	}
	if integer, err := strconv.ParseInt(text, 10, 64); err == nil {
		return integer
	}
	decimal, ok := new(big.Rat).SetString(text)
	if !ok {
		return 0
	}
	return new(big.Int).Quo(decimal.Num(), decimal.Denom()).Int64()
}

func intValue(value any) int { return int(int64Value(value)) }

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, _ := strconv.ParseBool(typed)
		return parsed
	case json.Number:
		return typed.String() != "0"
	default:
		return false
	}
}

func normalizedPage(query UsageQuery) (int, int) {
	page := query.Page
	if page < 1 {
		page = 1
	}
	pageSize := query.PageSize
	if pageSize < 1 {
		pageSize = defaultPageSize
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

func subtractAmount(total, used string) string {
	totalRat, totalOK := new(big.Rat).SetString(strings.TrimSpace(total))
	usedRat, usedOK := new(big.Rat).SetString(strings.TrimSpace(used))
	if !totalOK || !usedOK {
		return ""
	}
	remaining := new(big.Rat).Sub(totalRat, usedRat)
	if remaining.Sign() < 0 {
		remaining.SetInt64(0)
	}
	return formatRat(remaining)
}

func formatRat(value *big.Rat) string {
	text := value.FloatString(12)
	text = strings.TrimRight(text, "0")
	text = strings.TrimRight(text, ".")
	if text == "" || text == "-0" {
		return "0"
	}
	return text
}

func timestampValue(value any) string {
	text := strings.TrimSpace(stringValue(value))
	if text == "" {
		return ""
	}
	if _, err := time.Parse(time.RFC3339, text); err == nil {
		return text
	}
	integer, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return text
	}
	if integer > 9_999_999_999 {
		return time.UnixMilli(integer).UTC().Format(time.RFC3339)
	}
	return time.Unix(integer, 0).UTC().Format(time.RFC3339)
}

func credentialHash(refreshToken string) [32]byte {
	return sha256.Sum256([]byte(refreshToken))
}

func credentialsChanged(left, right Credentials) bool {
	return left.Authorization != right.Authorization ||
		left.AccessToken != right.AccessToken ||
		left.RefreshToken != right.RefreshToken ||
		left.ExpiresAt != right.ExpiresAt
}
