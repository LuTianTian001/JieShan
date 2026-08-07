package inventoryapi

import (
	"errors"
	"net/http"
)

const apiPrefix = "/api/vnext/inventory"

type Handler struct {
	repository        Repository
	platformDetector  PlatformDetector
	tokenJSONPreviews *tokenJSONPreviewStore
}

func New(repository Repository) (*Handler, error) {
	if repository == nil {
		return nil, errNilRepository
	}
	return &Handler{repository: repository, tokenJSONPreviews: newTokenJSONPreviewStore()}, nil
}

func NewWithPlatformDetector(repository Repository, detector PlatformDetector) (*Handler, error) {
	handler, err := New(repository)
	if err != nil {
		return nil, err
	}
	if detector == nil {
		return nil, errors.New("platform detector is required")
	}
	handler.platformDetector = detector
	return handler, nil
}

func (handler *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	segments, ok := routeSegments(r.URL.Path)
	if !ok || len(segments) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	switch segments[0] {
	case "sites":
		handler.handleSites(w, r, segments[1:])
	case "model-discovery":
		if len(segments) != 2 || segments[1] != "preview" {
			writeError(w, http.StatusNotFound, "not_found", "resource not found")
			return
		}
		handler.handleModelDiscoveryPreview(w, r)
	case "model-targets":
		if len(segments) != 1 {
			writeError(w, http.StatusNotFound, "not_found", "resource not found")
			return
		}
		handler.handleModelTargetCatalog(w, r)
	default:
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	}
}

func (handler *Handler) handleModelDiscoveryPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	handler.previewModels(w, r)
}

func (handler *Handler) handleSites(w http.ResponseWriter, r *http.Request, segments []string) {
	if len(segments) == 0 {
		switch r.Method {
		case http.MethodGet:
			handler.listSites(w, r)
		case http.MethodPost:
			handler.createSite(w, r)
		default:
			methodNotAllowed(w, http.MethodGet, http.MethodPost)
		}
		return
	}
	siteID, ok := positiveID(w, segments[0], "site")
	if !ok {
		return
	}
	if len(segments) == 1 {
		switch r.Method {
		case http.MethodGet:
			handler.getSite(w, r, siteID)
		case http.MethodPatch:
			handler.updateSite(w, r, siteID)
		case http.MethodDelete:
			handler.deleteSite(w, r, siteID)
		default:
			methodNotAllowed(w, http.MethodGet, http.MethodPatch, http.MethodDelete)
		}
		return
	}
	switch segments[1] {
	case "endpoints":
		handler.handleEndpoints(w, r, siteID, segments[2:])
	case "credentials":
		handler.handleCredentials(w, r, siteID, segments[2:])
	case "platform-detection":
		if len(segments) != 2 {
			writeError(w, http.StatusNotFound, "not_found", "resource not found")
			return
		}
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		handler.getPlatformDetection(w, r, siteID)
	case "token-json":
		if len(segments) != 3 {
			writeError(w, http.StatusNotFound, "not_found", "resource not found")
			return
		}
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		switch segments[2] {
		case "preview":
			handler.previewTokenJSON(w, r, siteID)
		case "import":
			handler.importTokenJSON(w, r, siteID)
		default:
			writeError(w, http.StatusNotFound, "not_found", "resource not found")
		}
	default:
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	}
}

func (handler *Handler) handleEndpoints(w http.ResponseWriter, r *http.Request, siteID int64, segments []string) {
	if len(segments) == 0 {
		switch r.Method {
		case http.MethodGet:
			handler.listEndpoints(w, r, siteID)
		case http.MethodPost:
			handler.createEndpoint(w, r, siteID)
		default:
			methodNotAllowed(w, http.MethodGet, http.MethodPost)
		}
		return
	}
	endpointID, ok := positiveID(w, segments[0], "endpoint")
	if !ok {
		return
	}
	if len(segments) == 1 {
		switch r.Method {
		case http.MethodGet:
			handler.getEndpoint(w, r, siteID, endpointID)
		case http.MethodPatch:
			handler.updateEndpoint(w, r, siteID, endpointID)
		default:
			methodNotAllowed(w, http.MethodGet, http.MethodPatch)
		}
		return
	}
	switch segments[1] {
	case "credentials":
		if len(segments) != 2 {
			writeError(w, http.StatusNotFound, "not_found", "resource not found")
			return
		}
		handler.handleEndpointCredentials(w, r, siteID, endpointID)
	case "models":
		handler.handleProviderModels(w, r, siteID, endpointID, segments[2:])
	default:
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	}
}

func (handler *Handler) handleCredentials(w http.ResponseWriter, r *http.Request, siteID int64, segments []string) {
	if len(segments) == 0 {
		switch r.Method {
		case http.MethodGet:
			handler.listCredentials(w, r, siteID)
		case http.MethodPost:
			handler.createCredential(w, r, siteID)
		default:
			methodNotAllowed(w, http.MethodGet, http.MethodPost)
		}
		return
	}
	credentialID, ok := positiveID(w, segments[0], "credential")
	if !ok {
		return
	}
	if len(segments) == 1 {
		switch r.Method {
		case http.MethodGet:
			handler.getCredential(w, r, siteID, credentialID)
		case http.MethodPatch:
			handler.updateCredential(w, r, siteID, credentialID)
		default:
			methodNotAllowed(w, http.MethodGet, http.MethodPatch)
		}
		return
	}
	if len(segments) == 2 && segments[1] == "secret" {
		if r.Method != http.MethodPut {
			methodNotAllowed(w, http.MethodPut)
			return
		}
		handler.replaceCredentialSecret(w, r, siteID, credentialID)
		return
	}
	writeError(w, http.StatusNotFound, "not_found", "resource not found")
}

func (handler *Handler) handleProviderModels(w http.ResponseWriter, r *http.Request, siteID, endpointID int64, segments []string) {
	if len(segments) == 0 {
		switch r.Method {
		case http.MethodGet:
			handler.listProviderModels(w, r, siteID, endpointID)
		case http.MethodPost:
			handler.createProviderModel(w, r, siteID, endpointID)
		default:
			methodNotAllowed(w, http.MethodGet, http.MethodPost)
		}
		return
	}
	if len(segments) == 1 && segments[0] == "discover" {
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		handler.discoverModels(w, r, siteID, endpointID)
		return
	}
	if len(segments) == 1 && segments[0] == "import" {
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		handler.importProviderModels(w, r, siteID, endpointID)
		return
	}
	if len(segments) != 1 {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	targetID, ok := positiveID(w, segments[0], "provider model target")
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		handler.getProviderModel(w, r, siteID, endpointID, targetID)
	case http.MethodPatch:
		handler.updateProviderModel(w, r, siteID, endpointID, targetID)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPatch)
	}
}
