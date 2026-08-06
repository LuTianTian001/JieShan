package httpapi

import (
	"net/http"
	"strconv"
)

type routingProfilePayload struct {
	Name     string `json:"name"`
	Revision int64  `json:"revision"`
}

type routingProfileModelPayload struct {
	TargetIDs []int64 `json:"targetIds"`
	Revision  int64   `json:"revision"`
}

func (s *Server) registerV2RoutingProfileRoutes() {
	s.mux.Handle("GET /api/v2/routing-profiles", s.admin(http.HandlerFunc(s.listRoutingProfilesV2)))
	s.mux.Handle("POST /api/v2/routing-profiles", s.admin(http.HandlerFunc(s.createRoutingProfileV2)))
	s.mux.Handle("GET /api/v2/routing-profiles/{id}", s.admin(http.HandlerFunc(s.getRoutingProfileV2)))
	s.mux.Handle("PATCH /api/v2/routing-profiles/{id}", s.admin(http.HandlerFunc(s.updateRoutingProfileV2)))
	s.mux.Handle("DELETE /api/v2/routing-profiles/{id}", s.admin(http.HandlerFunc(s.deleteRoutingProfileV2)))
	s.mux.Handle("GET /api/v2/routing-profiles/{id}/models/{modelId}", s.admin(http.HandlerFunc(s.getRoutingProfileModelV2)))
	s.mux.Handle("PUT /api/v2/routing-profiles/{id}/models/{modelId}", s.admin(http.HandlerFunc(s.putRoutingProfileModelV2)))
	s.mux.Handle("DELETE /api/v2/routing-profiles/{id}/models/{modelId}", s.admin(http.HandlerFunc(s.deleteRoutingProfileModelV2)))
}

func (s *Server) listRoutingProfilesV2(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListRoutingProfiles(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createRoutingProfileV2(w http.ResponseWriter, r *http.Request) {
	var body routingProfilePayload
	if !decodeJSON(w, r, &body) {
		return
	}
	id, err := s.store.CreateRoutingProfile(r.Context(), body.Name)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	item, err := s.store.GetRoutingProfile(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"item": item})
}

func (s *Server) getRoutingProfileV2(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	item, err := s.store.GetRoutingProfile(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) updateRoutingProfileV2(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	current, err := s.store.GetRoutingProfile(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var body routingProfilePayload
	if !decodeJSON(w, r, &body) {
		return
	}
	revision, ok := requestRevision(w, r, body.Revision)
	if !ok {
		return
	}
	if err := s.store.UpdateRoutingProfile(r.Context(), id, revision, firstNonEmpty(body.Name, current.Name)); err != nil {
		writeStoreError(w, err)
		return
	}
	item, err := s.store.GetRoutingProfile(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) deleteRoutingProfileV2(w http.ResponseWriter, r *http.Request) {
	s.deleteVersionedV2(w, r, s.store.DeleteRoutingProfile)
}

func (s *Server) getRoutingProfileModelV2(w http.ResponseWriter, r *http.Request) {
	profileID, modelID, ok := routingProfileModelIDs(w, r)
	if !ok {
		return
	}
	item, err := s.store.GetRoutingProfileModelRoute(r.Context(), profileID, modelID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) putRoutingProfileModelV2(w http.ResponseWriter, r *http.Request) {
	profileID, modelID, ok := routingProfileModelIDs(w, r)
	if !ok {
		return
	}
	var body routingProfileModelPayload
	if !decodeJSON(w, r, &body) {
		return
	}
	revision, ok := requestRevision(w, r, body.Revision)
	if !ok {
		return
	}
	if err := s.store.SetRoutingProfileModelTargets(r.Context(), profileID, modelID, revision, body.TargetIDs); err != nil {
		writeStoreError(w, err)
		return
	}
	item, err := s.store.GetRoutingProfileModelRoute(r.Context(), profileID, modelID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) deleteRoutingProfileModelV2(w http.ResponseWriter, r *http.Request) {
	profileID, modelID, ok := routingProfileModelIDs(w, r)
	if !ok {
		return
	}
	revision, ok := requestRevision(w, r, 0)
	if !ok {
		return
	}
	if err := s.store.ClearRoutingProfileModelTargets(r.Context(), profileID, modelID, revision); err != nil {
		writeStoreError(w, err)
		return
	}
	item, err := s.store.GetRoutingProfileModelRoute(r.Context(), profileID, modelID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func routingProfileModelIDs(w http.ResponseWriter, r *http.Request) (int64, int64, bool) {
	profileID, ok := pathID(w, r)
	if !ok {
		return 0, 0, false
	}
	modelID, err := strconv.ParseInt(r.PathValue("modelId"), 10, 64)
	if err != nil || modelID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid model id", "invalid_request")
		return 0, 0, false
	}
	return profileID, modelID, true
}
