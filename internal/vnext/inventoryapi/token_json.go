package inventoryapi

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/LuTianTian001/JieShan/internal/vnext/adminauth"
	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const (
	maxTokenJSONRequestBytes = 512 << 10
	maxTokenJSONBytes        = 256 << 10
	maxTokenJSONItems        = 100
	maxTokenJSONPreviews     = 32
	tokenJSONPreviewTTL      = 10 * time.Minute
	maxTokenJSONDepth        = 64
)

type previewTokenJSONRequest struct {
	RawJSON string `json:"rawJson"`
}

type importTokenJSONRequest struct {
	PreviewID string `json:"previewId"`
	Indices   []int  `json:"indices"`
}

type tokenJSONAccountPreview struct {
	Index          int      `json:"index"`
	AccountName    string   `json:"accountName"`
	CredentialName string   `json:"credentialName"`
	Platform       string   `json:"platform"`
	Endpoint       string   `json:"endpoint"`
	TokenHint      string   `json:"tokenHint"`
	Scopes         []string `json:"scopes"`
	Status         string   `json:"status"`
	Warnings       []string `json:"warnings"`
}

type tokenJSONImportPreview struct {
	PreviewID      string                    `json:"previewId"`
	SiteID         int64                     `json:"siteId"`
	DetectedFormat string                    `json:"detectedFormat"`
	Items          []tokenJSONAccountPreview `json:"items"`
	ReadyCount     int                       `json:"readyCount"`
	DuplicateCount int                       `json:"duplicateCount"`
	InvalidCount   int                       `json:"invalidCount"`
	ExpiresAt      int64                     `json:"expiresAt"`
}

type tokenJSONImportResponse struct {
	ImportedCount int     `json:"importedCount"`
	SkippedCount  int     `json:"skippedCount"`
	CredentialIDs []int64 `json:"credentialIds"`
	EndpointIDs   []int64 `json:"endpointIds"`
}

type tokenJSONPreviewItem struct {
	Preview tokenJSONAccountPreview
	Import  TokenJSONImportItem
}

type tokenJSONExistingEndpoint struct {
	AuthScheme string
}

type tokenJSONPreviewEntry struct {
	ID             string
	SiteID         int64
	SessionScope   string
	DetectedFormat string
	ExpiresAt      time.Time
	Items          []tokenJSONPreviewItem
	State          tokenJSONPreviewState
	SelectionKey   string
	Result         *tokenJSONImportResponse
}

type tokenJSONPreviewState uint8

const (
	tokenJSONPreviewPending tokenJSONPreviewState = iota
	tokenJSONPreviewImporting
	tokenJSONPreviewCompleted
	tokenJSONPreviewFailed
)

var (
	errTokenJSONPreviewUnavailable = errors.New("token JSON preview is unavailable")
	errTokenJSONSelectionInvalid   = errors.New("token JSON selection is invalid")
	errTokenJSONImportInProgress   = errors.New("token JSON import is already in progress")
	errTokenJSONPreviewConsumed    = errors.New("token JSON preview was already consumed")
)

func (entry *tokenJSONPreviewEntry) clear() {
	if entry == nil {
		return
	}
	for index := range entry.Items {
		clear(entry.Items[index].Import.Secret)
		entry.Items[index].Import.Secret = nil
	}
	entry.Items = nil
}

type tokenJSONPreviewStore struct {
	mu      sync.Mutex
	entries map[string]*tokenJSONPreviewEntry
	now     func() time.Time
	random  io.Reader
}

func newTokenJSONPreviewStore() *tokenJSONPreviewStore {
	return &tokenJSONPreviewStore{
		entries: make(map[string]*tokenJSONPreviewEntry),
		now:     time.Now,
		random:  rand.Reader,
	}
}

func (store *tokenJSONPreviewStore) put(entry *tokenJSONPreviewEntry) error {
	if store == nil || entry == nil {
		return errors.New("token JSON preview store is unavailable")
	}
	identifier := make([]byte, 24)
	if _, err := io.ReadFull(store.random, identifier); err != nil {
		return err
	}
	entry.ID = base64.RawURLEncoding.EncodeToString(identifier)
	now := store.now().UTC()
	entry.ExpiresAt = now.Add(tokenJSONPreviewTTL)
	entry.State = tokenJSONPreviewPending

	store.mu.Lock()
	defer store.mu.Unlock()
	store.removeExpiredLocked(now)
	if len(store.entries) >= maxTokenJSONPreviews {
		var oldestID string
		var oldest time.Time
		for id, candidate := range store.entries {
			if candidate.State == tokenJSONPreviewImporting {
				continue
			}
			if oldestID == "" || candidate.ExpiresAt.Before(oldest) {
				oldestID = id
				oldest = candidate.ExpiresAt
			}
		}
		if oldestID == "" {
			return errors.New("too many token JSON imports are in progress")
		}
		if previous := store.entries[oldestID]; previous != nil {
			previous.clear()
		}
		delete(store.entries, oldestID)
	}
	store.entries[entry.ID] = entry
	return nil
}

func (store *tokenJSONPreviewStore) claim(
	id string,
	siteID int64,
	sessionScope string,
	indices []int,
) (*tokenJSONPreviewEntry, *tokenJSONImportResponse, error) {
	if store == nil {
		return nil, nil, errTokenJSONPreviewUnavailable
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.now().UTC()
	store.removeExpiredLocked(now)
	entry := store.entries[id]
	if entry == nil || entry.SiteID != siteID || entry.SessionScope != sessionScope {
		return nil, nil, errTokenJSONPreviewUnavailable
	}
	selectionKey := fmt.Sprint(indices)
	switch entry.State {
	case tokenJSONPreviewPending:
		for _, index := range indices {
			if index >= len(entry.Items) || entry.Items[index].Preview.Index != index || entry.Items[index].Preview.Status != "ready" {
				return nil, nil, errTokenJSONSelectionInvalid
			}
		}
		entry.State = tokenJSONPreviewImporting
		entry.SelectionKey = selectionKey
		return entry, nil, nil
	case tokenJSONPreviewImporting:
		if entry.SelectionKey == selectionKey {
			return nil, nil, errTokenJSONImportInProgress
		}
		return nil, nil, errTokenJSONPreviewConsumed
	case tokenJSONPreviewCompleted:
		if entry.SelectionKey == selectionKey && entry.Result != nil {
			result := cloneTokenJSONImportResponse(*entry.Result)
			return nil, &result, nil
		}
		return nil, nil, errTokenJSONPreviewConsumed
	default:
		return nil, nil, errTokenJSONPreviewConsumed
	}
}

func (store *tokenJSONPreviewStore) complete(
	entry *tokenJSONPreviewEntry,
	result tokenJSONImportResponse,
) {
	if store == nil || entry == nil {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current := store.entries[entry.ID]
	if current != entry || current.State != tokenJSONPreviewImporting {
		return
	}
	current.clear()
	current.State = tokenJSONPreviewCompleted
	cloned := cloneTokenJSONImportResponse(result)
	current.Result = &cloned
	current.ExpiresAt = store.now().UTC().Add(tokenJSONPreviewTTL)
}

func (store *tokenJSONPreviewStore) fail(entry *tokenJSONPreviewEntry) {
	if store == nil || entry == nil {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current := store.entries[entry.ID]
	if current != entry || current.State != tokenJSONPreviewImporting {
		return
	}
	current.clear()
	current.State = tokenJSONPreviewFailed
	current.Result = nil
	current.ExpiresAt = store.now().UTC().Add(tokenJSONPreviewTTL)
}

func cloneTokenJSONImportResponse(value tokenJSONImportResponse) tokenJSONImportResponse {
	value.CredentialIDs = append([]int64(nil), value.CredentialIDs...)
	value.EndpointIDs = append([]int64(nil), value.EndpointIDs...)
	return value
}

func (store *tokenJSONPreviewStore) removeExpiredLocked(now time.Time) {
	for id, entry := range store.entries {
		if entry.State == tokenJSONPreviewImporting {
			continue
		}
		if !entry.ExpiresAt.After(now) {
			entry.clear()
			delete(store.entries, id)
		}
	}
}

func (handler *Handler) previewTokenJSON(w http.ResponseWriter, r *http.Request, siteID int64) {
	var body previewTokenJSONRequest
	if !decodeTokenJSONRequest(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.RawJSON) == "" {
		writeError(w, http.StatusBadRequest, "invalid_token_json", "rawJson must contain a JSON document")
		return
	}
	if len(body.RawJSON) > maxTokenJSONBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "token_json_too_large", "rawJson exceeds the 256 KiB safety limit")
		return
	}
	if _, err := handler.repository.GetSite(r.Context(), siteID); err != nil {
		writeRepositoryError(w, err)
		return
	}
	credentials, err := handler.repository.ListSiteCredentials(r.Context(), siteID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	existingNames := make(map[string]struct{}, len(credentials))
	for _, credential := range credentials {
		existingNames[strings.ToLower(strings.TrimSpace(credential.Credential.Name))] = struct{}{}
	}
	endpoints, err := handler.repository.ListSiteEndpoints(r.Context(), siteID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	existingEndpoints := make(map[string]tokenJSONExistingEndpoint, len(endpoints))
	for _, endpoint := range endpoints {
		existingEndpoints[tokenJSONEndpointKey(endpoint.BaseURL, endpoint.WireProtocol, endpoint.Surface)] = tokenJSONExistingEndpoint{
			AuthScheme: endpoint.AuthScheme,
		}
	}
	format, items, err := parseTokenJSON([]byte(body.RawJSON), existingNames, existingEndpoints)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_token_json", err.Error())
		return
	}
	entry := &tokenJSONPreviewEntry{
		SiteID: siteID, SessionScope: tokenJSONSessionScope(r), DetectedFormat: format, Items: items,
	}
	if err := handler.tokenJSONPreviews.put(entry); err != nil {
		entry.clear()
		writeError(w, http.StatusInternalServerError, "internal_error", "the token JSON preview could not be created")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, newTokenJSONPreviewResponse(entry))
}

func (handler *Handler) importTokenJSON(w http.ResponseWriter, r *http.Request, siteID int64) {
	var body importTokenJSONRequest
	if !decodeTokenJSONRequest(w, r, &body) {
		return
	}
	body.PreviewID = strings.TrimSpace(body.PreviewID)
	if len(body.PreviewID) < 20 || len(body.PreviewID) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_request", "previewId is invalid")
		return
	}
	if len(body.Indices) == 0 || len(body.Indices) > maxTokenJSONItems {
		writeError(w, http.StatusBadRequest, "invalid_request", "indices must contain between 1 and 100 items")
		return
	}
	seen := make(map[int]struct{}, len(body.Indices))
	for _, index := range body.Indices {
		if index < 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "indices must contain only non-negative integers")
			return
		}
		if _, duplicate := seen[index]; duplicate {
			writeError(w, http.StatusBadRequest, "invalid_request", "indices must not contain duplicates")
			return
		}
		seen[index] = struct{}{}
	}
	sort.Ints(body.Indices)

	entry, replay, err := handler.tokenJSONPreviews.claim(body.PreviewID, siteID, tokenJSONSessionScope(r), body.Indices)
	switch {
	case errors.Is(err, errTokenJSONPreviewUnavailable):
		writeError(w, http.StatusConflict, "token_preview_expired", "token import preview is missing, expired, or belongs to another session")
		return
	case errors.Is(err, errTokenJSONSelectionInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", "indices may select only ready preview items")
		return
	case errors.Is(err, errTokenJSONImportInProgress):
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusConflict, "token_import_in_progress", "this token import is already in progress")
		return
	case errors.Is(err, errTokenJSONPreviewConsumed):
		writeError(w, http.StatusConflict, "token_preview_consumed", "this token import preview was already used with a different selection or failed")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal_error", "the token import preview could not be claimed")
		return
	case replay != nil:
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, replay)
		return
	}
	completed := false
	defer func() {
		if !completed {
			handler.tokenJSONPreviews.fail(entry)
		}
	}()
	imports := make([]TokenJSONImportItem, 0, len(body.Indices))
	for _, index := range body.Indices {
		imports = append(imports, entry.Items[index].Import)
	}
	records, err := handler.repository.ImportTokenJSONItems(r.Context(), siteID, imports)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	result := tokenJSONImportResponse{
		ImportedCount: len(imports),
		SkippedCount:  len(entry.Items) - len(imports),
		CredentialIDs: records.CredentialIDs,
		EndpointIDs:   records.EndpointIDs,
	}
	handler.tokenJSONPreviews.complete(entry, result)
	completed = true
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, result)
}

func decodeTokenJSONRequest(w http.ResponseWriter, r *http.Request, destination any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxTokenJSONRequestBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds the 512 KiB safety limit")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid_request", "request body could not be read")
		return false
	}
	if err := validateStrictTokenJSON(raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must be one strict JSON value without duplicate fields")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must be a valid JSON object")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must contain exactly one JSON value")
		return false
	}
	return true
}

func tokenJSONSessionScope(r *http.Request) string {
	if cookie, err := r.Cookie(adminauth.SessionCookieName); err == nil && cookie.Value != "" {
		digest := sha256.Sum256([]byte(cookie.Value))
		return "session:" + hex.EncodeToString(digest[:])
	}
	if principal, ok := adminauth.PrincipalFromContext(r.Context()); ok && principal.AdminUserID > 0 {
		return fmt.Sprintf("admin:%d", principal.AdminUserID)
	}
	return "unscoped"
}

func newTokenJSONPreviewResponse(entry *tokenJSONPreviewEntry) tokenJSONImportPreview {
	response := tokenJSONImportPreview{
		PreviewID: entry.ID, SiteID: entry.SiteID, DetectedFormat: entry.DetectedFormat,
		Items: make([]tokenJSONAccountPreview, 0, len(entry.Items)), ExpiresAt: entry.ExpiresAt.UnixMilli(),
	}
	for _, item := range entry.Items {
		response.Items = append(response.Items, item.Preview)
		switch item.Preview.Status {
		case "ready":
			response.ReadyCount++
		case "duplicate":
			response.DuplicateCount++
		default:
			response.InvalidCount++
		}
	}
	return response
}

func parseTokenJSON(
	raw []byte,
	existingNames map[string]struct{},
	existingEndpoints map[string]tokenJSONExistingEndpoint,
) (string, []tokenJSONPreviewItem, error) {
	if err := validateStrictTokenJSON(raw); err != nil {
		return "", nil, err
	}
	format, rawItems, err := tokenJSONRootItems(raw)
	if err != nil {
		return "", nil, err
	}
	if len(rawItems) == 0 {
		return "", nil, errors.New("token JSON does not contain any accounts")
	}
	if len(rawItems) > maxTokenJSONItems {
		return "", nil, fmt.Errorf("token JSON contains more than %d accounts", maxTokenJSONItems)
	}

	items := make([]tokenJSONPreviewItem, 0, len(rawItems))
	seenNames := make(map[string]struct{}, len(rawItems))
	seenSecrets := make(map[[sha256.Size]byte]struct{}, len(rawItems))
	for index, rawItem := range rawItems {
		item := parseTokenJSONItem(index, rawItem)
		if item.Preview.Status == "ready" {
			nameKey := strings.ToLower(item.Import.CredentialName)
			secretDigest := sha256.Sum256(item.Import.Secret)
			_, existing := existingNames[nameKey]
			_, repeatedName := seenNames[nameKey]
			_, repeatedSecret := seenSecrets[secretDigest]
			switch {
			case existing:
				item.Preview.Status = "duplicate"
				item.Preview.Warnings = append(item.Preview.Warnings, "a credential with this name already exists on the site")
			case repeatedName:
				item.Preview.Status = "duplicate"
				item.Preview.Warnings = append(item.Preview.Warnings, "the credential name is repeated in this document")
			case repeatedSecret:
				item.Preview.Status = "duplicate"
				item.Preview.Warnings = append(item.Preview.Warnings, "the token is repeated in this document")
			}
			seenNames[nameKey] = struct{}{}
			seenSecrets[secretDigest] = struct{}{}
			if item.Preview.Status != "ready" {
				clear(item.Import.Secret)
				item.Import.Secret = nil
			}
		}
		if item.Preview.Status == "ready" {
			key := tokenJSONEndpointKey(item.Import.BaseURL, item.Import.WireProtocol, item.Import.Surface)
			if endpoint, exists := existingEndpoints[key]; exists {
				if endpoint.AuthScheme != item.Import.AuthScheme {
					item.Preview.Status = "invalid"
					item.Preview.Warnings = append(item.Preview.Warnings, "the matching endpoint uses a different authentication scheme")
					clear(item.Import.Secret)
					item.Import.Secret = nil
				} else {
					item.Preview.Warnings = append(item.Preview.Warnings, "the matching endpoint will be reused")
				}
			}
		}
		items = append(items, item)
	}
	return format, items, nil
}

func tokenJSONEndpointKey(baseURL, wireProtocol, surface string) string {
	return baseURL + "\x00" + wireProtocol + "\x00" + surface
}

func tokenJSONRootItems(raw []byte) (string, []json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "", nil, errors.New("token JSON is empty")
	}
	switch trimmed[0] {
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return "", nil, errors.New("token JSON must be valid JSON")
		}
		return "account_array", items, nil
	case '{':
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return "", nil, errors.New("token JSON must be valid JSON")
		}
		accounts, hasAccounts := object["accounts"]
		tokens, hasTokens := object["tokens"]
		if hasAccounts && hasTokens {
			return "", nil, errors.New("token JSON must not contain both accounts and tokens wrappers")
		}
		if hasAccounts {
			var items []json.RawMessage
			if err := json.Unmarshal(accounts, &items); err != nil {
				return "", nil, errors.New("accounts must be a JSON array")
			}
			return "accounts_envelope", items, nil
		}
		if hasTokens {
			var items []json.RawMessage
			if err := json.Unmarshal(tokens, &items); err != nil {
				return "", nil, errors.New("tokens must be a JSON array")
			}
			return "tokens_envelope", items, nil
		}
		return "single_account", []json.RawMessage{append(json.RawMessage(nil), raw...)}, nil
	default:
		return "", nil, errors.New("token JSON must be an account object, account array, or accounts/tokens wrapper")
	}
}

func parseTokenJSONItem(index int, raw json.RawMessage) tokenJSONPreviewItem {
	preview := tokenJSONAccountPreview{
		Index: index, AccountName: fmt.Sprintf("Account %d", index+1), CredentialName: fmt.Sprintf("Account %d", index+1),
		TokenHint: "not available", Scopes: []string{}, Status: "invalid", Warnings: []string{},
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		preview.Warnings = append(preview.Warnings, "account must be a JSON object")
		return tokenJSONPreviewItem{Preview: preview}
	}

	metadataInvalid := false
	accountName, err := tokenJSONStringField(object, "account name", "accountName", "account_name", "name", "email")
	if err != nil {
		preview.Warnings = append(preview.Warnings, err.Error())
		metadataInvalid = true
	} else if accountName != "" {
		preview.AccountName = truncateUTF8(accountName, 120)
	}
	credentialName, err := tokenJSONStringField(object, "credential name", "credentialName", "credential_name", "label")
	if err != nil {
		preview.Warnings = append(preview.Warnings, err.Error())
		metadataInvalid = true
	} else if credentialName != "" {
		preview.CredentialName = truncateUTF8(credentialName, 120)
	} else {
		preview.CredentialName = preview.AccountName
	}

	secret, secretErr := tokenJSONStringField(object, "token", "token", "access_token", "accessToken", "api_key", "apiKey")
	if secretErr != nil {
		preview.Warnings = append(preview.Warnings, secretErr.Error())
	}
	if secret == "" {
		if _, hasRefreshToken := object["refresh_token"]; hasRefreshToken {
			preview.Warnings = append(preview.Warnings, "refresh_token is not accepted as a runtime API credential")
		} else {
			preview.Warnings = append(preview.Warnings, "token, access_token, or api_key is required")
		}
	} else if len(secret) > 32<<10 {
		preview.Warnings = append(preview.Warnings, "token exceeds the 32 KiB safety limit")
		secret = ""
	} else {
		preview.TokenHint = tokenHint(secret)
	}

	platformValue, platformErr := tokenJSONStringField(object, "platform", "wireProtocol", "wire_protocol", "platform", "provider")
	protocolID, adapterKind, platformMapErr := mapTokenJSONPlatform(platformValue)
	if platformErr != nil {
		preview.Warnings = append(preview.Warnings, platformErr.Error())
	} else if platformMapErr != nil {
		preview.Warnings = append(preview.Warnings, platformMapErr.Error())
	} else {
		preview.Platform = string(protocolID)
	}

	endpoint, endpointErr := tokenJSONStringField(object, "endpoint", "endpoint", "baseUrl", "base_url", "apiBase", "api_base")
	if endpointErr != nil {
		preview.Warnings = append(preview.Warnings, endpointErr.Error())
	} else if endpoint == "" {
		preview.Warnings = append(preview.Warnings, "endpoint, base_url, or api_base is required")
	}

	var surface vnextprotocol.Surface
	var authScheme vnextprotocol.AuthScheme
	if platformErr == nil && platformMapErr == nil {
		surface, err = tokenJSONSurface(object, protocolID)
		if err != nil {
			preview.Warnings = append(preview.Warnings, err.Error())
		}
		if err == nil {
			authScheme, err = tokenJSONAuthScheme(object, protocolID)
			if err != nil {
				preview.Warnings = append(preview.Warnings, err.Error())
			}
		}
	}
	preview.Scopes, err = tokenJSONScopes(object)
	if err != nil {
		preview.Warnings = append(preview.Warnings, err.Error())
	}

	if metadataInvalid || secret == "" || platformErr != nil || platformMapErr != nil || endpointErr != nil || endpoint == "" || surface == "" || authScheme == "" ||
		strings.TrimSpace(preview.CredentialName) == "" || len(preview.CredentialName) > 120 {
		return tokenJSONPreviewItem{Preview: preview}
	}
	endpointName := truncateUTF8(preview.CredentialName+" Token JSON", 120)
	normalizedEndpoint, err := validateEndpointSnapshot(tokenJSONEndpointWrite(
		endpointName, endpoint, protocolID, surface, adapterKind, authScheme,
	))
	if err != nil {
		preview.Warnings = append(preview.Warnings, err.Error())
		return tokenJSONPreviewItem{Preview: preview}
	}
	preview.Endpoint = normalizedEndpoint.BaseURL
	preview.Status = "ready"
	return tokenJSONPreviewItem{
		Preview: preview,
		Import: TokenJSONImportItem{
			CredentialName: preview.CredentialName,
			Secret:         []byte(secret),
			EndpointName:   normalizedEndpoint.Name,
			BaseURL:        normalizedEndpoint.BaseURL,
			WireProtocol:   normalizedEndpoint.WireProtocol,
			Surface:        normalizedEndpoint.Surface,
			AdapterKind:    normalizedEndpoint.AdapterKind,
			AuthScheme:     normalizedEndpoint.AuthScheme,
		},
	}
}

func tokenJSONEndpointWrite(
	name, endpoint string,
	protocolID vnextprotocol.Protocol,
	surface vnextprotocol.Surface,
	adapterKind string,
	authScheme vnextprotocol.AuthScheme,
) vnextstore.SiteEndpointWrite {
	return vnextstore.SiteEndpointWrite{
		Name: name, BaseURL: endpoint, WireProtocol: string(protocolID), Surface: string(surface),
		AdapterKind: adapterKind, AuthScheme: string(authScheme), HeaderTemplate: json.RawMessage(`{}`), Enabled: true,
	}
}

func tokenJSONStringField(object map[string]json.RawMessage, label string, aliases ...string) (string, error) {
	var value string
	found := false
	for _, alias := range aliases {
		raw, ok := object[alias]
		if !ok {
			continue
		}
		var candidate string
		if err := json.Unmarshal(raw, &candidate); err != nil {
			return "", fmt.Errorf("%s field %q must be a string", label, alias)
		}
		candidate = strings.TrimSpace(candidate)
		if !found {
			value = candidate
			found = true
			continue
		}
		if candidate != value {
			return "", fmt.Errorf("%s aliases contain conflicting values", label)
		}
	}
	return value, nil
}

func mapTokenJSONPlatform(value string) (vnextprotocol.Protocol, string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "openai", "openai-compatible", "openai_compatible":
		return vnextprotocol.OpenAI, "openai", nil
	case "anthropic", "claude":
		return vnextprotocol.Anthropic, "anthropic", nil
	case "gemini", "google", "google-gemini", "google_gemini":
		return vnextprotocol.Gemini, "gemini", nil
	case "":
		return "", "", errors.New("platform, provider, or wire_protocol is required")
	default:
		return "", "", fmt.Errorf("unsupported token JSON platform %q", value)
	}
}

func tokenJSONSurface(object map[string]json.RawMessage, protocolID vnextprotocol.Protocol) (vnextprotocol.Surface, error) {
	value, err := tokenJSONStringField(object, "surface", "surface", "apiSurface", "api_surface")
	if err != nil {
		return "", err
	}
	if value == "" {
		return previewSurface(protocolID, optional[string]{})
	}
	surface, err := vnextprotocol.ParseSurface(value)
	if err != nil {
		return "", err
	}
	if err := vnextprotocol.ValidatePair(protocolID, surface); err != nil {
		return "", err
	}
	return surface, nil
}

func tokenJSONAuthScheme(object map[string]json.RawMessage, protocolID vnextprotocol.Protocol) (vnextprotocol.AuthScheme, error) {
	value, err := tokenJSONStringField(object, "auth scheme", "authScheme", "auth_scheme")
	if err != nil {
		return "", err
	}
	var scheme vnextprotocol.AuthScheme
	if value == "" {
		scheme, err = vnextprotocol.DefaultAuthScheme(protocolID)
	} else {
		scheme, err = vnextprotocol.ParseAuthScheme(value)
	}
	if err != nil {
		return "", err
	}
	if err := validatePreviewAuthScheme(protocolID, scheme); err != nil {
		return "", err
	}
	return scheme, nil
}

func tokenJSONScopes(object map[string]json.RawMessage) ([]string, error) {
	result := make([]string, 0)
	seen := make(map[string]struct{})
	if raw, ok := object["scopes"]; ok {
		var values []string
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, errors.New("scopes must be an array of strings")
		}
		for _, value := range values {
			value = strings.TrimSpace(value)
			if _, duplicate := seen[value]; value != "" && !duplicate {
				seen[value] = struct{}{}
				result = append(result, value)
			}
		}
	}
	if raw, ok := object["scope"]; ok {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, errors.New("scope must be a space-delimited string")
		}
		for _, scope := range strings.Fields(value) {
			if _, duplicate := seen[scope]; !duplicate {
				seen[scope] = struct{}{}
				result = append(result, scope)
			}
		}
	}
	if len(result) > 64 {
		return nil, errors.New("scopes exceeds the 64 item safety limit")
	}
	for _, scope := range result {
		if len(scope) > 128 {
			return nil, errors.New("scope values must not exceed 128 characters")
		}
	}
	return result, nil
}

func tokenHint(secret string) string {
	runes := []rune(secret)
	if len(runes) < 10 {
		return fmt.Sprintf("**** (%d chars)", len(runes))
	}
	return string(runes[:4]) + "..." + string(runes[len(runes)-4:])
}

func truncateUTF8(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	for len(value) > limit || !utf8.ValidString(value) {
		_, size := utf8.DecodeLastRuneInString(value)
		if size <= 0 {
			return ""
		}
		value = value[:len(value)-size]
	}
	return strings.TrimSpace(value)
}

func validateStrictTokenJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := validateTokenJSONValue(decoder, 0); err != nil {
		return fmt.Errorf("token JSON must be strict JSON: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("token JSON must contain exactly one JSON value")
		}
		return fmt.Errorf("token JSON must be strict JSON: %w", err)
	}
	return nil
}

func validateTokenJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maxTokenJSONDepth {
		return fmt.Errorf("nesting exceeds %d levels", maxTokenJSONDepth)
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key must be a string")
			}
			if _, duplicate := keys[key]; duplicate {
				return fmt.Errorf("object contains duplicate key %q", key)
			}
			keys[key] = struct{}{}
			if err := validateTokenJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := validateTokenJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("array is not closed")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}
