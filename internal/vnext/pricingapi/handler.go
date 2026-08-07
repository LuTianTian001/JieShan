package pricingapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/LuTianTian001/JieShan/internal/vnext/pricing"
)

const (
	apiPrefix       = "/api/vnext/pricing"
	maxRequestBytes = 8 << 20
)

type Handler struct {
	service *pricing.Service
}

func New(service *pricing.Service) (*Handler, error) {
	if service == nil {
		return nil, errors.New("price catalog service is required")
	}
	return &Handler{service: service}, nil
}

func (handler *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if handler == nil || handler.service == nil {
		writeError(w, http.StatusServiceUnavailable, "pricing_unavailable", "Price catalog service is unavailable.")
		return
	}
	segments, ok := pathSegments(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "Pricing resource was not found.")
		return
	}
	switch {
	case len(segments) == 1 && segments[0] == "catalogs":
		handler.handleCatalogs(w, r)
	case len(segments) == 2 && segments[0] == "catalogs" && segments[1] == "preview":
		handler.preview(w, r)
	case len(segments) == 2 && segments[0] == "catalogs":
		handler.getCatalog(w, r, segments[1])
	case len(segments) == 3 && segments[0] == "catalogs" && segments[2] == "activate":
		handler.activate(w, r, segments[1])
	case len(segments) == 1 && segments[0] == "state":
		handler.state(w, r)
	default:
		writeError(w, http.StatusNotFound, "not_found", "Pricing resource was not found.")
	}
}

func (handler *Handler) handleCatalogs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, state, err := handler.service.List(r.Context())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeStateETag(w, state)
		writeJSON(w, http.StatusOK, listResponse{Items: items, State: state})
	case http.MethodPost:
		var request importRequest
		if err := decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		result, err := handler.service.Import(r.Context(), request.Catalog, request.ExpectedDigest)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		status := http.StatusOK
		if result.Imported {
			status = http.StatusCreated
		}
		writeStateETag(w, result.State)
		writeJSON(w, status, result)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (handler *Handler) preview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var request previewRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	result, err := handler.service.Preview(r.Context(), request.Catalog)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeStateETag(w, result.State)
	writeJSON(w, http.StatusOK, result)
}

func (handler *Handler) getCatalog(w http.ResponseWriter, r *http.Request, version string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	catalog, err := handler.service.Get(r.Context(), version)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, catalogResponse{Catalog: catalog})
}

func (handler *Handler) activate(w http.ResponseWriter, r *http.Request, version string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	revision, ok := requireRevision(w, r)
	if !ok {
		return
	}
	state, err := handler.service.Activate(r.Context(), version, revision)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeStateETag(w, state)
	writeJSON(w, http.StatusOK, stateResponse{State: state})
}

func (handler *Handler) state(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	state, err := handler.service.State(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeStateETag(w, state)
	writeJSON(w, http.StatusOK, stateResponse{State: state})
}

type previewRequest struct {
	Catalog pricing.Catalog `json:"catalog"`
}

type importRequest struct {
	Catalog        pricing.Catalog `json:"catalog"`
	ExpectedDigest string          `json:"expected_digest"`
}

type listResponse struct {
	Items []pricing.CatalogSummary `json:"items"`
	State pricing.CatalogState     `json:"state"`
}

type catalogResponse struct {
	Catalog pricing.Catalog `json:"catalog"`
}

type stateResponse struct {
	State pricing.CatalogState `json:"state"`
}

type errorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func pathSegments(path string) ([]string, bool) {
	if path == apiPrefix || path == apiPrefix+"/" {
		return nil, true
	}
	if !strings.HasPrefix(path, apiPrefix+"/") {
		return nil, false
	}
	raw := strings.TrimPrefix(path, apiPrefix+"/")
	parts := strings.Split(raw, "/")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			return nil, false
		}
		decoded, err := url.PathUnescape(part)
		if err != nil || decoded == "" || strings.Contains(decoded, "/") {
			return nil, false
		}
		result = append(result, decoded)
	}
	return result, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	if contentType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])); contentType != "application/json" {
		return errors.New("Content-Type must be application/json")
	}
	reader := http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON body must contain one object")
		}
		return fmt.Errorf("invalid trailing JSON: %w", err)
	}
	return nil
}

func requireRevision(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" {
		writeError(w, http.StatusPreconditionRequired, "precondition_required", "If-Match is required for catalog activation.")
		return 0, false
	}
	if strings.HasPrefix(raw, "W/") || len(raw) < 3 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		writeError(w, http.StatusBadRequest, "invalid_revision", "If-Match must contain one strong numeric revision.")
		return 0, false
	}
	value, err := strconv.ParseInt(raw[1:len(raw)-1], 10, 64)
	if err != nil || value < 0 {
		writeError(w, http.StatusBadRequest, "invalid_revision", "If-Match must contain one strong numeric revision.")
		return 0, false
	}
	return value, true
}

func writeStateETag(w http.ResponseWriter, state pricing.CatalogState) {
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(state.Revision, 10)))
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pricing.ErrInvalidCatalog):
		writeError(w, http.StatusBadRequest, "invalid_catalog", err.Error())
	case errors.Is(err, pricing.ErrCatalogNotFound):
		writeError(w, http.StatusNotFound, "catalog_not_found", err.Error())
	case errors.Is(err, pricing.ErrCatalogVersionConflict):
		writeError(w, http.StatusConflict, "immutable_version_conflict", err.Error())
	case errors.Is(err, pricing.ErrDigestConfirmation):
		writeError(w, http.StatusConflict, "digest_confirmation_failed", err.Error())
	case errors.Is(err, pricing.ErrCatalogNotEffective):
		writeError(w, http.StatusConflict, "catalog_not_effective", err.Error())
	case errors.Is(err, pricing.ErrCatalogStateConflict):
		writeError(w, http.StatusPreconditionFailed, "revision_conflict", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "Price catalog operation failed.")
	}
}

func methodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method is not allowed for this pricing resource.")
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	response := errorResponse{}
	response.Error.Code = code
	response.Error.Message = message
	writeJSON(w, status, response)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
