package monitorapi

import (
	"net/http"
	"sort"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/monitoring"
	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const matrixStatusBarLimit = 24

type monitorSettingResponse struct {
	Enabled             bool   `json:"enabled"`
	IntervalMS          int64  `json:"intervalMs"`
	HistoryLimit        int    `json:"historyLimit"`
	NextProbeAt         int64  `json:"nextProbeAt"`
	LastProbeStartedAt  *int64 `json:"lastProbeStartedAt"`
	LastProbeFinishedAt *int64 `json:"lastProbeFinishedAt"`
	Busy                bool   `json:"busy"`
	Revision            int64  `json:"revision"`
	CreatedAt           int64  `json:"createdAt"`
	UpdatedAt           int64  `json:"updatedAt"`
}

type monitorRouteResponse struct {
	PublishedModelID       int64                   `json:"publishedModelId"`
	PublicModel            string                  `json:"publicModel"`
	OfficialPriceSKU       string                  `json:"officialPriceSku"`
	PublishedModelEnabled  bool                    `json:"publishedModelEnabled"`
	PublishedModelRevision int64                   `json:"publishedModelRevision"`
	Status                 string                  `json:"status"`
	Monitor                monitorSettingResponse  `json:"monitor"`
	Targets                []monitorTargetResponse `json:"targets"`
	Successes              int                     `json:"successes"`
	Failures               int                     `json:"failures"`
	Skipped                int                     `json:"skipped"`
	SuccessBasisPoints     int                     `json:"successBasisPoints"`
}

type monitorTargetResponse struct {
	PublishedModelTargetID       int64                  `json:"publishedModelTargetId"`
	PublishedModelTargetRevision int64                  `json:"publishedModelTargetRevision"`
	ProviderModelTargetID        int64                  `json:"providerModelTargetId"`
	ProviderModelTargetRevision  int64                  `json:"providerModelTargetRevision"`
	Position                     int                    `json:"position"`
	SiteID                       int64                  `json:"siteId"`
	SiteName                     string                 `json:"siteName"`
	EndpointID                   int64                  `json:"endpointId"`
	EndpointName                 string                 `json:"endpointName"`
	SourceModel                  string                 `json:"sourceModel"`
	WireProtocol                 string                 `json:"wireProtocol"`
	APISurface                   string                 `json:"apiSurface"`
	Enabled                      bool                   `json:"enabled"`
	UsableCredentialCount        int                    `json:"usableCredentialCount"`
	Status                       string                 `json:"status"`
	Successes                    int                    `json:"successes"`
	Failures                     int                    `json:"failures"`
	Skipped                      int                    `json:"skipped"`
	SuccessBasisPoints           int                    `json:"successBasisPoints"`
	Latest                       *probePointResponse    `json:"latest"`
	StatusBar                    []probePointResponse   `json:"statusBar"`
	Health                       *routingHealthResponse `json:"health"`
}

type routingHealthResponse struct {
	Phase                  string `json:"phase"`
	Capability             string `json:"capability"`
	ConsecutiveFailures    int    `json:"consecutiveFailures"`
	FailureWindowStartedAt *int64 `json:"failureWindowStartedAt"`
	LastFailureAt          *int64 `json:"lastFailureAt"`
	LastSuccessAt          *int64 `json:"lastSuccessAt"`
	CooldownUntil          *int64 `json:"cooldownUntil"`
	HalfOpenLeaseUntil     *int64 `json:"halfOpenLeaseUntil"`
	LastEventAt            *int64 `json:"lastEventAt"`
	LastFailureKind        string `json:"lastFailureKind"`
	ProviderTargetRevision int64  `json:"providerTargetRevision"`
	StateVersion           int64  `json:"stateVersion"`
	UpdatedAt              int64  `json:"updatedAt"`
}

type probePointResponse struct {
	ID                          int64  `json:"id,omitempty"`
	RunID                       string `json:"runId"`
	ProviderModelTargetRevision int64  `json:"providerModelTargetRevision"`
	Outcome                     string `json:"outcome"`
	PermitMode                  string `json:"permitMode"`
	PermitReason                string `json:"permitReason"`
	HTTPStatus                  *int   `json:"httpStatus"`
	FailureKind                 string `json:"failureKind"`
	ErrorCode                   string `json:"errorCode"`
	TotalLatencyMS              int64  `json:"totalLatencyMs"`
	FirstOutputMS               *int64 `json:"firstOutputMs"`
	StartedAt                   int64  `json:"startedAt"`
	FinishedAt                  int64  `json:"finishedAt"`
	HealthApplied               bool   `json:"healthApplied"`
	HealthApplyReason           string `json:"healthApplyReason"`
	HealthErrorCode             string `json:"healthErrorCode"`
}

type targetHistoryResponse struct {
	PublishedModelID   int64                  `json:"publishedModelId"`
	PublicModel        string                 `json:"publicModel"`
	Target             monitorTargetIdentity  `json:"target"`
	Status             string                 `json:"status"`
	Successes          int                    `json:"successes"`
	Failures           int                    `json:"failures"`
	Skipped            int                    `json:"skipped"`
	Total              int                    `json:"total"`
	Attempted          int                    `json:"attempted"`
	SuccessBasisPoints int                    `json:"successBasisPoints"`
	Health             *routingHealthResponse `json:"health"`
	Order              string                 `json:"order"`
	Items              []probePointResponse   `json:"items"`
}

type monitorTargetIdentity struct {
	PublishedModelTargetID int64  `json:"publishedModelTargetId"`
	ProviderModelTargetID  int64  `json:"providerModelTargetId"`
	Position               int    `json:"position"`
	SiteID                 int64  `json:"siteId"`
	SiteName               string `json:"siteName"`
	EndpointID             int64  `json:"endpointId"`
	EndpointName           string `json:"endpointName"`
	SourceModel            string `json:"sourceModel"`
	WireProtocol           string `json:"wireProtocol"`
	APISurface             string `json:"apiSurface"`
}

type probeRunResponse struct {
	ID               string                      `json:"id"`
	PublishedModelID int64                       `json:"publishedModelId"`
	PublicModel      string                      `json:"publicModel"`
	TriggerKind      string                      `json:"triggerKind"`
	Status           string                      `json:"status"`
	Outcome          string                      `json:"outcome"`
	TargetCount      int                         `json:"targetCount"`
	SuccessCount     int                         `json:"successCount"`
	FailureCount     int                         `json:"failureCount"`
	SkippedCount     int                         `json:"skippedCount"`
	StartedAt        int64                       `json:"startedAt"`
	FinishedAt       *int64                      `json:"finishedAt"`
	Results          []manualProbeResultResponse `json:"results"`
}

type manualProbeResultResponse struct {
	ProviderModelTargetID int64                        `json:"providerModelTargetId"`
	Position              int                          `json:"position"`
	SiteID                int64                        `json:"siteId"`
	SiteName              string                       `json:"siteName"`
	EndpointID            int64                        `json:"endpointId"`
	EndpointName          string                       `json:"endpointName"`
	SourceModel           string                       `json:"sourceModel"`
	Outcome               string                       `json:"outcome"`
	FailureKind           string                       `json:"failureKind"`
	ErrorCode             string                       `json:"errorCode"`
	HTTPStatus            *int                         `json:"httpStatus"`
	PermitMode            string                       `json:"permitMode"`
	PermitReason          string                       `json:"permitReason"`
	TotalLatencyMS        int64                        `json:"totalLatencyMs"`
	FirstOutputMS         *int64                       `json:"firstOutputMs"`
	StartedAt             int64                        `json:"startedAt"`
	FinishedAt            int64                        `json:"finishedAt"`
	HealthApplied         bool                         `json:"healthApplied"`
	HealthApplyReason     string                       `json:"healthApplyReason"`
	HealthErrorCode       string                       `json:"healthErrorCode"`
	Attempts              []manualProbeAttemptResponse `json:"attempts"`
}

type manualProbeAttemptResponse struct {
	CredentialID   int64  `json:"credentialId"`
	Outcome        string `json:"outcome"`
	FailureKind    string `json:"failureKind"`
	ErrorCode      string `json:"errorCode"`
	HTTPStatus     *int   `json:"httpStatus"`
	TotalLatencyMS int64  `json:"totalLatencyMs"`
	FinishedAt     int64  `json:"finishedAt"`
}

func (handler *Handler) newMonitorRouteResponse(
	request *http.Request,
	view vnextstore.MonitorRouteView,
) (monitorRouteResponse, error) {
	now := handler.now().UTC()
	response := monitorRouteResponse{
		PublishedModelID: view.Setting.PublishedModelID, PublicModel: view.PublicModel,
		OfficialPriceSKU: view.OfficialPriceSKU, PublishedModelEnabled: view.PublishedModelEnabled,
		PublishedModelRevision: view.PublishedModelRevision, Monitor: newMonitorSettingResponse(view.Setting, now),
		Targets: make([]monitorTargetResponse, 0, len(view.Targets)),
	}
	activeSummaries := make([]monitoring.TargetSummary, 0, len(view.Targets))
	for _, target := range view.Targets {
		limit := view.Setting.HistoryLimit
		if limit <= 0 {
			limit = vnextstore.DefaultModelMonitorHistoryLimit
		}
		if limit > 1000 {
			limit = 1000
		}
		results, err := handler.repository.ListModelProbeTargetResults(
			request.Context(), view.Setting.PublishedModelID, target.ProviderModelTargetID, limit,
		)
		if err != nil {
			return monitorRouteResponse{}, err
		}
		currentResults := resultsAtRevision(results, target.ProviderModelTargetRevision)
		summary, err := summarizeStoreResults(target.ProviderModelTargetID, currentResults)
		if err != nil {
			return monitorRouteResponse{}, err
		}
		if targetConfiguredEnabled(target) {
			activeSummaries = append(activeSummaries, summary)
			response.Successes += summary.Successes
			response.Failures += summary.Failures
			response.Skipped += summary.Skipped
		}
		response.Targets = append(response.Targets, newMonitorTargetResponse(target, currentResults, summary, now))
	}
	attempted := response.Successes + response.Failures
	if attempted > 0 {
		response.SuccessBasisPoints = response.Successes * 10_000 / attempted
	}
	switch {
	case !view.Setting.Enabled:
		response.Status = "disabled"
	case !view.PublishedModelEnabled:
		response.Status = "model_disabled"
	case !hasEnabledTarget(view.Targets):
		response.Status = "unavailable"
	default:
		response.Status = string(monitoring.SummarizeModel(activeSummaries))
	}
	return response, nil
}

func newMonitorSettingResponse(setting vnextstore.ModelMonitorSetting, now time.Time) monitorSettingResponse {
	return monitorSettingResponse{
		Enabled: setting.Enabled, IntervalMS: setting.Interval.Milliseconds(), HistoryLimit: setting.HistoryLimit,
		NextProbeAt: setting.NextProbeAt.UnixMilli(), LastProbeStartedAt: unixMilliPointer(setting.LastProbeStartedAt),
		LastProbeFinishedAt: unixMilliPointer(setting.LastProbeFinishedAt),
		Busy:                setting.LeaseUntil != nil && setting.LeaseUntil.After(now), Revision: setting.Revision,
		CreatedAt: setting.CreatedAt.UnixMilli(), UpdatedAt: setting.UpdatedAt.UnixMilli(),
	}
}

func newMonitorTargetResponse(
	target vnextstore.MonitorTargetView,
	results []vnextstore.ModelProbeTargetResult,
	summary monitoring.TargetSummary,
	now time.Time,
) monitorTargetResponse {
	enabled := targetConfiguredEnabled(target)
	response := monitorTargetResponse{
		PublishedModelTargetID: target.PublishedModelTargetID, PublishedModelTargetRevision: target.PublishedModelTargetRevision,
		ProviderModelTargetID:       target.ProviderModelTargetID,
		ProviderModelTargetRevision: target.ProviderModelTargetRevision, Position: target.Position,
		SiteID: target.SiteID, SiteName: target.SiteName, EndpointID: target.EndpointID,
		EndpointName: target.EndpointName, SourceModel: target.SourceModel,
		WireProtocol: target.WireProtocol, APISurface: target.Surface, Enabled: enabled,
		UsableCredentialCount: target.UsableCredentialCount, Successes: summary.Successes,
		Failures: summary.Failures, Skipped: summary.Skipped,
		SuccessBasisPoints: summary.SuccessBasisPoints(), Health: newRoutingHealthResponse(target.Health),
		StatusBar: make([]probePointResponse, 0),
	}
	chronological := chronologicalResults(results)
	if len(chronological) > matrixStatusBarLimit {
		chronological = chronological[len(chronological)-matrixStatusBarLimit:]
	}
	for _, result := range chronological {
		response.StatusBar = append(response.StatusBar, newProbePointResponse(result))
	}
	if len(results) > 0 {
		latest := newProbePointResponse(results[0])
		response.Latest = &latest
	}
	response.Status = targetStatus(target, response.Latest, now)
	return response
}

func newTargetHistoryResponse(
	view vnextstore.MonitorRouteView,
	target vnextstore.MonitorTargetView,
	results []vnextstore.ModelProbeTargetResult,
	now time.Time,
) (targetHistoryResponse, error) {
	summary, err := summarizeStoreResults(target.ProviderModelTargetID, results)
	if err != nil {
		return targetHistoryResponse{}, err
	}
	items := make([]probePointResponse, 0, len(results))
	for _, result := range chronologicalResults(results) {
		items = append(items, newProbePointResponse(result))
	}
	var latest *probePointResponse
	if len(results) > 0 {
		value := newProbePointResponse(results[0])
		latest = &value
	}
	return targetHistoryResponse{
		PublishedModelID: view.Setting.PublishedModelID, PublicModel: view.PublicModel,
		Target: monitorTargetIdentity{
			PublishedModelTargetID: target.PublishedModelTargetID, ProviderModelTargetID: target.ProviderModelTargetID,
			Position: target.Position, SiteID: target.SiteID, SiteName: target.SiteName,
			EndpointID: target.EndpointID, EndpointName: target.EndpointName, SourceModel: target.SourceModel,
			WireProtocol: target.WireProtocol, APISurface: target.Surface,
		},
		Status: targetStatus(target, latest, now), Successes: summary.Successes, Failures: summary.Failures,
		Skipped: summary.Skipped, Total: summary.Total, Attempted: summary.Successes + summary.Failures,
		SuccessBasisPoints: summary.SuccessBasisPoints(), Health: newRoutingHealthResponse(target.Health),
		Order: "oldest_first", Items: items,
	}, nil
}

func summarizeStoreResults(targetID int64, results []vnextstore.ModelProbeTargetResult) (monitoring.TargetSummary, error) {
	converted := make([]monitoring.TargetResult, 0, len(results))
	for _, result := range results {
		converted = append(converted, monitoring.TargetResult{
			RunID: result.RunID, TargetID: result.ProviderModelTargetID, Outcome: monitoring.Outcome(result.Outcome),
			FailureKind: routing.FailureKind(result.FailureKind), ErrorCode: result.ErrorCode,
			HTTPStatus: intValue(result.HTTPStatus), PermitMode: routing.PermitMode(result.PermitMode),
			PermitReason: routing.PermitReason(result.PermitReason), LatencyMS: result.LatencyMS,
			FirstOutputLatencyMS: cloneInt64(result.FirstOutputMS), StartedAt: result.StartedAt,
			FinishedAt: result.FinishedAt, HealthApplied: result.HealthApplied,
			HealthApplyReason: routing.ApplyReason(result.HealthApplyReason), HealthErrorCode: result.HealthErrorCode,
		})
	}
	return monitoring.SummarizeTarget(targetID, converted, time.Time{}, time.Time{})
}

func newProbePointResponse(result vnextstore.ModelProbeTargetResult) probePointResponse {
	return probePointResponse{
		ID: result.ID, RunID: result.RunID, ProviderModelTargetRevision: result.ProviderModelTargetRevision,
		Outcome: result.Outcome, PermitMode: result.PermitMode,
		PermitReason: result.PermitReason, HTTPStatus: cloneInt(result.HTTPStatus), FailureKind: result.FailureKind,
		ErrorCode: result.ErrorCode, TotalLatencyMS: result.LatencyMS, FirstOutputMS: cloneInt64(result.FirstOutputMS),
		StartedAt: result.StartedAt.UnixMilli(), FinishedAt: result.FinishedAt.UnixMilli(),
		HealthApplied: result.HealthApplied, HealthApplyReason: result.HealthApplyReason,
		HealthErrorCode: result.HealthErrorCode,
	}
}

func newRoutingHealthResponse(snapshot *vnextstore.TargetHealthSnapshot) *routingHealthResponse {
	if snapshot == nil {
		return nil
	}
	state := snapshot.State
	return &routingHealthResponse{
		Phase: string(state.Phase), Capability: string(state.Capability),
		ConsecutiveFailures:    state.ConsecutiveFailures,
		FailureWindowStartedAt: timeValuePointer(state.FailureWindowStartedAt),
		LastFailureAt:          timeValuePointer(state.LastFailureAt), LastSuccessAt: timeValuePointer(state.LastSuccessAt),
		CooldownUntil: timeValuePointer(state.CooldownUntil), HalfOpenLeaseUntil: timeValuePointer(state.HalfOpenLeaseUntil),
		LastEventAt: timeValuePointer(state.LastEventAt), LastFailureKind: string(state.LastFailureKind),
		ProviderTargetRevision: int64(state.Revision), StateVersion: snapshot.StateVersion, UpdatedAt: snapshot.UpdatedAt,
	}
}

func targetStatus(target vnextstore.MonitorTargetView, latest *probePointResponse, now time.Time) string {
	if !target.SiteEnabled || !target.EndpointEnabled || !target.ProviderTargetEnabled {
		return "disabled"
	}
	if target.UsableCredentialCount == 0 {
		return "no_credentials"
	}
	if target.Health != nil && int64(target.Health.State.Revision) == target.ProviderModelTargetRevision {
		state := target.Health.State
		if state.Capability == routing.CapabilityUnsupported {
			return "unsupported"
		}
		switch state.Phase {
		case routing.CircuitOpen:
			if state.CooldownUntil.After(now) {
				return "cooling"
			}
			return "recovering"
		case routing.CircuitHalfOpen:
			return "recovering"
		case routing.CircuitSuspect:
			return "suspect"
		}
	}
	if latest == nil {
		return "unprobed"
	}
	switch latest.Outcome {
	case string(monitoring.OutcomeSuccess):
		return "healthy"
	case string(monitoring.OutcomeFailure):
		return "suspect"
	default:
		return "skipped"
	}
}

func newProbeRunResponse(run monitoring.ModelRun, view vnextstore.MonitorRouteView) probeRunResponse {
	targets := make(map[int64]vnextstore.MonitorTargetView, len(view.Targets))
	for _, target := range view.Targets {
		targets[target.ProviderModelTargetID] = target
	}
	response := probeRunResponse{
		ID: run.Run.ID, PublishedModelID: view.Setting.PublishedModelID, PublicModel: view.PublicModel,
		TriggerKind: run.Run.TriggerKind, Status: run.Run.Status, TargetCount: len(run.Results),
		StartedAt: run.Run.StartedAt.UnixMilli(), FinishedAt: unixMilliPointer(run.Run.FinishedAt),
		Results: make([]manualProbeResultResponse, 0, len(run.Results)),
	}
	for _, result := range run.Results {
		target := targets[result.TargetID]
		item := manualProbeResultResponse{
			ProviderModelTargetID: result.TargetID, Position: target.Position, SiteID: target.SiteID,
			SiteName: target.SiteName, EndpointID: target.EndpointID, EndpointName: target.EndpointName,
			SourceModel: target.SourceModel, Outcome: string(result.Outcome), FailureKind: string(result.FailureKind),
			ErrorCode: result.ErrorCode, HTTPStatus: positiveIntPointer(result.HTTPStatus),
			PermitMode: string(result.PermitMode), PermitReason: string(result.PermitReason),
			TotalLatencyMS: result.LatencyMS, FirstOutputMS: cloneInt64(result.FirstOutputLatencyMS),
			StartedAt: result.StartedAt.UnixMilli(), FinishedAt: result.FinishedAt.UnixMilli(),
			HealthApplied: result.HealthApplied, HealthApplyReason: string(result.HealthApplyReason),
			HealthErrorCode: result.HealthErrorCode,
			Attempts:        make([]manualProbeAttemptResponse, 0, len(result.Attempts)),
		}
		for _, attempt := range result.Attempts {
			item.Attempts = append(item.Attempts, manualProbeAttemptResponse{
				CredentialID: attempt.CredentialID, Outcome: string(attempt.Outcome),
				FailureKind: string(attempt.FailureKind), ErrorCode: attempt.ErrorCode,
				HTTPStatus: positiveIntPointer(attempt.HTTPStatus), TotalLatencyMS: attempt.LatencyMS,
				FinishedAt: attempt.FinishedAt.UnixMilli(),
			})
		}
		response.Results = append(response.Results, item)
		switch result.Outcome {
		case monitoring.OutcomeSuccess:
			response.SuccessCount++
		case monitoring.OutcomeFailure:
			response.FailureCount++
		case monitoring.OutcomeSkipped:
			response.SkippedCount++
		}
	}
	response.TargetCount = len(response.Results)
	switch {
	case response.FailureCount == 0 && response.SuccessCount > 0:
		response.Outcome = "success"
	case response.SuccessCount > 0 && response.FailureCount > 0:
		response.Outcome = "partial_failure"
	case response.FailureCount > 0:
		response.Outcome = "upstream_probe_failed"
	case response.SkippedCount > 0:
		response.Outcome = "skipped"
	default:
		response.Outcome = "empty"
	}
	return response
}

func hasEnabledTarget(targets []vnextstore.MonitorTargetView) bool {
	for _, target := range targets {
		if targetConfiguredEnabled(target) {
			return true
		}
	}
	return false
}

func targetConfiguredEnabled(target vnextstore.MonitorTargetView) bool {
	return target.SiteEnabled && target.EndpointEnabled && target.ProviderTargetEnabled
}

func findTarget(targets []vnextstore.MonitorTargetView, targetID int64) (vnextstore.MonitorTargetView, bool) {
	for _, target := range targets {
		if target.ProviderModelTargetID == targetID {
			return target, true
		}
	}
	return vnextstore.MonitorTargetView{}, false
}

func chronologicalResults(results []vnextstore.ModelProbeTargetResult) []vnextstore.ModelProbeTargetResult {
	ordered := append([]vnextstore.ModelProbeTargetResult(nil), results...)
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].FinishedAt.Equal(ordered[right].FinishedAt) {
			return ordered[left].ID < ordered[right].ID
		}
		return ordered[left].FinishedAt.Before(ordered[right].FinishedAt)
	})
	return ordered
}

func resultsAtRevision(
	results []vnextstore.ModelProbeTargetResult,
	revision int64,
) []vnextstore.ModelProbeTargetResult {
	filtered := make([]vnextstore.ModelProbeTargetResult, 0, len(results))
	for _, result := range results {
		if result.ProviderModelTargetRevision == revision {
			filtered = append(filtered, result)
		}
	}
	return filtered
}

func unixMilliPointer(value *time.Time) *int64 {
	if value == nil {
		return nil
	}
	result := value.UTC().UnixMilli()
	return &result
}

func timeValuePointer(value time.Time) *int64 {
	if value.IsZero() {
		return nil
	}
	result := value.UTC().UnixMilli()
	return &result
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func intValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func positiveIntPointer(value int) *int {
	if value <= 0 {
		return nil
	}
	copy := value
	return &copy
}
