package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/LuTianTian001/JieShan/internal/store"
)

type siteModelPayload struct {
	DisplayName string `json:"displayName"`
	Enabled     *bool  `json:"enabled"`
	Stale       *bool  `json:"stale"`
	Revision    int64  `json:"revision"`
}

type publishedModelPayload struct {
	PublicName                string `json:"publicName"`
	DisplayName               string `json:"displayName"`
	OfficialPriceSKU          string `json:"officialPriceSku"`
	Enabled                   *bool  `json:"enabled"`
	MonitorEnabled            *bool  `json:"monitorEnabled"`
	MonitorIntervalSeconds    *int   `json:"monitorIntervalSeconds"`
	CooldownSeconds           *int   `json:"cooldownSeconds"`
	FailureThreshold          *int   `json:"failureThreshold"`
	FailureWindowSeconds      *int   `json:"failureWindowSeconds"`
	FirstOutputTimeoutSeconds *int   `json:"firstOutputTimeoutSeconds"`
	StreamIdleTimeoutSeconds  *int   `json:"streamIdleTimeoutSeconds"`
	RequestDeadlineSeconds    *int   `json:"requestDeadlineSeconds"`
	MaxAttempts               *int   `json:"maxAttempts"`
	Revision                  int64  `json:"revision"`
}

type routeSiteTargetPayload struct {
	SiteID            int64 `json:"siteId"`
	EndpointID        int64 `json:"endpointId"`
	SiteModelID       int64 `json:"siteModelId"`
	Enabled           *bool `json:"enabled"`
	Revision          int64 `json:"revision"`
	PublishedRevision int64 `json:"publishedRevision"`
}

func (s *Server) registerV2ModelRoutes() {
	s.mux.Handle("GET /api/v2/sites/{id}/models", s.admin(http.HandlerFunc(s.listSiteModelsV2)))
	s.mux.Handle("GET /api/v2/sites/{id}/model-discoveries", s.admin(http.HandlerFunc(s.listSiteModelDiscoveriesV2)))
	s.mux.Handle("POST /api/v2/sites/{id}/model-discoveries", s.admin(http.HandlerFunc(s.discoverSiteModelsV2)))
	s.mux.Handle("GET /api/v2/model-discoveries/{id}", s.admin(http.HandlerFunc(s.getModelDiscoveryRunV2)))
	s.mux.Handle("GET /api/v2/model-discoveries/{id}/attempts", s.admin(http.HandlerFunc(s.listModelDiscoveryAttemptsV2)))
	s.mux.Handle("PATCH /api/v2/site-models/{id}", s.admin(http.HandlerFunc(s.updateSiteModelV2)))
	s.mux.Handle("DELETE /api/v2/site-models/{id}", s.admin(http.HandlerFunc(s.deleteSiteModelV2)))

	s.mux.Handle("GET /api/v2/published-models", s.admin(http.HandlerFunc(s.listPublishedModelsV2)))
	s.mux.Handle("POST /api/v2/published-models", s.admin(http.HandlerFunc(s.createPublishedModelV2)))
	s.mux.Handle("GET /api/v2/published-models/{id}", s.admin(http.HandlerFunc(s.getPublishedModelV2)))
	s.mux.Handle("PATCH /api/v2/published-models/{id}", s.admin(http.HandlerFunc(s.updatePublishedModelV2)))
	s.mux.Handle("DELETE /api/v2/published-models/{id}", s.admin(http.HandlerFunc(s.deletePublishedModelV2)))
	s.mux.Handle("POST /api/v2/published-models/{id}/targets", s.admin(http.HandlerFunc(s.createRouteSiteTargetV2)))
	s.mux.Handle("PUT /api/v2/published-models/{id}/targets/order", s.admin(http.HandlerFunc(s.reorderRouteSiteTargetsV2)))
	s.mux.Handle("PATCH /api/v2/route-targets/{id}", s.admin(http.HandlerFunc(s.updateRouteSiteTargetV2)))
	s.mux.Handle("DELETE /api/v2/route-targets/{id}", s.admin(http.HandlerFunc(s.deleteRouteSiteTargetV2)))
}

func (s *Server) listSiteModelsV2(w http.ResponseWriter, r *http.Request) {
	siteID, ok := pathID(w, r)
	if !ok {
		return
	}
	items, err := s.store.ListSiteModelsWithCoverage(r.Context(), siteID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) updateSiteModelV2(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	current, err := s.store.GetSiteModel(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var body siteModelPayload
	if !decodeJSON(w, r, &body) {
		return
	}
	revision, ok := requestRevision(w, r, body.Revision)
	if !ok {
		return
	}
	input := store.SiteModelWrite{
		SiteID: current.SiteID, EndpointID: current.EndpointID, ModelName: current.ModelName,
		DisplayName: firstNonEmpty(body.DisplayName, current.DisplayName),
		Enabled:     boolDefault(body.Enabled, current.Enabled), Stale: boolDefault(body.Stale, current.Stale),
		MissingCount: current.MissingCount, LastSeenAt: current.LastSeenAt,
	}
	if err := s.store.UpdateSiteModel(r.Context(), id, revision, input); err != nil {
		writeStoreError(w, err)
		return
	}
	item, _ := s.store.GetSiteModel(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) deleteSiteModelV2(w http.ResponseWriter, r *http.Request) {
	s.deleteVersionedV2(w, r, s.store.DeleteSiteModel)
}

func (s *Server) listPublishedModelsV2(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListPublishedModelRoutes(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createPublishedModelV2(w http.ResponseWriter, r *http.Request) {
	var body publishedModelPayload
	if !decodeJSON(w, r, &body) {
		return
	}
	id, err := s.store.CreatePublishedModel(r.Context(), store.PublishedModelWrite{
		PublicName: body.PublicName, DisplayName: body.DisplayName, OfficialPriceSKU: body.OfficialPriceSKU,
		Enabled: boolDefault(body.Enabled, true), MonitorEnabled: boolDefault(body.MonitorEnabled, false),
		MonitorIntervalSeconds: intDefault(body.MonitorIntervalSeconds, 300),
		CooldownSeconds:        intDefault(body.CooldownSeconds, 300), FailureThreshold: intDefault(body.FailureThreshold, 2),
		FailureWindowSeconds:      intDefault(body.FailureWindowSeconds, 300),
		FirstOutputTimeoutSeconds: intDefault(body.FirstOutputTimeoutSeconds, 30),
		StreamIdleTimeoutSeconds:  intDefault(body.StreamIdleTimeoutSeconds, 60),
		RequestDeadlineSeconds:    intDefault(body.RequestDeadlineSeconds, 120), MaxAttempts: intDefault(body.MaxAttempts, 3),
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	item, _ := s.loadPublishedModelRoute(r, id)
	writeJSON(w, http.StatusCreated, map[string]any{"item": item})
}

func (s *Server) getPublishedModelV2(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	item, err := s.loadPublishedModelRoute(r, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) loadPublishedModelRoute(r *http.Request, id int64) (store.PublishedModelRoute, error) {
	item, err := s.store.GetPublishedModel(r.Context(), id)
	if err != nil {
		return store.PublishedModelRoute{}, err
	}
	targets, err := s.store.ListRouteSiteTargets(r.Context(), id)
	if err != nil {
		return store.PublishedModelRoute{}, err
	}
	return store.PublishedModelRoute{PublishedModel: item, Targets: targets}, nil
}

func (s *Server) updatePublishedModelV2(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	current, err := s.store.GetPublishedModel(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var body publishedModelPayload
	if !decodeJSON(w, r, &body) {
		return
	}
	revision, ok := requestRevision(w, r, body.Revision)
	if !ok {
		return
	}
	if err := s.store.UpdatePublishedModel(r.Context(), id, revision, store.PublishedModelWrite{
		PublicName:       firstNonEmpty(body.PublicName, current.PublicName),
		DisplayName:      firstNonEmpty(body.DisplayName, current.DisplayName),
		OfficialPriceSKU: firstNonEmpty(body.OfficialPriceSKU, current.OfficialPriceSKU),
		Enabled:          boolDefault(body.Enabled, current.Enabled), MonitorEnabled: boolDefault(body.MonitorEnabled, current.MonitorEnabled),
		MonitorIntervalSeconds:    intDefault(body.MonitorIntervalSeconds, current.MonitorIntervalSeconds),
		CooldownSeconds:           intDefault(body.CooldownSeconds, current.CooldownSeconds),
		FailureThreshold:          intDefault(body.FailureThreshold, current.FailureThreshold),
		FailureWindowSeconds:      intDefault(body.FailureWindowSeconds, current.FailureWindowSeconds),
		FirstOutputTimeoutSeconds: intDefault(body.FirstOutputTimeoutSeconds, current.FirstOutputTimeoutSeconds),
		StreamIdleTimeoutSeconds:  intDefault(body.StreamIdleTimeoutSeconds, current.StreamIdleTimeoutSeconds),
		RequestDeadlineSeconds:    intDefault(body.RequestDeadlineSeconds, current.RequestDeadlineSeconds),
		MaxAttempts:               intDefault(body.MaxAttempts, current.MaxAttempts),
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	item, _ := s.loadPublishedModelRoute(r, id)
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) deletePublishedModelV2(w http.ResponseWriter, r *http.Request) {
	s.deleteVersionedV2(w, r, s.store.DeletePublishedModel)
}

func (s *Server) createRouteSiteTargetV2(w http.ResponseWriter, r *http.Request) {
	publishedID, ok := pathID(w, r)
	if !ok {
		return
	}
	var body routeSiteTargetPayload
	if !decodeJSON(w, r, &body) {
		return
	}
	id, err := s.store.CreateRouteSiteTarget(r.Context(), publishedID, body.PublishedRevision, store.RouteSiteTargetWrite{
		SiteID: body.SiteID, EndpointID: body.EndpointID, SiteModelID: body.SiteModelID,
		Enabled: boolDefault(body.Enabled, true),
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	item, _ := s.store.GetRouteSiteTarget(r.Context(), id)
	writeJSON(w, http.StatusCreated, map[string]any{"item": item})
}

func (s *Server) updateRouteSiteTargetV2(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	current, err := s.store.GetRouteSiteTarget(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var body routeSiteTargetPayload
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.store.UpdateRouteSiteTarget(r.Context(), id, body.Revision, body.PublishedRevision, store.RouteSiteTargetWrite{
		SiteID: positiveOr(body.SiteID, current.SiteID), EndpointID: positiveOr(body.EndpointID, current.EndpointID),
		SiteModelID: positiveOr(body.SiteModelID, current.SiteModelID), Enabled: boolDefault(body.Enabled, current.Enabled),
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	item, _ := s.store.GetRouteSiteTarget(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) deleteRouteSiteTargetV2(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	targetRevision, err := positiveQueryInt64(r, "targetRevision")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_revision")
		return
	}
	publishedRevision, err := positiveQueryInt64(r, "publishedRevision")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_revision")
		return
	}
	if err := s.store.DeleteRouteSiteTarget(r.Context(), id, targetRevision, publishedRevision); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) reorderRouteSiteTargetsV2(w http.ResponseWriter, r *http.Request) {
	publishedID, ok := pathID(w, r)
	if !ok {
		return
	}
	var body orderPayload
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.store.ReorderRouteSiteTargets(r.Context(), publishedID, body.Revision, body.IDs); err != nil {
		writeStoreError(w, err)
		return
	}
	item, _ := s.loadPublishedModelRoute(r, publishedID)
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func positiveOr(value, fallback int64) int64 {
	if value > 0 {
		return value
	}
	return fallback
}

func positiveQueryInt64(r *http.Request, name string) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 1 {
		return 0, &strconv.NumError{Func: "ParseInt", Num: raw, Err: strconv.ErrSyntax}
	}
	return value, nil
}
