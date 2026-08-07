package siteadminapi

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/secretbox"
	"github.com/LuTianTian001/JieShan/internal/vnext/siteadmin"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const (
	apiPrefix       = "/api/vnext/site-accounts"
	maxRequestBytes = 128 << 10
)

type Handler struct {
	repository Repository
	service    *siteadmin.Service
	registry   siteadmin.AdapterLookup
}

func New(repository Repository, registry siteadmin.AdapterLookup) (*Handler, error) {
	if repository == nil || registry == nil {
		return nil, errors.New("site account repository and adapter registry are required")
	}
	service, err := siteadmin.NewService(repository, registry)
	if err != nil {
		return nil, err
	}
	return NewWithService(repository, registry, service)
}

func NewWithService(
	repository Repository,
	registry siteadmin.AdapterLookup,
	service *siteadmin.Service,
) (*Handler, error) {
	if repository == nil || registry == nil || service == nil {
		return nil, errors.New("site account repository, adapter registry, and service are required")
	}
	return &Handler{repository: repository, service: service, registry: registry}, nil
}

func NewStoreHandler(
	store *vnextstore.Store,
	box *secretbox.Box,
	registry *siteadmin.Registry,
) (*Handler, error) {
	repository, err := NewStoreRepository(store, box)
	if err != nil {
		return nil, err
	}
	return New(repository, registry)
}

func (handler *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	segments, ok := routeSegments(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	if len(segments) == 0 {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		handler.listConnections(w, r)
		return
	}
	if len(segments) < 2 || segments[0] != "sites" {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	siteID, err := strconv.ParseInt(segments[1], 10, 64)
	if err != nil || siteID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "site ID must be a positive integer")
		return
	}
	rest := segments[2:]
	if len(rest) == 0 {
		handler.handleConnection(w, r, siteID)
		return
	}
	switch strings.Join(rest, "/") {
	case "capabilities":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		handler.getCapabilities(w, r, siteID)
	case "secret":
		if r.Method != http.MethodPut {
			methodNotAllowed(w, http.MethodPut)
			return
		}
		handler.replaceSecret(w, r, siteID)
	case "session/refresh":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		handler.refreshSession(w, r, siteID)
	case "balance":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		handler.getBalance(w, r, siteID)
	case "balance/refresh":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		handler.refreshBalance(w, r, siteID)
	case "usage":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		handler.listUsage(w, r, siteID)
	case "usage/sync":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		handler.syncUsage(w, r, siteID)
	default:
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	}
}

func (handler *Handler) listConnections(w http.ResponseWriter, r *http.Request) {
	items, err := handler.repository.ListConnections(r.Context())
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	result := make([]connectionResponse, 0, len(items))
	for _, item := range items {
		result = append(result, handler.connectionResponse(item, nil))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": result})
}

func (handler *Handler) handleConnection(w http.ResponseWriter, r *http.Request, siteID int64) {
	switch r.Method {
	case http.MethodGet:
		item, err := handler.repository.GetConnection(r.Context(), siteID)
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		balance, balanceErr := handler.repository.LatestBalance(r.Context(), siteID)
		var latest *vnextstore.SiteBalanceSnapshot
		if balanceErr == nil {
			latest = &balance
		} else if !errors.Is(balanceErr, sql.ErrNoRows) {
			writeRepositoryError(w, balanceErr)
			return
		}
		writeRevisionJSON(w, http.StatusOK, item.Revision, handler.connectionResponse(item, latest))
	case http.MethodPut:
		var body connectionRequest
		if !decodeJSON(w, r, &body) {
			return
		}
		input, err := body.create()
		if err != nil {
			writeValidationError(w, err)
			return
		}
		if _, err := handler.registry.Lookup(input.AdapterKind); err != nil {
			writeError(w, http.StatusBadRequest, "unsupported_adapter", "adapterKind is not registered")
			return
		}
		item, err := handler.repository.CreateConnection(r.Context(), siteID, input)
		clearSiteSecrets(&input.Secrets)
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		writeRevisionJSON(w, http.StatusCreated, item.Revision, handler.connectionResponse(item, nil))
	case http.MethodPatch:
		revision, ok := requiredIfMatch(w, r)
		if !ok {
			return
		}
		current, err := handler.repository.GetConnection(r.Context(), siteID)
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		var body updateConnectionRequest
		if !decodeJSON(w, r, &body) {
			return
		}
		input, err := body.apply(current, revision)
		if err != nil {
			writeValidationError(w, err)
			return
		}
		if _, err := handler.registry.Lookup(input.AdapterKind); err != nil {
			writeError(w, http.StatusBadRequest, "unsupported_adapter", "adapterKind is not registered")
			return
		}
		item, err := handler.repository.UpdateConnection(r.Context(), siteID, input)
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		writeRevisionJSON(w, http.StatusOK, item.Revision, handler.connectionResponse(item, nil))
	case http.MethodDelete:
		revision, ok := requiredIfMatch(w, r)
		if !ok {
			return
		}
		if err := handler.repository.DeleteConnection(r.Context(), siteID, revision); err != nil {
			writeRepositoryError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete)
	}
}

func (handler *Handler) getCapabilities(w http.ResponseWriter, r *http.Request, siteID int64) {
	item, err := handler.repository.GetConnection(r.Context(), siteID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	adapter, err := handler.registry.Lookup(item.AdapterKind)
	if err != nil {
		writeError(w, http.StatusConflict, "adapter_unavailable", "configured adapter is not available")
		return
	}
	writeJSON(w, http.StatusOK, capabilitiesResponse(adapter.Capabilities()))
}

func (handler *Handler) replaceSecret(w http.ResponseWriter, r *http.Request, siteID int64) {
	revision, ok := requiredIfMatch(w, r)
	if !ok {
		return
	}
	var body secretRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	secrets, err := body.validate()
	if err != nil {
		writeValidationError(w, err)
		return
	}
	item, err := handler.repository.ReplaceConnectionSecrets(r.Context(), siteID, ConnectionSecretUpdate{
		ExpectedRevision: revision,
		Secrets:          secrets,
	})
	clearSiteSecrets(&secrets)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	writeRevisionJSON(w, http.StatusOK, item.Revision, handler.connectionResponse(item, nil))
}

func (handler *Handler) refreshSession(w http.ResponseWriter, r *http.Request, siteID int64) {
	result, err := handler.service.RefreshSession(r.Context(), siteID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"changed":     result.Changed,
		"refreshedAt": nullableTimeMillis(result.RefreshedAt),
	})
}

func (handler *Handler) getBalance(w http.ResponseWriter, r *http.Request, siteID int64) {
	if _, err := handler.repository.GetConnection(r.Context(), siteID); err != nil {
		writeRepositoryError(w, err)
		return
	}
	item, err := handler.repository.LatestBalance(r.Context(), siteID)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{"balance": nil})
		return
	}
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"balance": newBalanceResponse(item)})
}

func (handler *Handler) refreshBalance(w http.ResponseWriter, r *http.Request, siteID int64) {
	result, err := handler.service.RefreshBalance(r.Context(), siteID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	stored, err := handler.repository.LatestBalance(r.Context(), siteID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"balance":        newBalanceResponse(stored),
		"sessionChanged": result.SessionChanged,
	})
}

func (handler *Handler) listUsage(w http.ResponseWriter, r *http.Request, siteID int64) {
	filter, err := localUsageFilter(r)
	if err != nil {
		writeValidationError(w, err)
		return
	}
	page, err := handler.repository.ListUsage(r.Context(), siteID, filter)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	items := make([]usageResponse, 0, len(page.Records))
	for _, item := range page.Records {
		items = append(items, newUsageResponse(item))
	}
	nextCursor := ""
	if page.HasMore && page.NextBeforeOccurredAt != nil && page.NextBeforeID != nil {
		nextCursor = encodeCursor(*page.NextBeforeOccurredAt, *page.NextBeforeID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "hasMore": page.HasMore, "nextCursor": nextCursor,
	})
}

func (handler *Handler) syncUsage(w http.ResponseWriter, r *http.Request, siteID int64) {
	var body syncUsageRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	query, err := body.validate()
	if err != nil {
		writeValidationError(w, err)
		return
	}
	result, err := handler.service.SyncUsagePage(r.Context(), siteID, query)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"inserted": result.Saved.Inserted, "deduplicated": result.Saved.Deduplicated,
		"fetched": len(result.Page.Records), "hasMore": result.Page.HasMore,
		"nextCursor": result.Page.NextCursor, "fetchedAt": result.Page.FetchedAt.UTC().UnixMilli(),
		"sessionChanged": result.SessionChanged,
	})
}

type connectionRequest struct {
	AdapterKind string        `json:"adapterKind"`
	Origin      string        `json:"origin"`
	Enabled     *bool         `json:"enabled"`
	Secrets     secretRequest `json:"secrets"`
}

func (body connectionRequest) create() (ConnectionCreate, error) {
	adapterKind := strings.ToLower(strings.TrimSpace(body.AdapterKind))
	if adapterKind == "" || len(adapterKind) > 64 {
		return ConnectionCreate{}, errors.New("adapterKind must be a non-empty string of at most 64 characters")
	}
	origin, err := normalizeOrigin(body.Origin)
	if err != nil {
		return ConnectionCreate{}, err
	}
	secrets, err := body.Secrets.validate()
	if err != nil {
		return ConnectionCreate{}, err
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	return ConnectionCreate{AdapterKind: adapterKind, Origin: origin, Enabled: enabled, Secrets: secrets}, nil
}

type updateConnectionRequest struct {
	AdapterKind *string `json:"adapterKind"`
	Origin      *string `json:"origin"`
	Enabled     *bool   `json:"enabled"`
}

func (body updateConnectionRequest) apply(current vnextstore.SiteAccountConnection, revision int64) (ConnectionUpdate, error) {
	if body.AdapterKind == nil && body.Origin == nil && body.Enabled == nil {
		return ConnectionUpdate{}, errors.New("at least one mutable field is required")
	}
	result := ConnectionUpdate{
		ExpectedRevision: revision,
		AdapterKind:      current.AdapterKind,
		Origin:           current.Origin,
		Enabled:          current.Enabled,
	}
	if body.AdapterKind != nil {
		result.AdapterKind = strings.ToLower(strings.TrimSpace(*body.AdapterKind))
		if result.AdapterKind == "" || len(result.AdapterKind) > 64 {
			return ConnectionUpdate{}, errors.New("adapterKind must be a non-empty string of at most 64 characters")
		}
	}
	if body.Origin != nil {
		origin, err := normalizeOrigin(*body.Origin)
		if err != nil {
			return ConnectionUpdate{}, err
		}
		result.Origin = origin
	}
	if body.Enabled != nil {
		result.Enabled = *body.Enabled
	}
	return result, nil
}

type secretRequest struct {
	Authorization string `json:"authorization"`
	AccessToken   string `json:"accessToken"`
	RefreshToken  string `json:"refreshToken"`
	Cookie        string `json:"cookie"`
	ExpiresAt     *int64 `json:"expiresAt"`
}

func (body secretRequest) validate() (siteadmin.Secrets, error) {
	for name, value := range map[string]string{
		"authorization": body.Authorization, "accessToken": body.AccessToken,
		"refreshToken": body.RefreshToken, "cookie": body.Cookie,
	} {
		if len(value) > 64<<10 {
			return siteadmin.Secrets{}, fmt.Errorf("%s exceeds the safety limit", name)
		}
	}
	result := siteadmin.Secrets{
		Authorization: strings.TrimSpace(body.Authorization),
		AccessToken:   strings.TrimSpace(body.AccessToken),
		RefreshToken:  strings.TrimSpace(body.RefreshToken),
		Cookie:        strings.TrimSpace(body.Cookie),
	}
	if result.Authorization == "" && result.AccessToken == "" && result.RefreshToken == "" && result.Cookie == "" {
		return siteadmin.Secrets{}, errors.New("secrets must contain at least one credential")
	}
	if body.ExpiresAt != nil {
		if *body.ExpiresAt <= 0 {
			return siteadmin.Secrets{}, errors.New("expiresAt must be a positive Unix millisecond timestamp")
		}
		result.ExpiresAt = time.UnixMilli(*body.ExpiresAt).UTC()
	}
	return result, nil
}

type syncUsageRequest struct {
	Cursor    string `json:"cursor"`
	From      *int64 `json:"from"`
	To        *int64 `json:"to"`
	Limit     int    `json:"limit"`
	Model     string `json:"model"`
	Status    string `json:"status"`
	APIKey    string `json:"apiKey"`
	RequestID string `json:"requestId"`
}

func (body syncUsageRequest) validate() (siteadmin.UsageQuery, error) {
	if body.Limit == 0 {
		body.Limit = 100
	}
	query := siteadmin.UsageQuery{
		Cursor: strings.TrimSpace(body.Cursor), Limit: body.Limit, Model: strings.TrimSpace(body.Model),
		Status: strings.TrimSpace(body.Status), APIKey: strings.TrimSpace(body.APIKey), RequestID: strings.TrimSpace(body.RequestID),
	}
	if body.From != nil {
		query.From = time.UnixMilli(*body.From).UTC()
	}
	if body.To != nil {
		query.To = time.UnixMilli(*body.To).UTC()
	}
	return query, query.Validate()
}

type connectionResponse struct {
	ID                   int64                `json:"id"`
	SiteID               int64                `json:"siteId"`
	SiteName             string               `json:"siteName"`
	AdapterKind          string               `json:"adapterKind"`
	Origin               string               `json:"origin"`
	SecretConfigured     bool                 `json:"secretConfigured"`
	Enabled              bool                 `json:"enabled"`
	Capabilities         capabilitiesResponse `json:"capabilities"`
	AdapterAvailable     bool                 `json:"adapterAvailable"`
	LastSessionRefreshAt *int64               `json:"lastSessionRefreshAt"`
	LastBalanceRefreshAt *int64               `json:"lastBalanceRefreshAt"`
	LastUsageRefreshAt   *int64               `json:"lastUsageRefreshAt"`
	LastErrorOperation   string               `json:"lastErrorOperation"`
	LastErrorCode        string               `json:"lastErrorCode"`
	LastErrorAt          *int64               `json:"lastErrorAt"`
	LatestBalance        *balanceResponse     `json:"latestBalance"`
	Revision             int64                `json:"revision"`
	CreatedAt            int64                `json:"createdAt"`
	UpdatedAt            int64                `json:"updatedAt"`
}

type capabilitiesResponse struct {
	SessionRefresh bool `json:"sessionRefresh"`
	Balance        bool `json:"balance"`
	Usage          bool `json:"usage"`
}

func (handler *Handler) connectionResponse(item vnextstore.SiteAccountConnection, balance *vnextstore.SiteBalanceSnapshot) connectionResponse {
	result := connectionResponse{
		ID: item.ID, SiteID: item.SiteID, SiteName: item.SiteName, AdapterKind: item.AdapterKind, Origin: item.Origin,
		SecretConfigured: item.SecretConfigured, Enabled: item.Enabled,
		LastSessionRefreshAt: cloneInt64(item.LastSessionRefreshAt), LastBalanceRefreshAt: cloneInt64(item.LastBalanceRefreshAt),
		LastUsageRefreshAt: cloneInt64(item.LastUsageRefreshAt), LastErrorOperation: item.LastErrorOperation,
		LastErrorCode: item.LastErrorCode, LastErrorAt: cloneInt64(item.LastErrorAt), Revision: item.Revision,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
	if adapter, err := handler.registry.Lookup(item.AdapterKind); err == nil && adapter != nil {
		result.AdapterAvailable = true
		result.Capabilities = capabilitiesResponse(adapter.Capabilities())
	}
	if balance != nil {
		value := newBalanceResponse(*balance)
		result.LatestBalance = &value
	}
	return result
}

type balanceResponse struct {
	ID              int64   `json:"id"`
	AccountRemoteID string  `json:"accountRemoteId"`
	AccountName     string  `json:"accountName"`
	AvailableValue  string  `json:"availableValue"`
	AvailableUnit   string  `json:"availableUnit"`
	UsedValue       *string `json:"usedValue"`
	UsedUnit        *string `json:"usedUnit"`
	CapturedAt      int64   `json:"capturedAt"`
}

func newBalanceResponse(item vnextstore.SiteBalanceSnapshot) balanceResponse {
	return balanceResponse{
		ID: item.ID, AccountRemoteID: item.AccountRemoteID, AccountName: item.AccountName,
		AvailableValue: item.AvailableValue, AvailableUnit: item.AvailableUnit,
		UsedValue: cloneString(item.UsedValue), UsedUnit: cloneString(item.UsedUnit), CapturedAt: item.CapturedAt,
	}
}

type usageResponse struct {
	ID                int64   `json:"id"`
	RemoteID          string  `json:"remoteId"`
	RequestID         string  `json:"requestId"`
	UpstreamRequestID string  `json:"upstreamRequestId"`
	OccurredAt        int64   `json:"occurredAt"`
	Model             string  `json:"model"`
	UpstreamModel     string  `json:"upstreamModel"`
	Status            string  `json:"status"`
	HTTPStatus        *int    `json:"httpStatus"`
	InputTokens       *int64  `json:"inputTokens"`
	OutputTokens      *int64  `json:"outputTokens"`
	CacheReadTokens   *int64  `json:"cacheReadTokens"`
	CacheWriteTokens  *int64  `json:"cacheWriteTokens"`
	ReasoningTokens   *int64  `json:"reasoningTokens"`
	TotalTokens       *int64  `json:"totalTokens"`
	ChargeValue       *string `json:"chargeValue"`
	ChargeUnit        *string `json:"chargeUnit"`
	DurationMS        *int64  `json:"durationMs"`
	APIKeyName        string  `json:"apiKeyName"`
	SourceFetchedAt   int64   `json:"sourceFetchedAt"`
}

func newUsageResponse(item vnextstore.SiteUsageRecord) usageResponse {
	return usageResponse{
		ID: item.ID, RemoteID: item.RemoteID, RequestID: item.RequestID, UpstreamRequestID: item.UpstreamRequestID,
		OccurredAt: item.OccurredAt, Model: item.Model, UpstreamModel: item.UpstreamModel, Status: item.Status,
		HTTPStatus: cloneInt(item.HTTPStatus), InputTokens: cloneInt64(item.InputTokens), OutputTokens: cloneInt64(item.OutputTokens),
		CacheReadTokens: cloneInt64(item.CacheReadTokens), CacheWriteTokens: cloneInt64(item.CacheWriteTokens),
		ReasoningTokens: cloneInt64(item.ReasoningTokens), TotalTokens: cloneInt64(item.TotalTokens),
		ChargeValue: cloneString(item.ChargeValue), ChargeUnit: cloneString(item.ChargeUnit), DurationMS: cloneInt64(item.DurationMS),
		APIKeyName: item.APIKeyName, SourceFetchedAt: item.SourceFetchedAt,
	}
}

func localUsageFilter(r *http.Request) (vnextstore.SiteUsageListFilter, error) {
	values := r.URL.Query()
	limit := 50
	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return vnextstore.SiteUsageListFilter{}, errors.New("limit must be an integer")
		}
		limit = parsed
	}
	filter := vnextstore.SiteUsageListFilter{
		Limit: limit, Model: values.Get("model"), Status: values.Get("status"), APIKey: values.Get("apiKey"),
		RequestID: values.Get("requestId"), Search: values.Get("search"),
	}
	if raw := strings.TrimSpace(values.Get("cursor")); raw != "" {
		occurredAt, id, err := decodeCursor(raw)
		if err != nil {
			return vnextstore.SiteUsageListFilter{}, err
		}
		filter.BeforeOccurredAt, filter.BeforeID = &occurredAt, &id
	}
	for name, target := range map[string]**int64{"from": &filter.From, "to": &filter.To} {
		if raw := strings.TrimSpace(values.Get(name)); raw != "" {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || parsed <= 0 {
				return vnextstore.SiteUsageListFilter{}, fmt.Errorf("%s must be a positive Unix millisecond timestamp", name)
			}
			value := parsed
			*target = &value
		}
	}
	if filter.From != nil && filter.To != nil && *filter.To < *filter.From {
		return vnextstore.SiteUsageListFilter{}, errors.New("to cannot be before from")
	}
	if filter.Limit < 1 || filter.Limit > 200 {
		return vnextstore.SiteUsageListFilter{}, errors.New("limit must be between 1 and 200")
	}
	return filter, nil
}

func normalizeOrigin(raw string) (string, error) {
	value := strings.TrimRight(strings.TrimSpace(raw), "/")
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("origin must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	return value, nil
}

func routeSegments(path string) ([]string, bool) {
	if path == apiPrefix {
		return []string{}, true
	}
	if !strings.HasPrefix(path, apiPrefix+"/") {
		return nil, false
	}
	rest := strings.TrimPrefix(path, apiPrefix+"/")
	if rest == "" || strings.HasSuffix(rest, "/") || strings.Contains(rest, "//") {
		return nil, false
	}
	return strings.Split(rest, "/"), true
}

func requiredIfMatch(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" {
		writeError(w, http.StatusPreconditionRequired, "precondition_required", "If-Match header is required")
		return 0, false
	}
	if strings.HasPrefix(raw, `"`) && strings.HasSuffix(raw, `"`) && len(raw) >= 2 {
		raw = raw[1 : len(raw)-1]
	}
	revision, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || revision <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_revision", "If-Match must contain one strong numeric revision")
		return 0, false
	}
	return revision, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes))
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

func writeRepositoryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, vnextstore.ErrRevisionConflict):
		writeError(w, http.StatusConflict, "revision_conflict", "resource changed; refresh and try again")
	case errors.Is(err, vnextstore.ErrConflict) || isUniqueConstraint(err):
		writeError(w, http.StatusConflict, "conflict", "resource already exists or conflicts with current state")
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, vnextstore.ErrSiteAccountUnavailable), errors.Is(err, siteadmin.ErrConnectionUnavailable):
		writeError(w, http.StatusNotFound, "not_found", "site account connection was not found or is disabled")
	case errors.Is(err, siteadmin.ErrAdapterUnavailable):
		writeError(w, http.StatusConflict, "adapter_unavailable", "configured adapter is not available")
	case errors.Is(err, siteadmin.ErrUnsupportedCapability):
		writeError(w, http.StatusUnprocessableEntity, "unsupported_capability", "configured adapter does not support this operation")
	case errors.Is(err, siteadmin.ErrSyncFailed):
		writeError(w, http.StatusBadGateway, "upstream_sync_failed", "upstream account synchronization failed")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "the request could not be completed")
	}
}

func isUniqueConstraint(err error) bool {
	message := strings.ToLower(fmt.Sprint(err))
	return strings.Contains(message, "unique constraint") || strings.Contains(message, "constraint failed: unique")
}

func methodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func writeValidationError(w http.ResponseWriter, err error) {
	writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeRevisionJSON(w http.ResponseWriter, status int, revision int64, value any) {
	w.Header().Set("ETag", `"`+strconv.FormatInt(revision, 10)+`"`)
	writeJSON(w, status, value)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func encodeCursor(occurredAt, id int64) string {
	payload := strconv.FormatInt(occurredAt, 10) + ":" + strconv.FormatInt(id, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

func decodeCursor(raw string) (int64, int64, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0, 0, errors.New("cursor is invalid")
	}
	parts := strings.Split(string(decoded), ":")
	if len(parts) != 2 {
		return 0, 0, errors.New("cursor is invalid")
	}
	occurredAt, timeErr := strconv.ParseInt(parts[0], 10, 64)
	id, idErr := strconv.ParseInt(parts[1], 10, 64)
	if timeErr != nil || idErr != nil || occurredAt <= 0 || id <= 0 {
		return 0, 0, errors.New("cursor is invalid")
	}
	return occurredAt, id, nil
}

func nullableTimeMillis(value time.Time) *int64 {
	if value.IsZero() {
		return nil
	}
	result := value.UTC().UnixMilli()
	return &result
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func clearSiteSecrets(secrets *siteadmin.Secrets) {
	if secrets == nil {
		return
	}
	*secrets = siteadmin.Secrets{}
}
