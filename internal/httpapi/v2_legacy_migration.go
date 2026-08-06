package httpapi

import (
	"errors"
	"net/http"

	"github.com/LuTianTian001/JieShan/internal/store"
)

type legacyMigrationApplyPayload struct {
	PlanFingerprint string `json:"planFingerprint"`
	Force           bool   `json:"force"`
}

func (s *Server) registerV2LegacyMigrationRoutes() {
	s.mux.Handle("GET /api/v2/migrations/legacy/preview", s.admin(http.HandlerFunc(s.previewLegacyMigrationV2)))
	s.mux.Handle("POST /api/v2/migrations/legacy/apply", s.admin(http.HandlerFunc(s.applyLegacyMigrationV2)))
}

func (s *Server) previewLegacyMigrationV2(w http.ResponseWriter, r *http.Request) {
	preview, err := s.store.PreviewLegacyMigration(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": preview})
}

func (s *Server) applyLegacyMigrationV2(w http.ResponseWriter, r *http.Request) {
	var body legacyMigrationApplyPayload
	if !decodeJSON(w, r, &body) {
		return
	}
	result, err := s.store.ApplyLegacyMigration(r.Context(), body.PlanFingerprint, body.Force)
	if err == nil {
		writeJSON(w, http.StatusOK, map[string]any{"item": result})
		return
	}
	if errors.Is(err, store.ErrLegacyMigrationPlanFingerprintRequired) {
		writeError(w, http.StatusBadRequest, err.Error(), "legacy_migration_plan_fingerprint_required")
		return
	}
	var changed *store.LegacyMigrationPlanChangedError
	if errors.As(err, &changed) {
		message := "legacy migration plan changed; review the new preview before applying"
		writeJSON(w, http.StatusConflict, map[string]any{
			"message": message,
			"error":   map[string]any{"message": message, "code": "legacy_migration_plan_changed"},
			"preview": changed.Preview,
		})
		return
	}
	var blocked *store.LegacyMigrationBlockedError
	if errors.As(err, &blocked) {
		message := "legacy migration is blocked; review conflicts before applying"
		writeJSON(w, http.StatusConflict, map[string]any{
			"message": message,
			"error":   map[string]any{"message": message, "code": "legacy_migration_blocked"},
			"preview": blocked.Preview,
		})
		return
	}
	writeStoreError(w, err)
}
