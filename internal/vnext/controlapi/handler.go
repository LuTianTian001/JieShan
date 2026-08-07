package controlapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/LuTianTian001/JieShan/internal/vnext/downstreamkeys"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const (
	apiPrefix       = "/api/vnext/downstream-keys"
	maxRequestBytes = 64 << 10
)

// KeyCreate contains the complete operator-controlled portion of a new key.
// KeyIssuer owns secret generation, digesting, record creation, and the
// record-bound encryption needed to make a newly issued key revealable.
type KeyCreate struct {
	Name                 string
	RoutingProfileID     *int64
	QuotaNanoUSD         *int64
	HourlyQuotaNanoUSD   *int64
	BillingMultiplierBPS int
	Expires              *int64
	Enabled              bool
}

type IssuedKey struct {
	Key       vnextstore.DownstreamKey
	RawSecret string
}

type KeyIssuer interface {
	IssueDownstreamKey(context.Context, KeyCreate) (IssuedKey, error)
}

// KeySecretManager owns all plaintext secret operations. The handler only
// validates HTTP preconditions and serializes the one-time result.
type KeySecretManager interface {
	RevealDownstreamKey(context.Context, int64) (string, error)
	RotateDownstreamKey(context.Context, int64, int64) (IssuedKey, error)
}

// KeyUpdate is a complete mutable snapshot. ExpectedRevision lets the store
// reject a concurrent change between the handler's read and write.
type KeyUpdate struct {
	ExpectedRevision     int64
	Name                 string
	RoutingProfileID     *int64
	QuotaNanoUSD         *int64
	HourlyQuotaNanoUSD   *int64
	BillingMultiplierBPS int
	Expires              *int64
	Enabled              bool
}

// Repository is deliberately narrower than the VNext store. Methods that do
// not yet exist on store.Store can be supplied by a store-owned adapter without
// moving SQL or secret handling into the HTTP layer.
type Repository interface {
	ListDownstreamKeys(context.Context) ([]vnextstore.DownstreamKey, error)
	GetDownstreamKey(context.Context, int64) (vnextstore.DownstreamKey, error)
	UpdateDownstreamKey(context.Context, int64, KeyUpdate) (vnextstore.DownstreamKey, error)
	ListRoutingProfileRoutes(context.Context, int64) ([]vnextstore.RoutingProfileRoute, error)
}

type Handler struct {
	repository Repository
	issuer     KeyIssuer
	secrets    KeySecretManager
}

func New(repository Repository, issuer KeyIssuer, secrets KeySecretManager) *Handler {
	return &Handler{repository: repository, issuer: issuer, secrets: secrets}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.repository == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "control API is unavailable")
		return
	}
	segments, ok := routeSegments(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}

	switch {
	case len(segments) == 0:
		h.handleKeyCollection(w, r)
	case len(segments) == 1:
		h.handleKey(w, r, segments[0])
	case len(segments) == 2 && segments[1] == "models":
		h.handleModels(w, r, segments[0])
	case len(segments) == 2 && segments[1] == "reveal":
		h.handleReveal(w, r, segments[0])
	case len(segments) == 2 && segments[1] == "rotate":
		h.handleRotate(w, r, segments[0])
	default:
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	}
}

func (h *Handler) handleKeyCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listKeys(w, r)
	case http.MethodPost:
		h.createKey(w, r)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (h *Handler) handleKey(w http.ResponseWriter, r *http.Request, rawKeyID string) {
	keyID, ok := positiveID(w, rawKeyID, "key")
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.getKey(w, r, keyID)
	case http.MethodPatch:
		h.updateKey(w, r, keyID)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPatch)
	}
}

func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request, rawKeyID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	keyID, ok := positiveID(w, rawKeyID, "key")
	if !ok {
		return
	}
	h.listModels(w, r, keyID)
}

func (h *Handler) handleReveal(w http.ResponseWriter, r *http.Request, rawKeyID string) {
	secretResponseHeaders(w)
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	keyID, ok := positiveID(w, rawKeyID, "key")
	if !ok {
		return
	}
	if h.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "key_secret_service_unavailable", "key secret service is unavailable")
		return
	}
	secret, err := h.secrets.RevealDownstreamKey(r.Context(), keyID)
	if err != nil {
		writeKeySecretError(w, err)
		return
	}
	if strings.TrimSpace(secret) == "" {
		writeError(w, http.StatusInternalServerError, "internal_error", "the request could not be completed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret})
}

func (h *Handler) handleRotate(w http.ResponseWriter, r *http.Request, rawKeyID string) {
	secretResponseHeaders(w)
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	keyID, ok := positiveID(w, rawKeyID, "key")
	if !ok {
		return
	}
	expectedRevision, ok := requiredIfMatch(w, r)
	if !ok {
		return
	}
	if h.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "key_secret_service_unavailable", "key secret service is unavailable")
		return
	}
	issued, err := h.secrets.RotateDownstreamKey(r.Context(), keyID, expectedRevision)
	if err != nil {
		writeKeySecretError(w, err)
		return
	}
	if issued.Key.ID <= 0 || strings.TrimSpace(issued.RawSecret) == "" {
		writeError(w, http.StatusInternalServerError, "internal_error", "the request could not be completed")
		return
	}
	w.Header().Set("ETag", revisionETag(issued.Key.Revision))
	writeJSON(w, http.StatusOK, map[string]any{
		"item":   newKeyResponse(issued.Key, h.bestEffortCommittedKeyModels(r.Context(), issued.Key.RoutingProfileID)),
		"secret": issued.RawSecret,
	})
}

func (h *Handler) listKeys(w http.ResponseWriter, r *http.Request) {
	items, err := h.repository.ListDownstreamKeys(r.Context())
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	result := make([]keyResponse, 0, len(items))
	for _, item := range items {
		routes, err := h.repository.ListRoutingProfileRoutes(r.Context(), item.RoutingProfileID)
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		result = append(result, newKeyResponse(item, visibleModelNames(routes)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": result})
}

func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) {
	if h.issuer == nil {
		writeError(w, http.StatusServiceUnavailable, "key_issuer_unavailable", "key issuance is unavailable")
		return
	}
	var body createKeyRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	input, ok := body.validate(w)
	if !ok {
		return
	}
	issued, err := h.issuer.IssueDownstreamKey(r.Context(), input)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	if issued.Key.ID <= 0 || strings.TrimSpace(issued.RawSecret) == "" {
		writeError(w, http.StatusInternalServerError, "internal_error", "the request could not be completed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("ETag", revisionETag(issued.Key.Revision))
	writeJSON(w, http.StatusCreated, map[string]any{
		"item":   newKeyResponse(issued.Key, h.bestEffortCommittedKeyModels(r.Context(), issued.Key.RoutingProfileID)),
		"secret": issued.RawSecret,
	})
}

// Once issue or rotation returns, the secret mutation has committed. Models
// are only a convenience projection for the response and must never turn that
// committed success into an error that withholds the plaintext from the caller.
func (h *Handler) bestEffortCommittedKeyModels(ctx context.Context, routingProfileID int64) []string {
	routes, err := h.repository.ListRoutingProfileRoutes(ctx, routingProfileID)
	if err != nil {
		return []string{}
	}
	return visibleModelNames(routes)
}

func (h *Handler) getKey(w http.ResponseWriter, r *http.Request, keyID int64) {
	item, err := h.repository.GetDownstreamKey(r.Context(), keyID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	routes, err := h.repository.ListRoutingProfileRoutes(r.Context(), item.RoutingProfileID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("ETag", revisionETag(item.Revision))
	writeJSON(w, http.StatusOK, map[string]any{"item": newKeyResponse(item, visibleModelNames(routes))})
}

func (h *Handler) updateKey(w http.ResponseWriter, r *http.Request, keyID int64) {
	expectedRevision, ok := requiredIfMatch(w, r)
	if !ok {
		return
	}
	current, err := h.repository.GetDownstreamKey(r.Context(), keyID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	if current.Revision != expectedRevision {
		writeError(w, http.StatusConflict, "revision_conflict", "resource changed; refresh and try again")
		return
	}
	var body updateKeyRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	input, ok := body.apply(w, current, expectedRevision)
	if !ok {
		return
	}
	updated, err := h.repository.UpdateDownstreamKey(r.Context(), keyID, input)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	routes, err := h.repository.ListRoutingProfileRoutes(r.Context(), updated.RoutingProfileID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("ETag", revisionETag(updated.Revision))
	writeJSON(w, http.StatusOK, map[string]any{"item": newKeyResponse(updated, visibleModelNames(routes))})
}

// listModels is an operator-facing projection of the key's effective routing
// profile. It intentionally does not claim runtime routability. The gateway's
// /v1/models resolver must additionally require endpoint protocol capability,
// an eligible bound credential, and current target health.
func (h *Handler) listModels(w http.ResponseWriter, r *http.Request, keyID int64) {
	key, err := h.repository.GetDownstreamKey(r.Context(), keyID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	routes, err := h.repository.ListRoutingProfileRoutes(r.Context(), key.RoutingProfileID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	items := make([]modelResponse, 0, len(routes))
	seen := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		if !route.Enabled || strings.TrimSpace(route.PublicName) == "" {
			continue
		}
		enabledTargets := len(route.Targets)
		if enabledTargets == 0 {
			continue
		}
		if _, duplicate := seen[route.PublicName]; duplicate {
			continue
		}
		seen[route.PublicName] = struct{}{}
		items = append(items, modelResponse{
			ID:                 route.PublicName,
			PublishedModelID:   route.PublishedModelID,
			RouteRevision:      route.Revision,
			EnabledTargetCount: enabledTargets,
			OfficialPriceSKU:   route.OfficialPriceSKU,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type optional[T any] struct {
	Set   bool
	Null  bool
	Value T
}

func (value *optional[T]) UnmarshalJSON(data []byte) error {
	value.Set = true
	if string(data) == "null" {
		value.Null = true
		return nil
	}
	return json.Unmarshal(data, &value.Value)
}

type createKeyRequest struct {
	Name                 optional[string] `json:"name"`
	RoutingProfileID     optional[int64]  `json:"routingProfileId"`
	QuotaNanoUSD         optional[int64]  `json:"quotaNanoUSD"`
	HourlyQuotaNanoUSD   optional[int64]  `json:"hourlyQuotaNanoUSD"`
	BillingMultiplierBPS optional[int]    `json:"billingMultiplierBPS"`
	Expires              optional[int64]  `json:"expires"`
	Enabled              optional[bool]   `json:"enabled"`
}

func (body createKeyRequest) validate(w http.ResponseWriter) (KeyCreate, bool) {
	if !body.Name.Set || body.Name.Null || strings.TrimSpace(body.Name.Value) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "name is required")
		return KeyCreate{}, false
	}
	name := strings.TrimSpace(body.Name.Value)
	if len(name) > 120 {
		writeError(w, http.StatusBadRequest, "invalid_request", "name must not exceed 120 characters")
		return KeyCreate{}, false
	}
	quota, ok := nullableNonNegativeInt64(w, body.QuotaNanoUSD, "quotaNanoUSD")
	if !ok {
		return KeyCreate{}, false
	}
	expires, ok := nullablePositiveInt64(w, body.Expires, "expires")
	if !ok {
		return KeyCreate{}, false
	}
	hourlyQuota, ok := nullableNonNegativeInt64(w, body.HourlyQuotaNanoUSD, "hourlyQuotaNanoUSD")
	if !ok {
		return KeyCreate{}, false
	}
	billingMultiplierBPS := vnextstore.DefaultBillingMultiplierBPS
	if body.BillingMultiplierBPS.Set {
		if body.BillingMultiplierBPS.Null || body.BillingMultiplierBPS.Value < 0 || body.BillingMultiplierBPS.Value > vnextstore.MaxBillingMultiplierBPS {
			writeError(w, http.StatusBadRequest, "invalid_request", "billingMultiplierBPS must be an integer between 0 and 10000000")
			return KeyCreate{}, false
		}
		billingMultiplierBPS = body.BillingMultiplierBPS.Value
	}
	enabled := true
	if body.Enabled.Set {
		if body.Enabled.Null {
			writeError(w, http.StatusBadRequest, "invalid_request", "enabled must be a boolean")
			return KeyCreate{}, false
		}
		enabled = body.Enabled.Value
	}
	routingProfileID, ok := nullablePositiveInt64(w, body.RoutingProfileID, "routingProfileId")
	if !ok {
		return KeyCreate{}, false
	}
	return KeyCreate{
		Name: name, RoutingProfileID: routingProfileID, QuotaNanoUSD: quota, HourlyQuotaNanoUSD: hourlyQuota,
		BillingMultiplierBPS: billingMultiplierBPS, Expires: expires, Enabled: enabled,
	}, true
}

type updateKeyRequest struct {
	Name                 optional[string] `json:"name"`
	RoutingProfileID     optional[int64]  `json:"routingProfileId"`
	QuotaNanoUSD         optional[int64]  `json:"quotaNanoUSD"`
	HourlyQuotaNanoUSD   optional[int64]  `json:"hourlyQuotaNanoUSD"`
	BillingMultiplierBPS optional[int]    `json:"billingMultiplierBPS"`
	Expires              optional[int64]  `json:"expires"`
	Enabled              optional[bool]   `json:"enabled"`
}

func (body updateKeyRequest) apply(w http.ResponseWriter, current vnextstore.DownstreamKey, expectedRevision int64) (KeyUpdate, bool) {
	if !body.Name.Set && !body.RoutingProfileID.Set && !body.QuotaNanoUSD.Set && !body.HourlyQuotaNanoUSD.Set &&
		!body.BillingMultiplierBPS.Set && !body.Expires.Set && !body.Enabled.Set {
		writeError(w, http.StatusBadRequest, "invalid_request", "at least one mutable field is required")
		return KeyUpdate{}, false
	}
	result := KeyUpdate{
		ExpectedRevision:     expectedRevision,
		Name:                 current.Name,
		RoutingProfileID:     routingProfileSelection(current),
		QuotaNanoUSD:         cloneInt64(current.QuotaNanoUSD),
		HourlyQuotaNanoUSD:   cloneInt64(current.HourlyQuotaNanoUSD),
		BillingMultiplierBPS: current.BillingMultiplierBPS,
		Expires:              cloneInt64(current.ExpiresAt),
		Enabled:              current.Enabled,
	}
	if body.RoutingProfileID.Set {
		value, ok := nullablePositiveInt64(w, body.RoutingProfileID, "routingProfileId")
		if !ok {
			return KeyUpdate{}, false
		}
		result.RoutingProfileID = value
	}
	if body.Name.Set {
		if body.Name.Null || strings.TrimSpace(body.Name.Value) == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "name must be a non-empty string")
			return KeyUpdate{}, false
		}
		result.Name = strings.TrimSpace(body.Name.Value)
		if len(result.Name) > 120 {
			writeError(w, http.StatusBadRequest, "invalid_request", "name must not exceed 120 characters")
			return KeyUpdate{}, false
		}
	}
	if body.QuotaNanoUSD.Set {
		value, ok := nullableNonNegativeInt64(w, body.QuotaNanoUSD, "quotaNanoUSD")
		if !ok {
			return KeyUpdate{}, false
		}
		result.QuotaNanoUSD = value
	}
	if body.HourlyQuotaNanoUSD.Set {
		value, ok := nullableNonNegativeInt64(w, body.HourlyQuotaNanoUSD, "hourlyQuotaNanoUSD")
		if !ok {
			return KeyUpdate{}, false
		}
		result.HourlyQuotaNanoUSD = value
	}
	if body.BillingMultiplierBPS.Set {
		if body.BillingMultiplierBPS.Null || body.BillingMultiplierBPS.Value < 0 || body.BillingMultiplierBPS.Value > vnextstore.MaxBillingMultiplierBPS {
			writeError(w, http.StatusBadRequest, "invalid_request", "billingMultiplierBPS must be an integer between 0 and 10000000")
			return KeyUpdate{}, false
		}
		result.BillingMultiplierBPS = body.BillingMultiplierBPS.Value
	}
	if body.Expires.Set {
		value, ok := nullablePositiveInt64(w, body.Expires, "expires")
		if !ok {
			return KeyUpdate{}, false
		}
		result.Expires = value
	}
	if body.Enabled.Set {
		if body.Enabled.Null {
			writeError(w, http.StatusBadRequest, "invalid_request", "enabled must be a boolean")
			return KeyUpdate{}, false
		}
		result.Enabled = body.Enabled.Value
	}
	return result, true
}

type keyResponse struct {
	ID                        int64    `json:"id"`
	Name                      string   `json:"name"`
	KeyPrefix                 string   `json:"keyPrefix"`
	Enabled                   bool     `json:"enabled"`
	Revealable                bool     `json:"revealable"`
	RoutingProfileID          int64    `json:"routingProfileId"`
	RoutingProfileName        string   `json:"routingProfileName"`
	UsesDefaultRoutingProfile bool     `json:"usesDefaultRoutingProfile"`
	QuotaNanoUSD              *int64   `json:"quotaNanoUSD"`
	UsedNanoUSD               int64    `json:"usedNanoUSD"`
	ReservedNanoUSD           int64    `json:"reservedNanoUSD"`
	HourlyQuotaNanoUSD        *int64   `json:"hourlyQuotaNanoUSD"`
	UsedThisHourNanoUSD       int64    `json:"usedThisHourNanoUSD"`
	ReservedThisHourNanoUSD   int64    `json:"reservedThisHourNanoUSD"`
	HourlyWindowStartedAt     int64    `json:"hourlyWindowStartedAt"`
	BillingMultiplierBPS      int      `json:"billingMultiplierBPS"`
	Expires                   *int64   `json:"expires"`
	LastUsedAt                *int64   `json:"lastUsedAt"`
	Revision                  int64    `json:"revision"`
	Models                    []string `json:"models"`
	CreatedAt                 int64    `json:"createdAt"`
	UpdatedAt                 int64    `json:"updatedAt"`
}

func newKeyResponse(item vnextstore.DownstreamKey, models []string) keyResponse {
	if models == nil {
		models = []string{}
	}
	return keyResponse{
		ID: item.ID, Name: item.Name, KeyPrefix: item.KeyPrefix, Enabled: item.Enabled, Revealable: item.Revealable,
		RoutingProfileID: item.RoutingProfileID, RoutingProfileName: item.RoutingProfileName,
		UsesDefaultRoutingProfile: item.UsesDefaultRoutingProfile,
		QuotaNanoUSD:              cloneInt64(item.QuotaNanoUSD), UsedNanoUSD: item.UsedNanoUSD, ReservedNanoUSD: item.ReservedNanoUSD,
		HourlyQuotaNanoUSD: cloneInt64(item.HourlyQuotaNanoUSD), UsedThisHourNanoUSD: item.UsedThisHourNanoUSD,
		ReservedThisHourNanoUSD: item.ReservedThisHourNanoUSD, HourlyWindowStartedAt: item.HourlyWindowStartedAt,
		BillingMultiplierBPS: item.BillingMultiplierBPS,
		Expires:              cloneInt64(item.ExpiresAt), LastUsedAt: cloneInt64(item.LastUsedAt), Revision: item.Revision,
		Models: models, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

type modelResponse struct {
	ID                 string `json:"id"`
	PublishedModelID   int64  `json:"publishedModelId"`
	RouteRevision      int64  `json:"routeRevision"`
	EnabledTargetCount int    `json:"enabledTargetCount"`
	OfficialPriceSKU   string `json:"officialPriceSKU"`
}

func visibleModelNames(routes []vnextstore.RoutingProfileRoute) []string {
	result := make([]string, 0, len(routes))
	seen := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		if !route.Enabled || strings.TrimSpace(route.PublicName) == "" || len(route.Targets) == 0 {
			continue
		}
		if _, duplicate := seen[route.PublicName]; duplicate {
			continue
		}
		seen[route.PublicName] = struct{}{}
		result = append(result, route.PublicName)
	}
	return result
}

func routeSegments(path string) ([]string, bool) {
	if path == apiPrefix || path == apiPrefix+"/" {
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

func positiveID(w http.ResponseWriter, raw, kind string) (int64, bool) {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("%s ID must be a positive integer", kind))
		return 0, false
	}
	return value, true
}

func requiredIfMatch(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" {
		writeError(w, http.StatusPreconditionRequired, "precondition_required", "If-Match header is required")
		return 0, false
	}
	if strings.Contains(raw, ",") || raw == "*" || strings.HasPrefix(raw, "W/") {
		writeError(w, http.StatusBadRequest, "invalid_revision", "If-Match must contain one strong numeric revision")
		return 0, false
	}
	if strings.HasPrefix(raw, `"`) || strings.HasSuffix(raw, `"`) {
		if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
			writeError(w, http.StatusBadRequest, "invalid_revision", "If-Match must contain one strong numeric revision")
			return 0, false
		}
		raw = raw[1 : len(raw)-1]
	}
	revision, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || revision <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_revision", "If-Match must contain one strong numeric revision")
		return 0, false
	}
	return revision, true
}

func revisionETag(revision int64) string {
	if revision <= 0 {
		return ""
	}
	return `"` + strconv.FormatInt(revision, 10) + `"`
}

func nullableNonNegativeInt64(w http.ResponseWriter, value optional[int64], field string) (*int64, bool) {
	if !value.Set || value.Null {
		return nil, true
	}
	if value.Value < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", field+" must be null or a non-negative integer")
		return nil, false
	}
	return cloneInt64(&value.Value), true
}

func nullablePositiveInt64(w http.ResponseWriter, value optional[int64], field string) (*int64, bool) {
	if !value.Set || value.Null {
		return nil, true
	}
	if value.Value <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", field+" must be null or a positive integer")
		return nil, false
	}
	return cloneInt64(&value.Value), true
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func routingProfileSelection(item vnextstore.DownstreamKey) *int64 {
	if item.UsesDefaultRoutingProfile {
		return nil
	}
	value := item.RoutingProfileID
	return &value
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

func methodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func secretResponseHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func writeKeySecretError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, downstreamkeys.ErrRecentReauthenticationRequired):
		writeError(w, http.StatusForbidden, "recent_reauthentication_required", "sign in again before revealing this key")
	case errors.Is(err, downstreamkeys.ErrNotRevealable):
		writeError(w, http.StatusConflict, "key_not_revealable", "this key cannot be revealed; rotate it to create a revealable key")
	case errors.Is(err, downstreamkeys.ErrKeyNotFound), errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, vnextstore.ErrRevisionConflict):
		writeError(w, http.StatusConflict, "revision_conflict", "resource changed; refresh and try again")
	case errors.Is(err, vnextstore.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", "resource already exists or conflicts with current state")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "the request could not be completed")
	}
}

func writeRepositoryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, vnextstore.ErrRevisionConflict):
		writeError(w, http.StatusConflict, "revision_conflict", "resource changed; refresh and try again")
	case errors.Is(err, vnextstore.ErrQuotaBelowCommitted):
		writeError(w, http.StatusConflict, "quota_conflict", "quota is below current usage or reservations")
	case errors.Is(err, vnextstore.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", "resource already exists or conflicts with current state")
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	case isUniqueConstraint(err):
		writeError(w, http.StatusConflict, "conflict", "resource already exists")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "the request could not be completed")
	}
}

func isUniqueConstraint(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint") || strings.Contains(message, "constraint failed: unique")
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
