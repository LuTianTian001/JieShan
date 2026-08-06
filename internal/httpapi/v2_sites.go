package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/LuTianTian001/JieShan/internal/accountsync"
	"github.com/LuTianTian001/JieShan/internal/inferenceprotocol"
	"github.com/LuTianTian001/JieShan/internal/store"
)

type sitePayload struct {
	Name         string `json:"name"`
	DashboardURL string `json:"dashboardUrl"`
	Enabled      *bool  `json:"enabled"`
	Revision     int64  `json:"revision"`
}

type endpointPayload struct {
	Name                 string            `json:"name"`
	BaseURL              string            `json:"baseUrl"`
	WireProtocol         string            `json:"wireProtocol"`
	CompatibilityProfile string            `json:"compatibilityProfile"`
	AuthScheme           string            `json:"authScheme"`
	CustomHeaders        map[string]string `json:"customHeaders"`
	Enabled              *bool             `json:"enabled"`
	Revision             int64             `json:"revision"`
}

type credentialPayload struct {
	Name     string  `json:"name"`
	APIKey   *string `json:"apiKey"`
	Enabled  *bool   `json:"enabled"`
	Revision int64   `json:"revision"`
}

type orderPayload struct {
	IDs      []int64 `json:"ids"`
	Revision int64   `json:"revision"`
}

type siteDetail struct {
	Site        store.Site                  `json:"site"`
	Endpoints   []store.InferenceEndpoint   `json:"endpoints"`
	Credentials []store.InferenceCredential `json:"credentials"`
}

func (s *Server) registerV2SiteRoutes() {
	s.mux.Handle("GET /api/v2/sites", s.admin(http.HandlerFunc(s.listSitesV2)))
	s.mux.Handle("POST /api/v2/sites", s.admin(http.HandlerFunc(s.createSiteV2)))
	s.mux.Handle("GET /api/v2/sites/{id}", s.admin(http.HandlerFunc(s.getSiteV2)))
	s.mux.Handle("PATCH /api/v2/sites/{id}", s.admin(http.HandlerFunc(s.updateSiteV2)))
	s.mux.Handle("DELETE /api/v2/sites/{id}", s.admin(http.HandlerFunc(s.deleteSiteV2)))

	s.mux.Handle("GET /api/v2/sites/{id}/endpoints", s.admin(http.HandlerFunc(s.listEndpointsV2)))
	s.mux.Handle("POST /api/v2/sites/{id}/endpoints", s.admin(http.HandlerFunc(s.createEndpointV2)))
	s.mux.Handle("PUT /api/v2/sites/{id}/endpoints/order", s.admin(http.HandlerFunc(s.reorderEndpointsV2)))
	s.mux.Handle("PATCH /api/v2/endpoints/{id}", s.admin(http.HandlerFunc(s.updateEndpointV2)))
	s.mux.Handle("DELETE /api/v2/endpoints/{id}", s.admin(http.HandlerFunc(s.deleteEndpointV2)))

	s.mux.Handle("GET /api/v2/sites/{id}/credentials", s.admin(http.HandlerFunc(s.listCredentialsV2)))
	s.mux.Handle("POST /api/v2/sites/{id}/credentials", s.admin(http.HandlerFunc(s.createCredentialV2)))
	s.mux.Handle("PUT /api/v2/sites/{id}/credentials/order", s.admin(http.HandlerFunc(s.reorderCredentialsV2)))
	s.mux.Handle("PATCH /api/v2/credentials/{id}", s.admin(http.HandlerFunc(s.updateCredentialV2)))
	s.mux.Handle("DELETE /api/v2/credentials/{id}", s.admin(http.HandlerFunc(s.deleteCredentialV2)))

	s.mux.Handle("GET /api/v2/sites/{id}/account", s.admin(http.HandlerFunc(s.getSiteAccountV2)))
	s.mux.Handle("PUT /api/v2/sites/{id}/account", s.admin(http.HandlerFunc(s.configureSiteAccountV2)))
	s.mux.Handle("DELETE /api/v2/sites/{id}/account", s.admin(http.HandlerFunc(s.deleteSiteAccountV2)))
	s.mux.Handle("POST /api/v2/sites/{id}/account/refresh", s.admin(http.HandlerFunc(s.refreshSiteAccountV2)))
	s.mux.Handle("GET /api/v2/sites/{id}/account/usage", s.admin(http.HandlerFunc(s.listSiteAccountUsageV2)))
}

func (s *Server) listSitesV2(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListSiteSummaries(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createSiteV2(w http.ResponseWriter, r *http.Request) {
	var body sitePayload
	if !decodeJSON(w, r, &body) {
		return
	}
	id, err := s.store.CreateSite(r.Context(), store.SiteWrite{
		Name: body.Name, DashboardURL: body.DashboardURL, Enabled: boolDefault(body.Enabled, true),
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	item, err := s.store.GetSite(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"item": item})
}

func (s *Server) getSiteV2(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	detail, err := s.loadSiteDetail(r, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": detail})
}

func (s *Server) loadSiteDetail(r *http.Request, id int64) (siteDetail, error) {
	item, err := s.store.GetSite(r.Context(), id)
	if err != nil {
		return siteDetail{}, err
	}
	endpoints, err := s.store.ListInferenceEndpoints(r.Context(), id)
	if err != nil {
		return siteDetail{}, err
	}
	credentials, err := s.store.ListInferenceCredentials(r.Context(), id)
	if err != nil {
		return siteDetail{}, err
	}
	return siteDetail{Site: item, Endpoints: endpoints, Credentials: credentials}, nil
}

func (s *Server) updateSiteV2(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	current, err := s.store.GetSite(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var body sitePayload
	if !decodeJSON(w, r, &body) {
		return
	}
	revision, ok := requestRevision(w, r, body.Revision)
	if !ok {
		return
	}
	if err := s.store.UpdateSite(r.Context(), id, revision, store.SiteWrite{
		Name: firstNonEmpty(body.Name, current.Name), DashboardURL: firstNonEmpty(body.DashboardURL, current.DashboardURL),
		Enabled: boolDefault(body.Enabled, current.Enabled),
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	item, _ := s.store.GetSite(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) deleteSiteV2(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	revision, ok := requestRevision(w, r, 0)
	if !ok {
		return
	}
	if err := s.store.DeleteSite(r.Context(), id, revision); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listEndpointsV2(w http.ResponseWriter, r *http.Request) {
	s.listSiteChildrenV2(w, r, func(id int64) (any, error) { return s.store.ListInferenceEndpoints(r.Context(), id) })
}

func (s *Server) createEndpointV2(w http.ResponseWriter, r *http.Request) {
	siteID, ok := pathID(w, r)
	if !ok {
		return
	}
	var body endpointPayload
	if !decodeJSON(w, r, &body) {
		return
	}
	headers, err := json.Marshal(body.CustomHeaders)
	if err != nil {
		writeError(w, http.StatusBadRequest, "customHeaders must be a string map", "invalid_request")
		return
	}
	revision, ok := requestRevision(w, r, body.Revision)
	if !ok {
		return
	}
	id, err := s.store.CreateInferenceEndpoint(r.Context(), siteID, revision, store.InferenceEndpointWrite{
		Name: firstNonEmpty(body.Name, "Primary"), BaseURL: body.BaseURL,
		WireProtocol:         firstNonEmpty(body.WireProtocol, "openai"),
		CompatibilityProfile: firstNonEmpty(body.CompatibilityProfile, "generic"),
		AuthScheme:           body.AuthScheme, CustomHeaders: headers,
		Enabled: boolDefault(body.Enabled, true),
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	item, _ := s.store.GetInferenceEndpoint(r.Context(), id)
	writeJSON(w, http.StatusCreated, map[string]any{"item": item})
}

func (s *Server) updateEndpointV2(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	current, err := s.store.GetInferenceEndpoint(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var body endpointPayload
	if !decodeJSON(w, r, &body) {
		return
	}
	headers := current.CustomHeaders
	if body.CustomHeaders != nil {
		headers, _ = json.Marshal(body.CustomHeaders)
	}
	revision, ok := requestRevision(w, r, body.Revision)
	if !ok {
		return
	}
	protocol := firstNonEmpty(body.WireProtocol, current.WireProtocol)
	authScheme := strings.TrimSpace(body.AuthScheme)
	if authScheme == "" {
		if !strings.EqualFold(protocol, current.WireProtocol) {
			authScheme = inferenceprotocol.DefaultAuthScheme(protocol)
		} else {
			authScheme = current.AuthScheme
		}
	}
	if err := s.store.UpdateInferenceEndpoint(r.Context(), id, revision, store.InferenceEndpointWrite{
		Name: firstNonEmpty(body.Name, current.Name), BaseURL: firstNonEmpty(body.BaseURL, current.BaseURL),
		WireProtocol:         protocol,
		CompatibilityProfile: firstNonEmpty(body.CompatibilityProfile, current.CompatibilityProfile),
		AuthScheme:           authScheme, CustomHeaders: headers,
		Enabled: boolDefault(body.Enabled, current.Enabled),
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	item, _ := s.store.GetInferenceEndpoint(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) deleteEndpointV2(w http.ResponseWriter, r *http.Request) {
	s.deleteVersionedV2(w, r, s.store.DeleteInferenceEndpoint)
}

func (s *Server) reorderEndpointsV2(w http.ResponseWriter, r *http.Request) {
	s.reorderSiteChildrenV2(w, r, s.store.ReorderInferenceEndpoints)
}

func (s *Server) listCredentialsV2(w http.ResponseWriter, r *http.Request) {
	s.listSiteChildrenV2(w, r, func(id int64) (any, error) { return s.store.ListInferenceCredentials(r.Context(), id) })
}

func (s *Server) createCredentialV2(w http.ResponseWriter, r *http.Request) {
	siteID, ok := pathID(w, r)
	if !ok {
		return
	}
	var body credentialPayload
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.APIKey == nil || strings.TrimSpace(*body.APIKey) == "" {
		writeError(w, http.StatusBadRequest, "apiKey is required", "invalid_request")
		return
	}
	secret, err := s.cipher.Encrypt(strings.TrimSpace(*body.APIKey))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	revision, ok := requestRevision(w, r, body.Revision)
	if !ok {
		return
	}
	id, err := s.store.CreateInferenceCredential(r.Context(), siteID, revision, store.InferenceCredentialWrite{
		Name: firstNonEmpty(body.Name, "Default"), SecretCipher: secret, Enabled: boolDefault(body.Enabled, true),
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	item, _ := s.store.GetInferenceCredential(r.Context(), id)
	writeJSON(w, http.StatusCreated, map[string]any{"item": item})
}

func (s *Server) updateCredentialV2(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	current, err := s.store.GetInferenceCredential(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var body credentialPayload
	if !decodeJSON(w, r, &body) {
		return
	}
	input := store.InferenceCredentialUpdate{
		Name: firstNonEmpty(body.Name, current.Name), Enabled: boolDefault(body.Enabled, current.Enabled),
	}
	if body.APIKey != nil {
		if strings.TrimSpace(*body.APIKey) == "" {
			writeError(w, http.StatusBadRequest, "apiKey cannot be empty", "invalid_request")
			return
		}
		input.SecretCipher, err = s.cipher.Encrypt(strings.TrimSpace(*body.APIKey))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		input.ReplaceSecret = true
	}
	revision, ok := requestRevision(w, r, body.Revision)
	if !ok {
		return
	}
	if err := s.store.UpdateInferenceCredential(r.Context(), id, revision, input); err != nil {
		writeStoreError(w, err)
		return
	}
	item, _ := s.store.GetInferenceCredential(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) deleteCredentialV2(w http.ResponseWriter, r *http.Request) {
	s.deleteVersionedV2(w, r, s.store.DeleteInferenceCredential)
}

func (s *Server) reorderCredentialsV2(w http.ResponseWriter, r *http.Request) {
	s.reorderSiteChildrenV2(w, r, s.store.ReorderInferenceCredentials)
}

func (s *Server) getSiteAccountV2(w http.ResponseWriter, r *http.Request) {
	if !s.requireAccounts(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	account, err := s.accounts.GetSite(r.Context(), id)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": account})
}

func (s *Server) configureSiteAccountV2(w http.ResponseWriter, r *http.Request) {
	if !s.requireAccounts(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body accountsync.ConfigureInput
	if !decodeJSON(w, r, &body) {
		return
	}
	account, err := s.accounts.ConfigureSite(r.Context(), id, body)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": account})
}

func (s *Server) deleteSiteAccountV2(w http.ResponseWriter, r *http.Request) {
	if !s.requireAccounts(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.accounts.DeleteSite(r.Context(), id); err != nil {
		writeAccountError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) refreshSiteAccountV2(w http.ResponseWriter, r *http.Request) {
	if !s.requireAccounts(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	account, err := s.accounts.RefreshSite(r.Context(), id)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": account})
}

func (s *Server) listSiteAccountUsageV2(w http.ResponseWriter, r *http.Request) {
	if !s.requireAccounts(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rangeName := firstNonEmpty(strings.TrimSpace(r.URL.Query().Get("range")), "7d")
	limit, beforeID, ok := accountUsageQuery(w, r)
	if !ok {
		return
	}
	result, err := s.accounts.SiteUsage(r.Context(), id, rangeName, limit, beforeID)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func accountUsageQuery(w http.ResponseWriter, r *http.Request) (int, int64, bool) {
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 100", "invalid_request")
			return 0, 0, false
		}
		limit = parsed
	}
	var beforeID int64
	if raw := strings.TrimSpace(r.URL.Query().Get("beforeId")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 1 {
			writeError(w, http.StatusBadRequest, "beforeId must be a positive integer", "invalid_request")
			return 0, 0, false
		}
		beforeID = parsed
	}
	return limit, beforeID, true
}

func (s *Server) listSiteChildrenV2(w http.ResponseWriter, r *http.Request, load func(int64) (any, error)) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	items, err := load(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) deleteVersionedV2(w http.ResponseWriter, r *http.Request, remove func(context.Context, int64, int64) error) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	revision, ok := requestRevision(w, r, 0)
	if !ok {
		return
	}
	if err := remove(r.Context(), id, revision); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) reorderSiteChildrenV2(w http.ResponseWriter, r *http.Request, reorder func(context.Context, int64, int64, []int64) error) {
	siteID, ok := pathID(w, r)
	if !ok {
		return
	}
	var body orderPayload
	if !decodeJSON(w, r, &body) {
		return
	}
	revision, ok := requestRevision(w, r, body.Revision)
	if !ok {
		return
	}
	if err := reorder(r.Context(), siteID, revision, body.IDs); err != nil {
		writeStoreError(w, err)
		return
	}
	item, err := s.store.GetSite(r.Context(), siteID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": item.Revision})
}

func requestRevision(w http.ResponseWriter, r *http.Request, bodyRevision int64) (int64, bool) {
	if bodyRevision > 0 {
		return bodyRevision, true
	}
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("revision"))
	}
	raw = strings.TrimPrefix(raw, "W/")
	raw = strings.Trim(raw, `"`)
	if raw == "" {
		return 0, true
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 1 {
		writeError(w, http.StatusBadRequest, "revision must be a positive integer", "invalid_revision")
		return 0, false
	}
	return value, true
}
