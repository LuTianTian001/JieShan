package inventoryapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const modelDiscoveryPreviewTimeout = 30 * time.Second

func (handler *Handler) listSites(w http.ResponseWriter, r *http.Request) {
	items, err := handler.repository.ListSites(r.Context())
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	result := make([]siteResponse, 0, len(items))
	for _, item := range items {
		result = append(result, newSiteResponse(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": result})
}

func (handler *Handler) createSite(w http.ResponseWriter, r *http.Request) {
	var body createSiteRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	input, err := body.validate()
	if err != nil {
		writeValidationError(w, err)
		return
	}
	item, err := handler.repository.CreateSite(r.Context(), input)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("ETag", revisionETag(item.Revision))
	writeJSON(w, http.StatusCreated, map[string]any{"item": newSiteResponse(item)})
}

func (handler *Handler) getSite(w http.ResponseWriter, r *http.Request, siteID int64) {
	item, err := handler.repository.GetSite(r.Context(), siteID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("ETag", revisionETag(item.Revision))
	writeJSON(w, http.StatusOK, map[string]any{"item": newSiteResponse(item)})
}

func (handler *Handler) updateSite(w http.ResponseWriter, r *http.Request, siteID int64) {
	revision, ok := requiredIfMatch(w, r)
	if !ok {
		return
	}
	current, err := handler.repository.GetSite(r.Context(), siteID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	if current.Revision != revision {
		writeRepositoryError(w, vnextstore.ErrRevisionConflict)
		return
	}
	var body updateSiteRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	input, err := body.apply(current, revision)
	if err != nil {
		writeValidationError(w, err)
		return
	}
	item, err := handler.repository.UpdateSite(r.Context(), siteID, input)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("ETag", revisionETag(item.Revision))
	writeJSON(w, http.StatusOK, map[string]any{"item": newSiteResponse(item)})
}

func (handler *Handler) deleteSite(w http.ResponseWriter, r *http.Request, siteID int64) {
	revision, ok := requiredIfMatch(w, r)
	if !ok {
		return
	}
	if err := handler.repository.DeleteSite(r.Context(), siteID, revision); err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) listEndpoints(w http.ResponseWriter, r *http.Request, siteID int64) {
	items, err := handler.repository.ListSiteEndpoints(r.Context(), siteID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	result := make([]endpointResponse, 0, len(items))
	for _, item := range items {
		result = append(result, newEndpointResponse(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": result})
}

func (handler *Handler) createEndpoint(w http.ResponseWriter, r *http.Request, siteID int64) {
	var body createEndpointRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	input, err := body.validate()
	if err != nil {
		writeValidationError(w, err)
		return
	}
	item, err := handler.repository.CreateSiteEndpoint(r.Context(), siteID, input)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("ETag", revisionETag(item.Revision))
	writeJSON(w, http.StatusCreated, map[string]any{"item": newEndpointResponse(item)})
}

func (handler *Handler) getEndpoint(w http.ResponseWriter, r *http.Request, siteID, endpointID int64) {
	item, err := handler.repository.GetSiteEndpoint(r.Context(), siteID, endpointID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("ETag", revisionETag(item.Revision))
	writeJSON(w, http.StatusOK, map[string]any{"item": newEndpointResponse(item)})
}

func (handler *Handler) updateEndpoint(w http.ResponseWriter, r *http.Request, siteID, endpointID int64) {
	revision, ok := requiredIfMatch(w, r)
	if !ok {
		return
	}
	current, err := handler.repository.GetSiteEndpoint(r.Context(), siteID, endpointID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	if current.Revision != revision {
		writeRepositoryError(w, vnextstore.ErrRevisionConflict)
		return
	}
	var body updateEndpointRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	input, err := body.apply(current, revision)
	if err != nil {
		writeValidationError(w, err)
		return
	}
	item, err := handler.repository.UpdateSiteEndpoint(r.Context(), siteID, endpointID, input)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("ETag", revisionETag(item.Revision))
	writeJSON(w, http.StatusOK, map[string]any{"item": newEndpointResponse(item)})
}

func (handler *Handler) handleEndpointCredentials(w http.ResponseWriter, r *http.Request, siteID, endpointID int64) {
	switch r.Method {
	case http.MethodGet:
		items, err := handler.repository.ListEndpointCredentialBindings(r.Context(), siteID, endpointID)
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		result := make([]bindingResponse, 0, len(items))
		for _, item := range items {
			result = append(result, newBindingResponse(item))
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": result})
	case http.MethodPut:
		revision, ok := requiredIfMatch(w, r)
		if !ok {
			return
		}
		var body replaceCredentialBindingsRequest
		if !decodeJSON(w, r, &body) {
			return
		}
		ids, err := (replaceIDsRequest{IDs: body.CredentialIDs}).validate("credentialIds", true)
		if err != nil {
			writeValidationError(w, err)
			return
		}
		item, err := handler.repository.ReplaceEndpointCredentialBindings(r.Context(), siteID, endpointID, revision, ids)
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		bindings, err := handler.repository.ListEndpointCredentialBindings(r.Context(), siteID, endpointID)
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		result := make([]bindingResponse, 0, len(bindings))
		for _, binding := range bindings {
			result = append(result, newBindingResponse(binding))
		}
		w.Header().Set("ETag", revisionETag(item.Revision))
		writeJSON(w, http.StatusOK, map[string]any{"endpoint": newEndpointResponse(item), "items": result})
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPut)
	}
}

func (handler *Handler) listCredentials(w http.ResponseWriter, r *http.Request, siteID int64) {
	items, err := handler.repository.ListSiteCredentials(r.Context(), siteID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	result := make([]credentialResponse, 0, len(items))
	for _, item := range items {
		result = append(result, newCredentialResponse(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": result})
}

func (handler *Handler) createCredential(w http.ResponseWriter, r *http.Request, siteID int64) {
	var body credentialRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	input, err := body.create()
	body.Secret.Value = ""
	if err != nil {
		writeValidationError(w, err)
		return
	}
	defer clear(input.Secret)
	item, err := handler.repository.CreateSiteCredential(r.Context(), siteID, input)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("ETag", revisionETag(item.Credential.Revision))
	writeJSON(w, http.StatusCreated, map[string]any{"item": newCredentialResponse(item)})
}

func (handler *Handler) getCredential(w http.ResponseWriter, r *http.Request, siteID, credentialID int64) {
	item, err := handler.repository.GetSiteCredential(r.Context(), siteID, credentialID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("ETag", revisionETag(item.Credential.Revision))
	writeJSON(w, http.StatusOK, map[string]any{"item": newCredentialResponse(item)})
}

func (handler *Handler) updateCredential(w http.ResponseWriter, r *http.Request, siteID, credentialID int64) {
	revision, ok := requiredIfMatch(w, r)
	if !ok {
		return
	}
	current, err := handler.repository.GetSiteCredential(r.Context(), siteID, credentialID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	if current.Credential.Revision != revision {
		writeRepositoryError(w, vnextstore.ErrRevisionConflict)
		return
	}
	var body updateCredentialRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	input, err := body.apply(current.Credential, revision)
	if err != nil {
		writeValidationError(w, err)
		return
	}
	item, err := handler.repository.UpdateSiteCredential(r.Context(), siteID, credentialID, input)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("ETag", revisionETag(item.Credential.Revision))
	writeJSON(w, http.StatusOK, map[string]any{"item": newCredentialResponse(item)})
}

func (handler *Handler) replaceCredentialSecret(w http.ResponseWriter, r *http.Request, siteID, credentialID int64) {
	revision, ok := requiredIfMatch(w, r)
	if !ok {
		return
	}
	var body replaceSecretRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	input, err := body.validate(revision)
	body.Secret.Value = ""
	if err != nil {
		writeValidationError(w, err)
		return
	}
	defer clear(input.Secret)
	item, err := handler.repository.ReplaceSiteCredentialSecret(r.Context(), siteID, credentialID, input)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("ETag", revisionETag(item.Credential.Revision))
	writeJSON(w, http.StatusOK, map[string]any{"item": newCredentialResponse(item)})
}

func (handler *Handler) listProviderModels(w http.ResponseWriter, r *http.Request, siteID, endpointID int64) {
	items, err := handler.repository.ListProviderModelTargets(r.Context(), siteID, endpointID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	result := make([]providerModelResponse, 0, len(items))
	for _, item := range items {
		result = append(result, newProviderModelResponse(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": result})
}

func (handler *Handler) createProviderModel(w http.ResponseWriter, r *http.Request, siteID, endpointID int64) {
	var body createProviderModelRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	input, err := body.validate(siteID, endpointID)
	if err != nil {
		writeValidationError(w, err)
		return
	}
	item, err := handler.repository.CreateProviderModelTarget(r.Context(), input)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("ETag", revisionETag(item.Revision))
	writeJSON(w, http.StatusCreated, map[string]any{"item": newProviderModelResponse(item)})
}

func (handler *Handler) getProviderModel(w http.ResponseWriter, r *http.Request, siteID, endpointID, targetID int64) {
	item, err := handler.repository.GetProviderModelTarget(r.Context(), siteID, endpointID, targetID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("ETag", revisionETag(item.Revision))
	writeJSON(w, http.StatusOK, map[string]any{"item": newProviderModelResponse(item)})
}

func (handler *Handler) updateProviderModel(w http.ResponseWriter, r *http.Request, siteID, endpointID, targetID int64) {
	revision, ok := requiredIfMatch(w, r)
	if !ok {
		return
	}
	current, err := handler.repository.GetProviderModelTarget(r.Context(), siteID, endpointID, targetID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	if current.Revision != revision {
		writeRepositoryError(w, vnextstore.ErrRevisionConflict)
		return
	}
	var body updateProviderModelRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	input, err := body.apply(current, revision)
	if err != nil {
		writeValidationError(w, err)
		return
	}
	item, err := handler.repository.UpdateProviderModelTarget(r.Context(), siteID, endpointID, targetID, input)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	w.Header().Set("ETag", revisionETag(item.Revision))
	writeJSON(w, http.StatusOK, map[string]any{"item": newProviderModelResponse(item)})
}

func (handler *Handler) discoverModels(w http.ResponseWriter, r *http.Request, siteID, endpointID int64) {
	var body discoverModelsRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	credentialID, err := body.validate()
	if err != nil {
		writeValidationError(w, err)
		return
	}
	discovery, err := handler.repository.DiscoverModels(r.Context(), siteID, endpointID, credentialID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	existing, err := handler.repository.ListProviderModelTargets(r.Context(), siteID, endpointID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	byName := make(map[string]vnextstore.ProviderModelTarget, len(existing))
	for _, target := range existing {
		byName[target.SourceModel] = target
	}
	type discoveredModelResponse struct {
		SourceModel string `json:"sourceModel"`
		Imported    bool   `json:"imported"`
		TargetID    *int64 `json:"targetId"`
		Enabled     *bool  `json:"enabled"`
		Revision    *int64 `json:"revision"`
	}
	result := make([]discoveredModelResponse, 0, len(discovery.Models))
	for _, model := range discovery.Models {
		item := discoveredModelResponse{SourceModel: model}
		if target, found := byName[model]; found {
			item.Imported = true
			item.TargetID = &target.ID
			item.Enabled = &target.Enabled
			item.Revision = &target.Revision
		}
		result = append(result, item)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"items": result, "credentialId": credentialID, "complete": discovery.Complete})
}

func (handler *Handler) previewModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	var body previewModelsRequest
	defer func() { body.APIKey.Value = "" }()
	if !decodeJSON(w, r, &body) {
		return
	}
	input, err := body.validate()
	if err != nil {
		writeValidationError(w, err)
		return
	}
	defer clear(input.Secret)
	ctx, cancel := context.WithTimeout(r.Context(), modelDiscoveryPreviewTimeout)
	defer cancel()
	discovery, err := handler.repository.PreviewModels(ctx, input)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"models": discovery.Models, "complete": discovery.Complete,
	})
}

func (handler *Handler) importProviderModels(w http.ResponseWriter, r *http.Request, siteID, endpointID int64) {
	var body importModelsRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	credentialID, models, err := body.validate()
	if err != nil {
		writeValidationError(w, err)
		return
	}
	items, err := handler.repository.ImportProviderModelTargets(r.Context(), siteID, endpointID, credentialID, models)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	result := make([]providerModelResponse, 0, len(items))
	for _, item := range items {
		result = append(result, newProviderModelResponse(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": result})
}

func (handler *Handler) handleModelTargetCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	items, err := handler.repository.ListProviderModelTargetCatalog(r.Context())
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	protocolFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("protocol")))
	surfaceFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("surface")))
	var siteFilter int64
	if raw := strings.TrimSpace(r.URL.Query().Get("siteId")); raw != "" {
		siteFilter, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || siteFilter <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "siteId must be a positive integer")
			return
		}
	}
	var enabledFilter *bool
	if raw := strings.TrimSpace(r.URL.Query().Get("enabled")); raw != "" {
		value, parseErr := strconv.ParseBool(raw)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "enabled must be true or false")
			return
		}
		enabledFilter = &value
	}
	result := make([]modelTargetCatalogResponse, 0, len(items))
	for _, item := range items {
		target := item.Target
		if siteFilter > 0 && target.SiteID != siteFilter {
			continue
		}
		if protocolFilter != "" && strings.ToLower(target.WireProtocol) != protocolFilter {
			continue
		}
		if surfaceFilter != "" && strings.ToLower(target.Surface) != surfaceFilter {
			continue
		}
		if enabledFilter != nil && target.Enabled != *enabledFilter {
			continue
		}
		if query != "" {
			haystack := strings.ToLower(strings.Join([]string{
				target.SourceModel, target.DisplayName, target.SiteName, target.EndpointName,
			}, " "))
			if !strings.Contains(haystack, query) {
				continue
			}
		}
		result = append(result, newModelTargetCatalogResponse(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": result})
}
