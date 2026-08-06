package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/LuTianTian001/JieShan/internal/store"
)

type publishedModelProbePayload struct {
	TargetID *int64 `json:"targetId"`
}

func (s *Server) registerV2ProbeRoutes() {
	s.mux.Handle("GET /api/v2/monitor/matrix", s.admin(http.HandlerFunc(s.monitorMatrixV2)))
	s.mux.Handle("POST /api/v2/published-models/{id}/probe", s.admin(http.HandlerFunc(s.probePublishedModelV2)))
	s.mux.Handle("GET /api/v2/published-models/{id}/probe-runs", s.admin(http.HandlerFunc(s.listPublishedModelProbeRunsV2)))
	s.mux.Handle("GET /api/v2/probe-runs/{runId}", s.admin(http.HandlerFunc(s.getProbeRunV2)))
	s.mux.Handle("GET /api/v2/probe-runs/{runId}/attempts", s.admin(http.HandlerFunc(s.listProbeAttemptsV2)))
}

func (s *Server) monitorMatrixV2(w http.ResponseWriter, r *http.Request) {
	models, err := s.store.ListPublishedModelMonitorMatrix(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"generatedAt": store.NowMS(), "models": models})
}

func (s *Server) probePublishedModelV2(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body publishedModelProbePayload
	if !decodeOptionalJSON(w, r, &body) {
		return
	}
	if body.TargetID != nil && *body.TargetID <= 0 {
		writeError(w, http.StatusBadRequest, "targetId must be positive", "invalid_request")
		return
	}
	run, err := s.gateway.ProbePublishedModel(r.Context(), id, body.TargetID, "manual")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	attempts, err := s.store.ListProbeAttempts(r.Context(), run.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run, "attempts": attempts})
}

func (s *Server) listPublishedModelProbeRunsV2(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := s.store.GetPublishedModel(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	limit, ok := probeRunLimit(w, r)
	if !ok {
		return
	}
	items, err := s.store.ListProbeRuns(r.Context(), id, limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getProbeRunV2(w http.ResponseWriter, r *http.Request) {
	runID := strings.TrimSpace(r.PathValue("runId"))
	if runID == "" {
		writeError(w, http.StatusBadRequest, "invalid probe run id", "invalid_request")
		return
	}
	item, err := s.store.GetProbeRun(r.Context(), runID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) listProbeAttemptsV2(w http.ResponseWriter, r *http.Request) {
	runID := strings.TrimSpace(r.PathValue("runId"))
	if runID == "" {
		writeError(w, http.StatusBadRequest, "invalid probe run id", "invalid_request")
		return
	}
	if _, err := s.store.GetProbeRun(r.Context(), runID); err != nil {
		writeStoreError(w, err)
		return
	}
	items, err := s.store.ListProbeAttempts(r.Context(), runID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return true
		}
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error(), "invalid_request")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "request body must contain exactly one JSON value", "invalid_request")
		return false
	}
	return true
}

func probeRunLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return 50, true
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > 200 {
		writeError(w, http.StatusBadRequest, "limit must be between 1 and 200", "invalid_request")
		return 0, false
	}
	return limit, true
}
