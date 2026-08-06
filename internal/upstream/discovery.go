package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/LuTianTian001/JieShan/internal/inferenceprotocol"
)

const (
	maxDiscoveryPages        = 20
	maxDiscoveryResponseSize = 8 << 20
)

var ErrEmptyModelList = errors.New("model discovery returned an empty complete list")

// ModelDiscoveryResult describes both the collected names and whether every
// advertised page was fetched. Models is always a non-nil slice.
type ModelDiscoveryResult struct {
	Models       []string `json:"models"`
	Complete     bool     `json:"complete"`
	PagesFetched int      `json:"pagesFetched"`
}

type EndpointDiscoveryInput struct {
	Protocol      string
	BaseURL       string
	Secret        string
	CustomHeaders []byte
}

type modelPage struct {
	Models       []string
	Continuation modelContinuation
}

type modelContinuation struct {
	Link        string
	Cursor      string
	CursorParam string
	More        bool
}

func (c *Client) DiscoverModels(ctx context.Context, upstreamID int64) (ModelDiscoveryResult, error) {
	result := emptyModelDiscovery()
	if c.store == nil || c.cipher == nil {
		return result, errors.New("model discovery storage or cipher is unavailable")
	}
	item, err := c.store.GetUpstreamSecret(ctx, upstreamID)
	if err != nil {
		return result, err
	}
	secret, err := c.cipher.Decrypt(item.SecretCipher)
	if err != nil {
		return result, err
	}
	requestURL, err := modelsURL(item.Kind, item.BaseURL, secret)
	if err != nil {
		return result, err
	}
	return c.discoverModelsFromURL(ctx, item.Kind, requestURL, secret, item.CustomHeaders)
}

// DiscoverEndpoint runs model discovery for an explicit endpoint and Key.
// It is used by the V3 site model where credentials are selected by the
// administrator instead of being hidden behind a legacy upstream row.
func (c *Client) DiscoverEndpoint(ctx context.Context, input EndpointDiscoveryInput) (ModelDiscoveryResult, error) {
	result := emptyModelDiscovery()
	protocol, err := normalizeInferenceProtocol(input.Protocol)
	if err != nil {
		return result, err
	}
	secret := strings.TrimSpace(input.Secret)
	if secret == "" {
		return result, errors.New("model discovery credential is empty")
	}
	requestURL, err := modelsURL(protocol, input.BaseURL, secret)
	if err != nil {
		return result, err
	}
	return c.discoverModelsFromURL(ctx, protocol, requestURL, secret, input.CustomHeaders)
}

func normalizeInferenceProtocol(value string) (string, error) {
	return inferenceprotocol.Normalize(value)
}

func (c *Client) discoverModelsFromURL(ctx context.Context, kind, requestURL, secret string, customHeaders []byte) (ModelDiscoveryResult, error) {
	result := emptyModelDiscovery()
	initial, err := url.Parse(requestURL)
	if err != nil || validateURLSyntax(initial) != nil {
		return result, errors.New("invalid model discovery URL")
	}
	current := initial
	seenURLs := make(map[string]struct{}, maxDiscoveryPages)
	seenModels := make(map[string]struct{})

	for result.PagesFetched < maxDiscoveryPages {
		key := current.String()
		if _, exists := seenURLs[key]; exists {
			normalizeDiscoveredModels(&result)
			return result, safeError(fmt.Errorf("model discovery pagination loop at %s", current.Redacted()))
		}
		seenURLs[key] = struct{}{}

		req, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, key, nil)
		if requestErr != nil {
			normalizeDiscoveredModels(&result)
			return result, safeError(requestErr)
		}
		applyAuth(req, kind, secret)
		applyCustomHeaders(req.Header, customHeaders)
		resp, requestErr := c.Do(req)
		if requestErr != nil {
			normalizeDiscoveredModels(&result)
			return result, requestErr
		}
		body, readErr := readDiscoveryBody(resp.Body)
		contentType := resp.Header.Get("Content-Type")
		statusCode := resp.StatusCode
		_ = resp.Body.Close()
		if readErr != nil {
			normalizeDiscoveredModels(&result)
			return result, safeError(readErr)
		}
		if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
			normalizeDiscoveredModels(&result)
			return result, safeError(discoveryHTTPError(statusCode, contentType, body))
		}
		if mediaType, _, parseErr := mime.ParseMediaType(contentType); parseErr == nil && mediaType == "text/html" {
			normalizeDiscoveredModels(&result)
			return result, errors.New("model discovery returned HTML instead of JSON")
		}

		page, parseErr := parseModelPage(kind, body)
		result.PagesFetched++
		if parseErr != nil {
			normalizeDiscoveredModels(&result)
			return result, safeError(parseErr)
		}
		for _, model := range page.Models {
			if _, exists := seenModels[model]; exists {
				continue
			}
			seenModels[model] = struct{}{}
			result.Models = append(result.Models, model)
		}
		if !page.Continuation.More {
			result.Complete = true
			normalizeDiscoveredModels(&result)
			if len(result.Models) == 0 {
				return result, ErrEmptyModelList
			}
			return result, nil
		}
		if result.PagesFetched == maxDiscoveryPages {
			break
		}
		next, nextErr := resolveModelContinuation(initial, current, kind, secret, page.Continuation)
		if nextErr != nil {
			normalizeDiscoveredModels(&result)
			return result, safeError(nextErr)
		}
		current = next
	}

	normalizeDiscoveredModels(&result)
	return result, fmt.Errorf("model discovery exceeded the %d page safety limit", maxDiscoveryPages)
}

func emptyModelDiscovery() ModelDiscoveryResult {
	return ModelDiscoveryResult{Models: make([]string, 0)}
}

func normalizeDiscoveredModels(result *ModelDiscoveryResult) {
	if result.Models == nil {
		result.Models = make([]string, 0)
	}
	sort.Strings(result.Models)
}

func readDiscoveryBody(reader io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxDiscoveryResponseSize+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxDiscoveryResponseSize {
		return nil, fmt.Errorf("model discovery response exceeds %d bytes", maxDiscoveryResponseSize)
	}
	return body, nil
}

func parseModels(kind string, body []byte) ([]string, error) {
	page, err := parseModelPage(kind, body)
	return page.Models, err
}

func parseModelPage(kind string, body []byte) (modelPage, error) {
	page := modelPage{Models: make([]string, 0)}
	if looksLikeHTML(body) {
		return page, errors.New("model discovery returned HTML instead of JSON")
	}
	root, err := decodeDiscoveryJSON(body)
	if err != nil {
		return page, fmt.Errorf("decode model list: %w", err)
	}
	models, found := findModelCollection(root, kind, 0)
	if message, failed := discoveryBusinessFailure(root, found); failed {
		return page, fmt.Errorf("model discovery business error: %s", message)
	}
	if !found {
		return page, errors.New("model discovery JSON does not contain a supported model list")
	}
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = normalizeModelName(model)
		if model == "" {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		page.Models = append(page.Models, model)
	}
	sort.Strings(page.Models)
	page.Continuation = findModelContinuation(root)
	if page.Continuation.More && page.Continuation.Link == "" && page.Continuation.Cursor == "" {
		return page, errors.New("model discovery indicates another page but provides no link or cursor")
	}
	return page, nil
}

func decodeDiscoveryJSON(body []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf})))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return root, nil
}

func findModelCollection(value any, kind string, depth int) ([]string, bool) {
	if depth > 5 {
		return nil, false
	}
	switch typed := value.(type) {
	case []any:
		return modelsFromArray(typed, kind)
	case map[string]any:
		for _, key := range []string{"models", "items", "data", "result", "results"} {
			child, exists := lookupObjectValue(typed, key)
			if !exists {
				continue
			}
			if models, found := findModelCollection(child, kind, depth+1); found {
				return models, true
			}
		}
	}
	return nil, false
}

func modelsFromArray(values []any, kind string) ([]string, bool) {
	models := make([]string, 0, len(values))
	if len(values) == 0 {
		return models, true
	}
	recognized := false
	for _, value := range values {
		switch typed := value.(type) {
		case string:
			recognized = true
			if name := normalizeModelName(typed); name != "" {
				models = append(models, name)
			}
		case map[string]any:
			if name, ok := modelNameFromObject(typed, kind); ok {
				recognized = true
				if name != "" {
					models = append(models, name)
				}
			}
		}
	}
	return models, recognized
}

func modelNameFromObject(item map[string]any, kind string) (string, bool) {
	for _, key := range []string{"id", "name", "model", "model_name", "modelId"} {
		value, exists := lookupObjectValue(item, key)
		if !exists {
			continue
		}
		name, ok := value.(string)
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if kind == "gemini" || strings.HasPrefix(name, "models/") {
			name = strings.TrimPrefix(name, "models/")
		}
		return name, true
	}
	return "", false
}

func normalizeModelName(value string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "models/"))
}

func discoveryBusinessFailure(root any, hasModels bool) (string, bool) {
	object, ok := root.(map[string]any)
	if !ok {
		return "", false
	}
	if value, exists := lookupObjectValue(object, "error"); exists && !emptyJSONValue(value) {
		return firstBusinessMessage(value, object), true
	}
	for _, key := range []string{"success", "ok"} {
		if value, exists := lookupObjectValue(object, key); exists {
			if flag, ok := value.(bool); ok && !flag {
				return firstBusinessMessage(nil, object), true
			}
		}
	}
	if value, exists := lookupObjectValue(object, "status"); exists {
		status := strings.ToLower(strings.TrimSpace(scalarString(value)))
		if status == "error" || status == "failed" || status == "failure" {
			return firstBusinessMessage(nil, object), true
		}
	}
	if !hasModels {
		if value, exists := lookupObjectValue(object, "code"); exists && failureCode(value) {
			return firstBusinessMessage(nil, object), true
		}
	}
	return "", false
}

func firstBusinessMessage(primary any, object map[string]any) string {
	if primary != nil {
		if nested, ok := primary.(map[string]any); ok {
			for _, key := range []string{"message", "msg", "detail", "code"} {
				if value, exists := lookupObjectValue(nested, key); exists {
					if text := strings.TrimSpace(scalarString(value)); text != "" {
						return text
					}
				}
			}
		}
		if text := strings.TrimSpace(scalarString(primary)); text != "" {
			return text
		}
	}
	for _, key := range []string{"message", "msg", "detail", "code"} {
		if value, exists := lookupObjectValue(object, key); exists {
			if text := strings.TrimSpace(scalarString(value)); text != "" {
				return text
			}
		}
	}
	return "upstream rejected the request"
}

func emptyJSONValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == ""
	case []any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	default:
		return false
	}
}

func failureCode(value any) bool {
	text := strings.ToLower(strings.TrimSpace(scalarString(value)))
	return text != "" && text != "0" && text != "200" && text != "ok" && text != "success"
}

func findModelContinuation(root any) modelContinuation {
	object, ok := root.(map[string]any)
	if !ok {
		return modelContinuation{}
	}
	scopes := paginationScopes(object)
	continuation := modelContinuation{}
	for _, scope := range scopes {
		for _, key := range []string{"has_more", "hasMore"} {
			if value, exists := lookupObjectValue(scope, key); exists {
				if more, ok := value.(bool); ok && more {
					continuation.More = true
				}
			}
		}
		for _, key := range []string{"next", "next_url", "nextUrl", "next_link", "nextLink"} {
			value, exists := lookupObjectValue(scope, key)
			if !exists {
				continue
			}
			candidate := paginationLink(value)
			if candidate == "" {
				continue
			}
			continuation.More = true
			if looksLikePaginationLink(candidate) {
				continuation.Link = candidate
			} else if continuation.Cursor == "" {
				continuation.Cursor, continuation.CursorParam = candidate, "cursor"
			}
		}
		for _, candidate := range []struct {
			keys  []string
			param string
		}{
			{keys: []string{"next_cursor", "nextCursor"}, param: "cursor"},
			{keys: []string{"next_page_token"}, param: "page_token"},
			{keys: []string{"nextPageToken"}, param: "pageToken"},
		} {
			for _, key := range candidate.keys {
				if value, exists := lookupObjectValue(scope, key); exists {
					if cursor := strings.TrimSpace(scalarString(value)); cursor != "" {
						continuation.More = true
						if continuation.Cursor == "" {
							continuation.Cursor, continuation.CursorParam = cursor, candidate.param
						}
					}
				}
			}
		}
	}
	if continuation.More && continuation.Cursor == "" && continuation.Link == "" {
		for _, scope := range scopes {
			for _, key := range []string{"last_id", "lastId"} {
				if value, exists := lookupObjectValue(scope, key); exists {
					if cursor := strings.TrimSpace(scalarString(value)); cursor != "" {
						continuation.Cursor, continuation.CursorParam = cursor, "after"
						return continuation
					}
				}
			}
		}
	}
	return continuation
}

func paginationScopes(root map[string]any) []map[string]any {
	scopes := []map[string]any{root}
	for _, key := range []string{"links", "pagination", "page", "meta", "data", "result"} {
		if value, exists := lookupObjectValue(root, key); exists {
			if object, ok := value.(map[string]any); ok {
				scopes = append(scopes, object)
				for _, nestedKey := range []string{"links", "pagination", "page", "meta"} {
					if nested, nestedExists := lookupObjectValue(object, nestedKey); nestedExists {
						if nestedObject, nestedOK := nested.(map[string]any); nestedOK {
							scopes = append(scopes, nestedObject)
						}
					}
				}
			}
		}
	}
	return scopes
}

func paginationLink(value any) string {
	if text := strings.TrimSpace(scalarString(value)); text != "" {
		return text
	}
	if object, ok := value.(map[string]any); ok {
		for _, key := range []string{"href", "url", "link"} {
			if nested, exists := lookupObjectValue(object, key); exists {
				if text := strings.TrimSpace(scalarString(nested)); text != "" {
					return text
				}
			}
		}
	}
	return ""
}

func looksLikePaginationLink(value string) bool {
	lower := strings.ToLower(value)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(value, "/") || strings.HasPrefix(value, "?") || strings.Contains(value, "/")
}

func resolveModelContinuation(initial, current *url.URL, kind, secret string, continuation modelContinuation) (*url.URL, error) {
	var next *url.URL
	if continuation.Link != "" {
		reference, err := url.Parse(strings.TrimSpace(continuation.Link))
		if err != nil {
			return nil, fmt.Errorf("invalid model pagination link: %w", err)
		}
		next = current.ResolveReference(reference)
	} else {
		clone := *current
		next = &clone
		query := next.Query()
		query.Set(continuation.CursorParam, continuation.Cursor)
		next.RawQuery = query.Encode()
	}
	if validateURLSyntax(next) != nil {
		return nil, errors.New("model pagination returned an unsafe URL")
	}
	if !sameOrigin(initial, next) {
		return nil, errors.New("model pagination cannot change origin")
	}
	if normalizedPath(initial.Path) != normalizedPath(next.Path) {
		return nil, errors.New("model pagination cannot change endpoint path")
	}
	if kind == "gemini" {
		query := next.Query()
		query.Set("key", secret)
		next.RawQuery = query.Encode()
	}
	next.Fragment = ""
	return next, nil
}

func normalizedPath(value string) string {
	value = strings.TrimRight(value, "/")
	if value == "" {
		return "/"
	}
	return value
}

func lookupObjectValue(object map[string]any, key string) (any, bool) {
	if value, exists := object[key]; exists {
		return value, true
	}
	for candidate, value := range object {
		if strings.EqualFold(candidate, key) {
			return value, true
		}
	}
	return nil, false
}

func scalarString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
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

func looksLikeHTML(body []byte) bool {
	trimmed := strings.ToLower(strings.TrimSpace(string(body)))
	return strings.HasPrefix(trimmed, "<!doctype html") || strings.HasPrefix(trimmed, "<html") ||
		strings.HasPrefix(trimmed, "<head") || strings.HasPrefix(trimmed, "<body")
}

func discoveryHTTPError(status int, contentType string, body []byte) error {
	if looksLikeHTML(body) || strings.Contains(strings.ToLower(contentType), "text/html") {
		return fmt.Errorf("model discovery returned HTTP %d with an HTML response", status)
	}
	if root, err := decodeDiscoveryJSON(body); err == nil {
		if message, failed := discoveryBusinessFailure(root, false); failed {
			return fmt.Errorf("model discovery returned HTTP %d: %s", status, message)
		}
	}
	message := strings.TrimSpace(compact(body, 500))
	if message == "" {
		message = http.StatusText(status)
	}
	return fmt.Errorf("model discovery returned HTTP %d: %s", status, message)
}
