package monitorapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/LuTianTian001/JieShan/internal/vnext/monitoring"
	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const maxRequestBytes = 64 << 10

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path == APIPrefix {
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, http.MethodGet)
			return
		}
		handler.listMatrix(writer, request)
		return
	}
	segments, ok := routeSegments(request.URL.Path)
	if !ok || len(segments) < 2 || segments[0] != "models" {
		writeError(writer, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	publishedModelID, ok := positiveID(writer, segments[1], "published model")
	if !ok {
		return
	}
	switch {
	case len(segments) == 2:
		handler.handleModel(writer, request, publishedModelID)
	case len(segments) == 3 && segments[2] == "probe":
		if request.Method != http.MethodPost {
			methodNotAllowed(writer, http.MethodPost)
			return
		}
		handler.probeModel(writer, request, publishedModelID)
	case len(segments) == 4 && segments[2] == "targets" && segments[3] == "probe":
		if request.Method != http.MethodPost {
			methodNotAllowed(writer, http.MethodPost)
			return
		}
		handler.probeTargets(writer, request, publishedModelID)
	case len(segments) == 5 && segments[2] == "targets" && segments[4] == "probe":
		targetID, valid := positiveID(writer, segments[3], "provider model target")
		if !valid {
			return
		}
		if request.Method != http.MethodPost {
			methodNotAllowed(writer, http.MethodPost)
			return
		}
		handler.probeTarget(writer, request, publishedModelID, targetID)
	case len(segments) == 5 && segments[2] == "targets" && segments[4] == "history":
		targetID, valid := positiveID(writer, segments[3], "provider model target")
		if !valid {
			return
		}
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, http.MethodGet)
			return
		}
		handler.targetHistory(writer, request, publishedModelID, targetID)
	default:
		writeError(writer, http.StatusNotFound, "not_found", "resource not found")
	}
}

func (handler *Handler) handleModel(writer http.ResponseWriter, request *http.Request, publishedModelID int64) {
	switch request.Method {
	case http.MethodGet:
		handler.getModel(writer, request, publishedModelID)
	case http.MethodPost:
		handler.createSetting(writer, request, publishedModelID)
	case http.MethodPatch:
		handler.updateSetting(writer, request, publishedModelID)
	default:
		methodNotAllowed(writer, http.MethodGet, http.MethodPost, http.MethodPatch)
	}
}

func (handler *Handler) listMatrix(writer http.ResponseWriter, request *http.Request) {
	views, err := handler.repository.ListMonitorRouteViews(request.Context())
	if err != nil {
		writeRepositoryError(writer, err, "")
		return
	}
	items := make([]monitorRouteResponse, 0, len(views))
	for _, view := range views {
		item, err := handler.newMonitorRouteResponse(request, view)
		if err != nil {
			writeRepositoryError(writer, err, "")
			return
		}
		items = append(items, item)
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (handler *Handler) getModel(writer http.ResponseWriter, request *http.Request, publishedModelID int64) {
	view, err := handler.selectedModel(request, publishedModelID)
	if err != nil {
		writeRepositoryError(writer, err, "selected")
		return
	}
	item, err := handler.newMonitorRouteResponse(request, view)
	if err != nil {
		writeRepositoryError(writer, err, "")
		return
	}
	writer.Header().Set("ETag", revisionETag(view.Setting.Revision))
	writeJSON(writer, http.StatusOK, map[string]any{"item": item})
}

type monitorSettingRequest struct {
	Enabled      *bool `json:"enabled"`
	HistoryLimit *int  `json:"historyLimit"`
}

func (body monitorSettingRequest) createInput() (vnextstore.ModelMonitorSettingWrite, error) {
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	input := vnextstore.ModelMonitorSettingWrite{Enabled: enabled}
	if body.HistoryLimit != nil {
		if *body.HistoryLimit < 1 || *body.HistoryLimit > 10000 {
			return vnextstore.ModelMonitorSettingWrite{}, errors.New("historyLimit must be between 1 and 10000")
		}
		input.HistoryLimit = *body.HistoryLimit
	}
	return input, nil
}

func (body monitorSettingRequest) updateInput(current vnextstore.ModelMonitorSetting) (vnextstore.ModelMonitorSettingWrite, error) {
	if body.Enabled == nil && body.HistoryLimit == nil {
		return vnextstore.ModelMonitorSettingWrite{}, errors.New("at least one monitor setting field is required")
	}
	input := vnextstore.ModelMonitorSettingWrite{
		Enabled: current.Enabled, HistoryLimit: current.HistoryLimit,
	}
	if body.Enabled != nil {
		input.Enabled = *body.Enabled
	}
	if body.HistoryLimit != nil {
		if *body.HistoryLimit < 1 || *body.HistoryLimit > 10000 {
			return vnextstore.ModelMonitorSettingWrite{}, errors.New("historyLimit must be between 1 and 10000")
		}
		input.HistoryLimit = *body.HistoryLimit
	}
	return input, nil
}

func (handler *Handler) createSetting(writer http.ResponseWriter, request *http.Request, publishedModelID int64) {
	var body monitorSettingRequest
	if !decodeJSON(writer, request, &body) {
		return
	}
	input, err := body.createInput()
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	setting, err := handler.repository.CreateModelMonitorSetting(request.Context(), publishedModelID, input, handler.now().UTC())
	if err != nil {
		writeRepositoryError(writer, err, "create")
		return
	}
	writer.Header().Set("ETag", revisionETag(setting.Revision))
	writeJSON(writer, http.StatusCreated, map[string]any{"item": newMonitorSettingResponse(setting, handler.now().UTC())})
}

func (handler *Handler) updateSetting(writer http.ResponseWriter, request *http.Request, publishedModelID int64) {
	revision, ok := requiredIfMatch(writer, request)
	if !ok {
		return
	}
	current, err := handler.repository.GetModelMonitorSetting(request.Context(), publishedModelID)
	if err != nil {
		writeRepositoryError(writer, err, "selected")
		return
	}
	if current.Revision != revision {
		writeRepositoryError(writer, vnextstore.ErrRevisionConflict, "")
		return
	}
	var body monitorSettingRequest
	if !decodeJSON(writer, request, &body) {
		return
	}
	input, err := body.updateInput(current)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	setting, err := handler.repository.UpdateModelMonitorSettingCAS(
		request.Context(), publishedModelID, revision, input, handler.now().UTC(),
	)
	if err != nil {
		writeRepositoryError(writer, err, "selected")
		return
	}
	writer.Header().Set("ETag", revisionETag(setting.Revision))
	writeJSON(writer, http.StatusOK, map[string]any{"item": newMonitorSettingResponse(setting, handler.now().UTC())})
}

func (handler *Handler) probeModel(writer http.ResponseWriter, request *http.Request, publishedModelID int64) {
	view, err := handler.selectedModel(request, publishedModelID)
	if err != nil {
		writeRepositoryError(writer, err, "selected")
		return
	}
	if !view.Setting.Enabled || !view.PublishedModelEnabled || !hasEnabledTarget(view.Targets) {
		writeError(writer, http.StatusConflict, "monitor_disabled", "the selected model monitor has no enabled probe target")
		return
	}
	if handler.prober == nil {
		writeError(writer, http.StatusServiceUnavailable, "probe_unavailable", "manual probe execution is not installed")
		return
	}
	run, err := handler.prober.ProbeModel(request.Context(), publishedModelID)
	if err != nil {
		handler.writeProbeError(writer, err)
		return
	}
	handler.writeProbeRun(writer, run, view)
}

func (handler *Handler) probeTarget(
	writer http.ResponseWriter,
	request *http.Request,
	publishedModelID, targetID int64,
) {
	view, err := handler.selectedModel(request, publishedModelID)
	if err != nil {
		writeRepositoryError(writer, err, "selected")
		return
	}
	target, exists := findTarget(view.Targets, targetID)
	if !exists {
		writeError(writer, http.StatusNotFound, "monitor_target_not_found", "target is not part of this published model")
		return
	}
	if !view.Setting.Enabled || !view.PublishedModelEnabled || !targetConfiguredEnabled(target) {
		writeError(writer, http.StatusConflict, "monitor_disabled", "the selected upstream target is not enabled for probing")
		return
	}
	if handler.target == nil {
		writeError(writer, http.StatusServiceUnavailable, "probe_unavailable", "target probe execution is not installed")
		return
	}
	run, err := handler.target.ProbeTarget(request.Context(), publishedModelID, targetID)
	if err != nil {
		handler.writeProbeError(writer, err)
		return
	}
	handler.writeProbeRun(writer, run, view)
}

type targetsProbeRequest struct {
	ProviderModelTargetIDs []int64 `json:"providerModelTargetIds"`
}

func (body targetsProbeRequest) targetIDs() ([]int64, error) {
	if len(body.ProviderModelTargetIDs) == 0 {
		return nil, errors.New("providerModelTargetIds must contain at least one target ID")
	}
	targetIDs := make([]int64, 0, len(body.ProviderModelTargetIDs))
	seen := make(map[int64]struct{}, len(body.ProviderModelTargetIDs))
	for _, targetID := range body.ProviderModelTargetIDs {
		if targetID <= 0 {
			return nil, errors.New("providerModelTargetIds must contain only positive integers")
		}
		if _, exists := seen[targetID]; exists {
			continue
		}
		seen[targetID] = struct{}{}
		targetIDs = append(targetIDs, targetID)
	}
	return targetIDs, nil
}

func (handler *Handler) probeTargets(writer http.ResponseWriter, request *http.Request, publishedModelID int64) {
	view, err := handler.selectedModel(request, publishedModelID)
	if err != nil {
		writeRepositoryError(writer, err, "selected")
		return
	}
	if !view.Setting.Enabled || !view.PublishedModelEnabled {
		writeError(writer, http.StatusConflict, "monitor_disabled", "the selected model monitor is disabled")
		return
	}
	var body targetsProbeRequest
	if !decodeJSON(writer, request, &body) {
		return
	}
	targetIDs, err := body.targetIDs()
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	for _, targetID := range targetIDs {
		target, exists := findTarget(view.Targets, targetID)
		if !exists {
			writeError(writer, http.StatusNotFound, "monitor_target_not_found", "target is not part of this published model")
			return
		}
		if !targetConfiguredEnabled(target) {
			writeError(writer, http.StatusConflict, "monitor_disabled", "one or more selected upstream targets are not enabled for probing")
			return
		}
	}
	if handler.targets == nil {
		writeError(writer, http.StatusServiceUnavailable, "probe_unavailable", "multi-target probe execution is not installed")
		return
	}
	run, err := handler.targets.ProbeTargets(request.Context(), publishedModelID, targetIDs)
	if err != nil {
		handler.writeProbeError(writer, err)
		return
	}
	handler.writeProbeRun(writer, run, view)
}

func (handler *Handler) writeProbeError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, monitoring.ErrProbeInProgress), errors.Is(err, vnextstore.ErrModelMonitorBusy):
		writeError(writer, http.StatusConflict, "monitor_busy", "a probe for this model is already in progress")
	case errors.Is(err, monitoring.ErrProbeTargetMissing):
		writeError(writer, http.StatusConflict, "monitor_target_unavailable", "the selected upstream target is no longer available")
	case errors.Is(err, vnextstore.ErrModelMonitorNotFound), errors.Is(err, vnextstore.ErrModelMonitorLeaseLost):
		writeError(writer, http.StatusConflict, "monitor_disabled", "the selected model monitor is disabled")
	default:
		writeError(writer, http.StatusInternalServerError, "probe_failed", "the model probe could not be completed")
	}
}

func (handler *Handler) writeProbeRun(
	writer http.ResponseWriter,
	run monitoring.ModelRun,
	view vnextstore.MonitorRouteView,
) {
	response := newProbeRunResponse(run, view)
	if response.FailureCount > 0 && response.SuccessCount == 0 {
		writeJSON(writer, http.StatusBadGateway, map[string]any{
			"error": map[string]string{
				"code": "upstream_probe_failed", "message": "all attempted upstream probes failed",
			},
			"run": response,
		})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"run": response})
}

func (handler *Handler) targetHistory(
	writer http.ResponseWriter,
	request *http.Request,
	publishedModelID, targetID int64,
) {
	view, err := handler.selectedModel(request, publishedModelID)
	if err != nil {
		writeRepositoryError(writer, err, "selected")
		return
	}
	target, ok := findTarget(view.Targets, targetID)
	if !ok {
		writeError(writer, http.StatusNotFound, "monitor_target_not_found", "target is not part of this published model")
		return
	}
	limit, err := historyLimit(request, view.Setting.HistoryLimit)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	results, err := handler.repository.ListModelProbeTargetResults(
		request.Context(), publishedModelID, targetID, maxInt(limit, monitorProbeEvidenceLimit),
	)
	if err != nil {
		writeRepositoryError(writer, err, "")
		return
	}
	currentEvidenceResults := resultsAtRevision(results, target.ProviderModelTargetRevision)
	currentResults := firstResults(currentEvidenceResults, limit)
	now := handler.now().UTC()
	traffic, err := handler.repository.ListMonitorTrafficObservations(
		request.Context(), target.ProviderModelTargetID, target.ProviderModelTargetRevision,
		now.Add(-monitorTransitionEvidenceWindow), now, monitorTrafficObservationLimit,
	)
	if err != nil {
		writeRepositoryError(writer, err, "")
		return
	}
	runtimeSettings, err := handler.repository.GetRuntimeSettings(request.Context())
	if err != nil {
		writeRepositoryError(writer, err, "")
		return
	}
	policy := routing.DefaultHealthPolicy()
	policy.FailureThreshold = runtimeSettings.FailureThreshold
	policy.FailureWindow = runtimeSettings.FailureWindow
	policy.Cooldown = runtimeSettings.Cooldown
	response, err := newTargetHistoryResponse(
		view, target, currentResults, currentEvidenceResults, traffic, policy, now,
	)
	if err != nil {
		writeRepositoryError(writer, err, "")
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) selectedModel(request *http.Request, publishedModelID int64) (vnextstore.MonitorRouteView, error) {
	views, err := handler.repository.ListMonitorRouteViews(request.Context())
	if err != nil {
		return vnextstore.MonitorRouteView{}, err
	}
	for _, view := range views {
		if view.Setting.PublishedModelID == publishedModelID {
			return view, nil
		}
	}
	return vnextstore.MonitorRouteView{}, vnextstore.ErrModelMonitorNotFound
}

func routeSegments(path string) ([]string, bool) {
	if !strings.HasPrefix(path, APIPrefix+"/") {
		return nil, false
	}
	rest := strings.TrimPrefix(path, APIPrefix+"/")
	if rest == "" || strings.HasSuffix(rest, "/") || strings.Contains(rest, "//") {
		return nil, false
	}
	return strings.Split(rest, "/"), true
}

func positiveID(writer http.ResponseWriter, raw, kind string) (int64, bool) {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		writeError(writer, http.StatusBadRequest, "invalid_request", fmt.Sprintf("%s ID must be a positive integer", kind))
		return 0, false
	}
	return value, true
}

func historyLimit(request *http.Request, configured int) (int, error) {
	limit := configured
	if limit <= 0 {
		limit = vnextstore.DefaultModelMonitorHistoryLimit
	}
	if limit > 1000 {
		limit = 1000
	}
	if raw := strings.TrimSpace(request.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 1000 {
			return 0, errors.New("limit must be between 1 and 1000")
		}
		limit = parsed
	}
	return limit, nil
}

func requiredIfMatch(writer http.ResponseWriter, request *http.Request) (int64, bool) {
	raw := strings.TrimSpace(request.Header.Get("If-Match"))
	if raw == "" {
		writeError(writer, http.StatusPreconditionRequired, "precondition_required", "If-Match header is required")
		return 0, false
	}
	if strings.Contains(raw, ",") || raw == "*" || strings.HasPrefix(raw, "W/") {
		writeError(writer, http.StatusBadRequest, "invalid_revision", "If-Match must contain one strong numeric revision")
		return 0, false
	}
	if strings.HasPrefix(raw, `"`) || strings.HasSuffix(raw, `"`) {
		if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
			writeError(writer, http.StatusBadRequest, "invalid_revision", "If-Match must contain one strong numeric revision")
			return 0, false
		}
		raw = raw[1 : len(raw)-1]
	}
	revision, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || revision <= 0 {
		writeError(writer, http.StatusBadRequest, "invalid_revision", "If-Match must contain one strong numeric revision")
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

func decodeJSON(writer http.ResponseWriter, request *http.Request, destination any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, maxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", "request body must be a valid JSON object")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(writer, http.StatusBadRequest, "invalid_request", "request body must contain exactly one JSON value")
		return false
	}
	return true
}

func methodNotAllowed(writer http.ResponseWriter, methods ...string) {
	writer.Header().Set("Allow", strings.Join(methods, ", "))
	writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func writeRepositoryError(writer http.ResponseWriter, err error, monitorContext string) {
	switch {
	case errors.Is(err, vnextstore.ErrRevisionConflict):
		writeError(writer, http.StatusConflict, "revision_conflict", "monitor settings changed; refresh and try again")
	case errors.Is(err, vnextstore.ErrConflict):
		writeError(writer, http.StatusConflict, "monitor_already_selected", "this published model is already selected for monitoring")
	case errors.Is(err, vnextstore.ErrModelMonitorNotFound):
		if monitorContext == "create" {
			writeError(writer, http.StatusNotFound, "model_not_found", "published model was not found")
		} else {
			writeError(writer, http.StatusNotFound, "monitor_not_selected", "published model is not selected for monitoring")
		}
	case errors.Is(err, sql.ErrNoRows):
		writeError(writer, http.StatusNotFound, "not_found", "resource not found")
	default:
		writeError(writer, http.StatusInternalServerError, "internal_error", "the request could not be completed")
	}
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
