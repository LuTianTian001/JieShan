package monitorapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/monitoring"
	"github.com/LuTianTian001/JieShan/internal/vnext/routing"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

func TestStoreHandlerMatrixAndRealTargetHistory(t *testing.T) {
	fixture := newAPIFixture(t)
	seedProbeHistory(t, fixture)
	seedLiveTrafficEvidence(t, fixture)
	recentAt, recent, err := fixture.storage.LatestSuccessfulRequestAttempt(
		context.Background(), fixture.selectedTargetIDs[0], 1, fixture.now.Add(-15*time.Minute),
	)
	if err != nil || !recent || !recentAt.Equal(fixture.now.Add(-10*time.Minute+time.Second)) {
		t.Fatalf("recent live success evidence = %s, %t, %v", recentAt, recent, err)
	}

	matrix := serveMonitor(fixture.handler, http.MethodGet, APIPrefix, nil, nil)
	if matrix.Code != http.StatusOK {
		t.Fatalf("matrix status = %d, body = %s", matrix.Code, matrix.Body.String())
	}
	var matrixBody struct {
		Items []struct {
			PublishedModelID int64 `json:"publishedModelId"`
			Monitor          struct {
				IntervalMS int64 `json:"intervalMs"`
			} `json:"monitor"`
			Targets []struct {
				ProviderModelTargetID int64                   `json:"providerModelTargetId"`
				SuccessBasisPoints    int                     `json:"successBasisPoints"`
				Latest                *probePointResponse     `json:"latest"`
				StatusBar             []probePointResponse    `json:"statusBar"`
				Evidence              monitorEvidenceResponse `json:"evidence"`
			} `json:"targets"`
		} `json:"items"`
	}
	if err := json.Unmarshal(matrix.Body.Bytes(), &matrixBody); err != nil {
		t.Fatal(err)
	}
	if len(matrixBody.Items) != 1 || matrixBody.Items[0].PublishedModelID != fixture.selectedRouteID {
		t.Fatalf("matrix leaked an unselected route: %+v", matrixBody.Items)
	}
	if matrixBody.Items[0].Monitor.IntervalMS != int64((15*time.Minute)/time.Millisecond) {
		t.Fatalf("default matrix interval = %d", matrixBody.Items[0].Monitor.IntervalMS)
	}
	if len(matrixBody.Items[0].Targets) != 2 {
		t.Fatalf("matrix target count = %d", len(matrixBody.Items[0].Targets))
	}
	firstTarget := matrixBody.Items[0].Targets[0]
	if firstTarget.ProviderModelTargetID != fixture.selectedTargetIDs[0] || firstTarget.SuccessBasisPoints != 5000 {
		t.Fatalf("first target summary = %+v", firstTarget)
	}
	if len(firstTarget.StatusBar) != 2 || firstTarget.StatusBar[0].Outcome != "success" ||
		firstTarget.StatusBar[1].Outcome != "failure" {
		t.Fatalf("status bar is not chronological real history: %+v", firstTarget.StatusBar)
	}
	if firstTarget.Latest == nil || firstTarget.Latest.FirstOutputMS == nil || *firstTarget.Latest.FirstOutputMS != 700 {
		t.Fatalf("latest firstOutputMs = %+v", firstTarget.Latest)
	}
	live := firstTarget.Evidence.LiveTraffic
	if live.WindowMS != monitoring.LiveTrafficEvidenceWindow.Milliseconds() || live.Samples != 4 ||
		live.Successes != 1 || live.Failures != 2 || live.Skipped != 1 || live.SuccessBasisPoints != 3333 ||
		live.P50FirstOutputMS == nil || *live.P50FirstOutputMS != 300 ||
		live.P95FirstOutputMS == nil || *live.P95FirstOutputMS != 400 ||
		live.LastFailureKind != string(routing.FailureTransport) {
		t.Fatalf("live traffic evidence = %+v", live)
	}
	probe := firstTarget.Evidence.Probe
	if probe.WindowMS != monitoring.ProbeEvidenceWindow.Milliseconds() || probe.Samples != 2 ||
		probe.Successes != 1 || probe.Failures != 1 || probe.Skipped != 0 || probe.SuccessBasisPoints != 5000 ||
		probe.P50FirstOutputMS == nil || *probe.P50FirstOutputMS != 500 ||
		probe.P95FirstOutputMS == nil || *probe.P95FirstOutputMS != 700 ||
		probe.LastFailureKind != string(routing.FailureTransport) {
		t.Fatalf("probe evidence = %+v", probe)
	}
	if strings.Contains(matrix.Body.String(), "firstToken") {
		t.Fatalf("monitor contract exposed the old ambiguous latency name: %s", matrix.Body.String())
	}

	historyPath := fmt.Sprintf("%s/models/%d/targets/%d/history", APIPrefix, fixture.selectedRouteID, fixture.selectedTargetIDs[0])
	history := serveMonitor(fixture.handler, http.MethodGet, historyPath, nil, nil)
	if history.Code != http.StatusOK {
		t.Fatalf("history status = %d, body = %s", history.Code, history.Body.String())
	}
	var historyBody targetHistoryResponse
	if err := json.Unmarshal(history.Body.Bytes(), &historyBody); err != nil {
		t.Fatal(err)
	}
	if historyBody.Total != 2 || historyBody.Attempted != 2 || historyBody.SuccessBasisPoints != 5000 ||
		historyBody.Order != "oldest_first" {
		t.Fatalf("history summary = %+v", historyBody)
	}
	if len(historyBody.Items) != 2 || historyBody.Items[0].Outcome != "success" ||
		historyBody.Items[1].Outcome != "failure" {
		t.Fatalf("history points = %+v", historyBody.Items)
	}
	if len(historyBody.CircuitTransitions) != 5 ||
		historyBody.CircuitTransitions[0].FromPhase != string(routing.CircuitClosed) ||
		historyBody.CircuitTransitions[0].ToPhase != string(routing.CircuitSuspect) ||
		historyBody.CircuitTransitions[1].ToPhase != string(routing.CircuitOpen) ||
		historyBody.CircuitTransitions[2].Trigger != string(routing.HealthEvidenceTimer) ||
		historyBody.CircuitTransitions[3].ToPhase != string(routing.CircuitClosed) ||
		historyBody.CircuitTransitions[4].Trigger != string(routing.HealthEvidenceProbe) ||
		historyBody.CircuitTransitions[4].ToPhase != string(routing.CircuitSuspect) {
		t.Fatalf("circuit transitions = %+v", historyBody.CircuitTransitions)
	}
	if historyBody.Items[1].TotalLatencyMS != 1400 || historyBody.Items[1].FirstOutputMS == nil ||
		*historyBody.Items[1].FirstOutputMS != 700 || historyBody.Items[1].HTTPStatus == nil ||
		*historyBody.Items[1].HTTPStatus != http.StatusBadGateway || historyBody.Items[1].ErrorCode != "upstream_reset" {
		t.Fatalf("real failure point lost metrics: %+v", historyBody.Items[1])
	}

	unselected := fmt.Sprintf("%s/models/%d/targets/%d/history", APIPrefix, fixture.unselectedRouteID, fixture.unselectedTargetIDs[0])
	assertAPIError(t, serveMonitor(fixture.handler, http.MethodGet, unselected, nil, nil),
		http.StatusNotFound, "monitor_not_selected")
	wrongTarget := fmt.Sprintf("%s/models/%d/targets/%d/history", APIPrefix, fixture.selectedRouteID, fixture.unselectedTargetIDs[0])
	assertAPIError(t, serveMonitor(fixture.handler, http.MethodGet, wrongTarget, nil, nil),
		http.StatusNotFound, "monitor_target_not_found")
}

func TestStoreHandlerCreatesDefaultsAndUpdatesWithCAS(t *testing.T) {
	fixture := newAPIFixture(t)
	path := fmt.Sprintf("%s/models/%d", APIPrefix, fixture.unselectedRouteID)

	assertAPIError(t, serveMonitor(fixture.handler, http.MethodPost, path,
		[]byte(`{"enabled":true,"intervalMs":120000}`), nil), http.StatusBadRequest, "invalid_request")
	if _, err := fixture.storage.GetModelMonitorSetting(context.Background(), fixture.unselectedRouteID); !errors.Is(err, vnextstore.ErrModelMonitorNotFound) {
		t.Fatalf("rejected per-model interval created a setting: %v", err)
	}

	created := serveMonitor(fixture.handler, http.MethodPost, path, []byte(`{}`), nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	if created.Header().Get("ETag") != `"1"` {
		t.Fatalf("create ETag = %q", created.Header().Get("ETag"))
	}
	var createdBody struct {
		Item monitorSettingResponse `json:"item"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdBody); err != nil {
		t.Fatal(err)
	}
	if !createdBody.Item.Enabled || createdBody.Item.IntervalMS != int64((15*time.Minute)/time.Millisecond) ||
		createdBody.Item.HistoryLimit != vnextstore.DefaultModelMonitorHistoryLimit {
		t.Fatalf("create defaults = %+v", createdBody.Item)
	}
	assertAPIError(t, serveMonitor(fixture.handler, http.MethodPost, path, []byte(`{}`), nil),
		http.StatusConflict, "monitor_already_selected")
	assertAPIError(t, serveMonitor(fixture.handler, http.MethodPatch, path, []byte(`{"enabled":false}`), nil),
		http.StatusPreconditionRequired, "precondition_required")
	assertAPIError(t, serveMonitor(fixture.handler, http.MethodPatch, path,
		[]byte(`{"intervalMs":120000}`), map[string]string{"If-Match": `"1"`}),
		http.StatusBadRequest, "invalid_request")

	updated := serveMonitor(fixture.handler, http.MethodPatch, path,
		[]byte(`{"enabled":false,"historyLimit":48}`), map[string]string{"If-Match": `"1"`})
	if updated.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updated.Code, updated.Body.String())
	}
	if updated.Header().Get("ETag") != `"2"` {
		t.Fatalf("update ETag = %q", updated.Header().Get("ETag"))
	}
	var updatedBody struct {
		Item monitorSettingResponse `json:"item"`
	}
	if err := json.Unmarshal(updated.Body.Bytes(), &updatedBody); err != nil {
		t.Fatal(err)
	}
	if updatedBody.Item.Enabled || updatedBody.Item.IntervalMS != int64((15*time.Minute)/time.Millisecond) ||
		updatedBody.Item.HistoryLimit != 48 {
		t.Fatalf("updated setting = %+v", updatedBody.Item)
	}
	assertAPIError(t, serveMonitor(fixture.handler, http.MethodPatch, path,
		[]byte(`{"enabled":true}`), map[string]string{"If-Match": `"1"`}),
		http.StatusConflict, "revision_conflict")
	assertAPIError(t, serveMonitor(fixture.handler, http.MethodPost,
		fmt.Sprintf("%s/models/999999", APIPrefix), []byte(`{}`), nil),
		http.StatusNotFound, "model_not_found")

	matrix := serveMonitor(fixture.handler, http.MethodGet, APIPrefix, nil, nil)
	if matrix.Code != http.StatusOK {
		t.Fatalf("matrix status = %d, body = %s", matrix.Code, matrix.Body.String())
	}
	var matrixBody struct {
		Items []monitorRouteResponse `json:"items"`
	}
	if err := json.Unmarshal(matrix.Body.Bytes(), &matrixBody); err != nil {
		t.Fatal(err)
	}
	if len(matrixBody.Items) != 2 {
		t.Fatalf("explicit monitor count = %d", len(matrixBody.Items))
	}
	if matrixBody.Items[1].PublishedModelID != fixture.unselectedRouteID || matrixBody.Items[1].Status != "disabled" {
		t.Fatalf("disabled explicit monitor = %+v", matrixBody.Items[1])
	}
}

func TestStoreHandlerManualProbeSemantics(t *testing.T) {
	fixture := newAPIFixture(t)
	path := fmt.Sprintf("%s/models/%d/probe", APIPrefix, fixture.selectedRouteID)
	startedAt := fixture.now.Add(time.Minute)
	finishedAt := startedAt.Add(2 * time.Second)

	fixture.prober.set(monitoring.ModelRun{
		Run: vnextstore.ModelProbeRun{
			ID: "manual-partial", PublishedModelID: fixture.selectedRouteID, PublicModelSnapshot: "public-selected",
			TriggerKind: "manual", Status: "completed", StartedAt: startedAt, FinishedAt: &finishedAt,
		},
		Results: []monitoring.TargetResult{
			{
				RunID: "manual-partial", TargetID: fixture.selectedTargetIDs[0], Outcome: monitoring.OutcomeSuccess,
				HTTPStatus: http.StatusOK, LatencyMS: 900, FirstOutputLatencyMS: int64Pointer(300),
				StartedAt: startedAt, FinishedAt: startedAt.Add(900 * time.Millisecond),
			},
			{
				RunID: "manual-partial", TargetID: fixture.selectedTargetIDs[1], Outcome: monitoring.OutcomeFailure,
				FailureKind: routing.FailureTransport, ErrorCode: "upstream_timeout", LatencyMS: 2000,
				StartedAt: startedAt, FinishedAt: finishedAt,
			},
		},
	}, nil)
	partial := serveMonitor(fixture.handler, http.MethodPost, path, nil, nil)
	if partial.Code != http.StatusOK {
		t.Fatalf("partial probe status = %d, body = %s", partial.Code, partial.Body.String())
	}
	var partialBody struct {
		Run probeRunResponse `json:"run"`
	}
	if err := json.Unmarshal(partial.Body.Bytes(), &partialBody); err != nil {
		t.Fatal(err)
	}
	if partialBody.Run.Outcome != "partial_failure" || partialBody.Run.SuccessCount != 1 ||
		partialBody.Run.FailureCount != 1 || len(partialBody.Run.Results) != 2 {
		t.Fatalf("partial probe response = %+v", partialBody.Run)
	}
	if partialBody.Run.Results[0].FirstOutputMS == nil || *partialBody.Run.Results[0].FirstOutputMS != 300 {
		t.Fatalf("manual probe did not expose firstOutputMs: %+v", partialBody.Run.Results[0])
	}

	fixture.prober.set(monitoring.ModelRun{
		Run: vnextstore.ModelProbeRun{
			ID: "manual-failed", PublishedModelID: fixture.selectedRouteID, TriggerKind: "manual",
			Status: "completed", StartedAt: startedAt, FinishedAt: &finishedAt,
		},
		Results: []monitoring.TargetResult{
			{RunID: "manual-failed", TargetID: fixture.selectedTargetIDs[0], Outcome: monitoring.OutcomeFailure,
				FailureKind: routing.FailureTransport, ErrorCode: "connect_failed", LatencyMS: 800,
				StartedAt: startedAt, FinishedAt: startedAt.Add(800 * time.Millisecond)},
			{RunID: "manual-failed", TargetID: fixture.selectedTargetIDs[1], Outcome: monitoring.OutcomeFailure,
				FailureKind: routing.FailureTransport, ErrorCode: "probe_timeout", LatencyMS: 2000,
				StartedAt: startedAt, FinishedAt: finishedAt},
		},
	}, nil)
	failed := serveMonitor(fixture.handler, http.MethodPost, path, nil, nil)
	assertAPIError(t, failed, http.StatusBadGateway, "upstream_probe_failed")
	if !strings.Contains(failed.Body.String(), `"run"`) || !strings.Contains(failed.Body.String(), `"connect_failed"`) {
		t.Fatalf("failed probe response omitted target evidence: %s", failed.Body.String())
	}

	fixture.prober.set(monitoring.ModelRun{}, monitoring.ErrProbeInProgress)
	assertAPIError(t, serveMonitor(fixture.handler, http.MethodPost, path, nil, nil),
		http.StatusConflict, "monitor_busy")

	unselectedPath := fmt.Sprintf("%s/models/%d/probe", APIPrefix, fixture.unselectedRouteID)
	assertAPIError(t, serveMonitor(fixture.handler, http.MethodPost, unselectedPath, nil, nil),
		http.StatusNotFound, "monitor_not_selected")

	withoutProber, err := NewStoreHandler(fixture.storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	withoutProber.now = func() time.Time { return fixture.now }
	readable := serveMonitor(withoutProber, http.MethodGet, APIPrefix, nil, nil)
	if readable.Code != http.StatusOK {
		t.Fatalf("configuration became unreadable without a prober: status=%d body=%s", readable.Code, readable.Body.String())
	}
	assertAPIError(t, serveMonitor(withoutProber, http.MethodPost, path, nil, nil),
		http.StatusServiceUnavailable, "probe_unavailable")

	setting, err := fixture.storage.GetModelMonitorSetting(context.Background(), fixture.selectedRouteID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.storage.UpdateModelMonitorSettingCAS(context.Background(), fixture.selectedRouteID, setting.Revision,
		vnextstore.ModelMonitorSettingWrite{Enabled: false, HistoryLimit: setting.HistoryLimit},
		fixture.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	beforeCalls := fixture.prober.callCount()
	assertAPIError(t, serveMonitor(fixture.handler, http.MethodPost, path, nil, nil),
		http.StatusConflict, "monitor_disabled")
	if fixture.prober.callCount() != beforeCalls {
		t.Fatal("disabled monitor invoked the injected prober")
	}
	historyPath := fmt.Sprintf("%s/models/%d/targets/%d/history", APIPrefix, fixture.selectedRouteID, fixture.selectedTargetIDs[0])
	if response := serveMonitor(fixture.handler, http.MethodGet, historyPath, nil, nil); response.Code != http.StatusOK {
		t.Fatalf("disabled monitor history status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestStoreHandlerManualTargetProbe(t *testing.T) {
	fixture := newAPIFixture(t)
	targetID := fixture.selectedTargetIDs[0]
	path := fmt.Sprintf("%s/models/%d/targets/%d/probe", APIPrefix, fixture.selectedRouteID, targetID)
	startedAt := fixture.now.Add(time.Minute)
	finishedAt := startedAt.Add(500 * time.Millisecond)
	fixture.prober.set(monitoring.ModelRun{
		Run: vnextstore.ModelProbeRun{
			ID: "manual-target", PublishedModelID: fixture.selectedRouteID, PublicModelSnapshot: "public-selected",
			TriggerKind: "manual", Status: "completed", TargetCount: 1, StartedAt: startedAt, FinishedAt: &finishedAt,
		},
		Results: []monitoring.TargetResult{{
			RunID: "manual-target", TargetID: targetID, Outcome: monitoring.OutcomeSuccess,
			HTTPStatus: http.StatusOK, LatencyMS: 500, FirstOutputLatencyMS: int64Pointer(180),
			StartedAt: startedAt, FinishedAt: finishedAt,
		}},
	}, nil)

	response := serveMonitor(fixture.handler, http.MethodPost, path, nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("target probe status = %d, body = %s", response.Code, response.Body.String())
	}
	var body struct {
		Run probeRunResponse `json:"run"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Run.TargetCount != 1 || len(body.Run.Results) != 1 || body.Run.Results[0].ProviderModelTargetID != targetID {
		t.Fatalf("target probe response = %+v", body.Run)
	}
	modelID, calledTargetID, calls := fixture.prober.targetCall()
	if calls != 1 || modelID != fixture.selectedRouteID || calledTargetID != targetID {
		t.Fatalf("target prober call = model %d target %d calls %d", modelID, calledTargetID, calls)
	}

	unknown := fmt.Sprintf("%s/models/%d/targets/%d/probe", APIPrefix, fixture.selectedRouteID, 999999)
	assertAPIError(t, serveMonitor(fixture.handler, http.MethodPost, unknown, nil, nil),
		http.StatusNotFound, "monitor_target_not_found")
}

func TestStoreHandlerManualTargetsProbe(t *testing.T) {
	fixture := newAPIFixture(t)
	path := fmt.Sprintf("%s/models/%d/targets/probe", APIPrefix, fixture.selectedRouteID)
	startedAt := fixture.now.Add(time.Minute)
	finishedAt := startedAt.Add(500 * time.Millisecond)
	fixture.prober.set(monitoring.ModelRun{
		Run: vnextstore.ModelProbeRun{
			ID: "manual-targets", PublishedModelID: fixture.selectedRouteID, PublicModelSnapshot: "public-selected",
			TriggerKind: "manual", Status: "completed", TargetCount: 2, StartedAt: startedAt, FinishedAt: &finishedAt,
		},
		Results: []monitoring.TargetResult{
			{
				RunID: "manual-targets", TargetID: fixture.selectedTargetIDs[0], Outcome: monitoring.OutcomeSuccess,
				HTTPStatus: http.StatusOK, LatencyMS: 400, StartedAt: startedAt, FinishedAt: finishedAt,
			},
			{
				RunID: "manual-targets", TargetID: fixture.selectedTargetIDs[1], Outcome: monitoring.OutcomeSuccess,
				HTTPStatus: http.StatusOK, LatencyMS: 500, StartedAt: startedAt, FinishedAt: finishedAt,
			},
		},
	}, nil)

	for _, body := range [][]byte{
		[]byte(`{}`),
		[]byte(`{"providerModelTargetIds":[]}`),
		[]byte(`{"providerModelTargetIds":[0]}`),
		[]byte(`{"providerModelTargetIds":[1.5]}`),
		[]byte(`{"providerModelTargetIds":["1"]}`),
	} {
		assertAPIError(t, serveMonitor(fixture.handler, http.MethodPost, path, body, nil),
			http.StatusBadRequest, "invalid_request")
	}
	if _, _, calls := fixture.prober.targetsCall(); calls != 0 {
		t.Fatalf("invalid requests invoked multi-target prober %d times", calls)
	}

	foreignBody, err := json.Marshal(targetsProbeRequest{ProviderModelTargetIDs: []int64{
		fixture.selectedTargetIDs[0], fixture.unselectedTargetIDs[0],
	}})
	if err != nil {
		t.Fatal(err)
	}
	assertAPIError(t, serveMonitor(fixture.handler, http.MethodPost, path, foreignBody, nil),
		http.StatusNotFound, "monitor_target_not_found")

	requested := []int64{fixture.selectedTargetIDs[1], fixture.selectedTargetIDs[0], fixture.selectedTargetIDs[1]}
	requestBody, err := json.Marshal(targetsProbeRequest{ProviderModelTargetIDs: requested})
	if err != nil {
		t.Fatal(err)
	}
	response := serveMonitor(fixture.handler, http.MethodPost, path, requestBody, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("targets probe status = %d, body = %s", response.Code, response.Body.String())
	}
	var body struct {
		Run probeRunResponse `json:"run"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Run.TargetCount != 2 || len(body.Run.Results) != 2 {
		t.Fatalf("targets probe response = %+v", body.Run)
	}
	modelID, targetIDs, calls := fixture.prober.targetsCall()
	wantTargetIDs := []int64{fixture.selectedTargetIDs[1], fixture.selectedTargetIDs[0]}
	if calls != 1 || modelID != fixture.selectedRouteID || !reflect.DeepEqual(targetIDs, wantTargetIDs) {
		t.Fatalf("targets prober call = model %d targets %+v calls %d", modelID, targetIDs, calls)
	}

	target, err := fixture.storage.GetProviderModelTarget(context.Background(), fixture.selectedTargetIDs[1])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.storage.UpdateProviderModelTarget(context.Background(), target.SiteID, target.EndpointID, target.ID,
		vnextstore.ProviderModelTargetUpdate{
			ExpectedRevision: target.Revision, SourceModel: target.SourceModel,
			DisplayName: target.DisplayName, Enabled: false,
		}); err != nil {
		t.Fatal(err)
	}
	disabledBody, err := json.Marshal(targetsProbeRequest{ProviderModelTargetIDs: []int64{target.ID}})
	if err != nil {
		t.Fatal(err)
	}
	assertAPIError(t, serveMonitor(fixture.handler, http.MethodPost, path, disabledBody, nil),
		http.StatusConflict, "monitor_disabled")
	if _, _, calls := fixture.prober.targetsCall(); calls != 1 {
		t.Fatalf("disabled target invoked multi-target prober; calls = %d", calls)
	}

	modelOnly, err := NewStoreHandler(fixture.storage, ProbeModelFunc(func(context.Context, int64) (monitoring.ModelRun, error) {
		return monitoring.ModelRun{}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	availableBody, err := json.Marshal(targetsProbeRequest{ProviderModelTargetIDs: []int64{fixture.selectedTargetIDs[0]}})
	if err != nil {
		t.Fatal(err)
	}
	assertAPIError(t, serveMonitor(modelOnly, http.MethodPost, path, availableBody, nil),
		http.StatusServiceUnavailable, "probe_unavailable")
	assertAPIError(t, serveMonitor(fixture.handler, http.MethodGet, path, nil, nil),
		http.StatusMethodNotAllowed, "method_not_allowed")
}

type apiFixture struct {
	storage             *vnextstore.Store
	handler             *Handler
	prober              *fakeModelProber
	now                 time.Time
	selectedRouteID     int64
	selectedTargetIDs   []int64
	unselectedRouteID   int64
	unselectedTargetIDs []int64
}

func newAPIFixture(t *testing.T) apiFixture {
	t.Helper()
	ctx := context.Background()
	storage, err := vnextstore.Open(ctx, filepath.Join(t.TempDir(), "vnext.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	selectedRouteID, selectedTargetIDs := createAPIModel(t, storage, "selected", 2)
	unselectedRouteID, unselectedTargetIDs := createAPIModel(t, storage, "unselected", 1)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	if _, err := storage.CreateModelMonitorSetting(ctx, selectedRouteID,
		vnextstore.ModelMonitorSettingWrite{Enabled: true}, now); err != nil {
		t.Fatal(err)
	}
	prober := &fakeModelProber{}
	handler, err := NewStoreHandler(storage, prober)
	if err != nil {
		t.Fatal(err)
	}
	handler.now = func() time.Time { return now }
	return apiFixture{
		storage: storage, handler: handler, prober: prober, now: now,
		selectedRouteID: selectedRouteID, selectedTargetIDs: selectedTargetIDs,
		unselectedRouteID: unselectedRouteID, unselectedTargetIDs: unselectedTargetIDs,
	}
}

func createAPIModel(t *testing.T, storage *vnextstore.Store, suffix string, targetCount int) (int64, []int64) {
	t.Helper()
	ctx := context.Background()
	targetIDs := make([]int64, 0, targetCount)
	for index := 0; index < targetCount; index++ {
		siteID, err := storage.CreateSite(ctx, vnextstore.SiteWrite{
			Name: fmt.Sprintf("Monitor API %s site %d", suffix, index), Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		endpointID, err := storage.CreateSiteEndpoint(ctx, siteID, vnextstore.SiteEndpointWrite{
			Name: fmt.Sprintf("Endpoint %d", index), BaseURL: fmt.Sprintf("https://%s-%d.example/v1", suffix, index),
			WireProtocol: "openai", Surface: "openai.chat_completions", AdapterKind: "generic",
			AuthScheme: "bearer", Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		credentialID, err := storage.CreateSiteCredential(ctx, siteID, vnextstore.SiteCredentialWrite{
			Name: "Primary", SecretCipher: []byte{1, 2, 3}, CipherVersion: 1, Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		endpoint, err := storage.GetSiteEndpoint(ctx, endpointID)
		if err != nil {
			t.Fatal(err)
		}
		if err := storage.ReplaceEndpointCredentialBindings(ctx, siteID, endpointID, endpoint.Revision,
			[]int64{credentialID}); err != nil {
			t.Fatal(err)
		}
		targetID, err := storage.CreateProviderModelTarget(ctx, vnextstore.ProviderModelTargetWrite{
			SiteID: siteID, EndpointID: endpointID, SourceModel: "model-" + suffix, Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		targetIDs = append(targetIDs, targetID)
	}
	model, err := storage.CreatePublishedModel(ctx, vnextstore.PublishedModelWrite{
		PublicName: "public-" + suffix, OfficialPriceSKU: "public-" + suffix, Enabled: true,
	}, targetIDs)
	if err != nil {
		t.Fatal(err)
	}
	return model.ID, targetIDs
}

func seedProbeHistory(t *testing.T, fixture apiFixture) {
	t.Helper()
	ctx := context.Background()
	views, err := fixture.storage.ListMonitorRouteViews(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var selected vnextstore.MonitorRouteView
	for _, view := range views {
		if view.Setting.PublishedModelID == fixture.selectedRouteID {
			selected = view
			break
		}
	}
	if len(selected.Targets) != 2 {
		t.Fatalf("selected target fixture = %+v", selected.Targets)
	}
	for runIndex := 0; runIndex < 2; runIndex++ {
		startedAt := fixture.now.Add(time.Duration(runIndex-2) * time.Minute)
		owner := fmt.Sprintf("monitor-api-seed-%d", runIndex)
		job, err := fixture.storage.ClaimModelMonitor(ctx, fixture.selectedRouteID, owner, startedAt, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		runID := fmt.Sprintf("monitor-api-run-%d", runIndex)
		if err := fixture.storage.StartModelProbeRun(ctx, vnextstore.ModelProbeRunWrite{
			ID: runID, PublishedModelID: fixture.selectedRouteID, LeaseOwner: job.Setting.LeaseOwner,
			TriggerKind: "scheduled", TargetCount: len(job.Targets), StartedAt: startedAt,
		}); err != nil {
			t.Fatal(err)
		}
		for targetIndex, target := range selected.Targets {
			latency := int64(1000 + runIndex*400 + targetIndex*100)
			firstOutput := latency / 2
			status := http.StatusOK
			outcome := "success"
			failureKind := ""
			errorCode := ""
			permitReason := "granted"
			if runIndex == 1 && targetIndex == 0 {
				status = http.StatusBadGateway
				outcome = "failure"
				failureKind = string(routing.FailureTransport)
				errorCode = "upstream_reset"
			}
			if runIndex == 1 && targetIndex == 1 {
				outcome = "skipped"
				status = 0
				permitReason = "cooling"
				firstOutput = 0
			}
			var httpStatus *int
			if status > 0 {
				httpStatus = &status
			}
			var firstOutputMS *int64
			if firstOutput > 0 {
				firstOutputMS = &firstOutput
			}
			if err := fixture.storage.SaveModelProbeTargetResult(ctx, vnextstore.ModelProbeTargetWrite{
				RunID: runID, PublishedModelID: fixture.selectedRouteID,
				PublishedModelTargetID:       target.PublishedModelTargetID,
				PublishedModelTargetRevision: target.PublishedModelTargetRevision,
				ProviderModelTargetID:        target.ProviderModelTargetID,
				ProviderModelTargetRevision:  target.ProviderModelTargetRevision, TargetPosition: target.Position,
				SiteID: target.SiteID, EndpointID: target.EndpointID, SiteName: target.SiteName,
				EndpointName: target.EndpointName, SourceModel: target.SourceModel,
				WireProtocol: target.WireProtocol, Surface: target.Surface, Outcome: outcome,
				PermitMode: "normal", PermitReason: permitReason, HTTPStatus: httpStatus,
				FailureKind: failureKind, ErrorCode: errorCode, LatencyMS: latency,
				FirstOutputMS: firstOutputMS, StartedAt: startedAt,
				FinishedAt:    startedAt.Add(time.Duration(latency) * time.Millisecond),
				HealthApplied: true, HealthApplyReason: "accepted",
			}); err != nil {
				t.Fatal(err)
			}
		}
		finishedAt := startedAt.Add(2 * time.Second)
		if _, err := fixture.storage.FinishModelProbeRun(ctx, runID, "completed", finishedAt); err != nil {
			t.Fatal(err)
		}
		if err := fixture.storage.FinishModelMonitorClaim(ctx, fixture.selectedRouteID, owner, finishedAt); err != nil {
			t.Fatal(err)
		}
	}
}

func seedLiveTrafficEvidence(t *testing.T, fixture apiFixture) {
	t.Helper()
	ctx := context.Background()
	views, err := fixture.storage.ListMonitorRouteViews(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var selected vnextstore.MonitorRouteView
	for _, view := range views {
		if view.Setting.PublishedModelID == fixture.selectedRouteID {
			selected = view
			break
		}
	}
	if len(selected.Targets) == 0 {
		t.Fatal("selected monitor has no target")
	}
	target := selected.Targets[0]
	var credentialID int64
	if err := fixture.storage.DB.QueryRowContext(ctx,
		`SELECT credential_id FROM credential_endpoint_bindings WHERE endpoint_id=? AND enabled=1 ORDER BY position,credential_id LIMIT 1`,
		target.EndpointID).Scan(&credentialID); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("monitor-live-evidence-key"))
	key, err := fixture.storage.CreateRevealableDownstreamKey(ctx, vnextstore.DownstreamKeyWrite{
		Name: "Monitor evidence", KeyPrefix: "js_monitor_evidence", KeyDigest: digest[:], Enabled: true,
	}, 1, func(int64) ([]byte, error) { return []byte{1, 2, 3}, nil })
	if err != nil {
		t.Fatal(err)
	}

	type evidenceAttempt struct {
		id          string
		startedAt   time.Time
		status      string
		httpStatus  int
		failureKind string
		errorCode   string
		firstOutput *int64
	}
	firstFailureOutput := int64(300)
	secondFailureOutput := int64(400)
	successOutput := int64(250)
	attempts := []evidenceAttempt{
		{id: "live-failure-1", startedAt: fixture.now.Add(-30 * time.Minute), status: "failed", httpStatus: 503,
			failureKind: string(routing.FailureTransport), errorCode: "upstream_reset", firstOutput: &firstFailureOutput},
		{id: "live-failure-2", startedAt: fixture.now.Add(-26 * time.Minute), status: "failed", httpStatus: 503,
			failureKind: string(routing.FailureTransport), errorCode: "upstream_reset", firstOutput: &secondFailureOutput},
		{id: "live-success", startedAt: fixture.now.Add(-10 * time.Minute), status: "success", httpStatus: 200,
			firstOutput: &successOutput},
		{id: "live-throttle", startedAt: fixture.now.Add(-5 * time.Minute), status: "failed", httpStatus: 429,
			failureKind: string(routing.FailureUnknown), errorCode: "upstream_concurrency"},
	}
	for _, item := range attempts {
		startedAt := item.startedAt.UTC().UnixMilli()
		if _, err := fixture.storage.StartRequestWithQuotaReservation(ctx, vnextstore.RequestStart{
			ID: item.id, DownstreamKeyID: key.ID,
			PublishedModelID: fixture.selectedRouteID, PublishedModelRevision: selected.PublishedModelRevision,
			EffectiveRoutingProfileID: key.RoutingProfileID, EffectiveRoutingProfileName: key.RoutingProfileName,
			SourceRoutingProfileID: key.RoutingProfileID, SourceRoutingProfileName: key.RoutingProfileName,
			RouteRevision: selected.PublishedModelRevision,
			RouteCandidates: []vnextstore.RequestRouteCandidateWrite{{
				Position: target.Position, PublishedModelTargetID: target.PublishedModelTargetID,
				PublishedModelTargetRevision: target.PublishedModelTargetRevision,
				ProviderModelTargetID:        target.ProviderModelTargetID,
				ProviderModelTargetRevision:  target.ProviderModelTargetRevision,
				SiteID:                       target.SiteID, SiteName: target.SiteName, EndpointID: target.EndpointID,
				EndpointName: target.EndpointName, SourceModel: target.SourceModel,
				WireProtocol: target.WireProtocol, APISurface: target.Surface,
				Credentials:        []vnextstore.RequestRouteCredentialSnapshot{},
				InitialEligibility: "eligible", InitialReason: "ready",
			}},
			PublicModel: selected.PublicModel, APISurface: target.Surface,
			PriceCatalogVersion: "monitor-evidence", PriceSKU: selected.OfficialPriceSKU,
			StartedAt: startedAt,
		}); err != nil {
			t.Fatal(err)
		}
		finishedAt := startedAt + 1000
		status := item.httpStatus
		if err := fixture.storage.RecordRequestAttempt(ctx, vnextstore.RequestAttemptWrite{
			RequestID: item.id, AttemptIndex: 0,
			PublishedModelTargetID:       target.PublishedModelTargetID,
			PublishedModelTargetRevision: target.PublishedModelTargetRevision,
			ProviderModelTargetID:        target.ProviderModelTargetID,
			ProviderModelTargetRevision:  target.ProviderModelTargetRevision,
			SiteID:                       target.SiteID, EndpointID: target.EndpointID, CredentialID: credentialID,
			SiteName: target.SiteName, EndpointName: target.EndpointName, CredentialName: "Primary",
			SourceModel: target.SourceModel, WireProtocol: target.WireProtocol, APISurface: target.Surface,
			Status: item.status, HTTPStatus: &status, FailureKind: item.failureKind, ErrorCode: item.errorCode,
			FirstTokenMS: item.firstOutput, DurationMS: 1000, StartedAt: startedAt, FinishedAt: finishedAt,
		}); err != nil {
			t.Fatal(err)
		}
		if item.status == "success" {
			permit, err := fixture.storage.AcquireTargetAttempt(
				ctx, target.ProviderModelTargetID, routing.Revision(target.ProviderModelTargetRevision),
				routing.DefaultHealthPolicy(), item.startedAt,
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, result, err := fixture.storage.ApplyTargetHealthEvent(
				ctx, target.ProviderModelTargetID, routing.DefaultHealthPolicy(), routing.HealthEvent{
					Revision: routing.Revision(target.ProviderModelTargetRevision), Sequence: permit.Sequence,
					OccurredAt: time.UnixMilli(finishedAt).UTC(), Outcome: routing.HealthSuccess,
				},
			); err != nil || !result.Applied {
				t.Fatalf("apply live success health evidence: result=%+v err=%v", result, err)
			}
		}
	}
}

type fakeModelProber struct {
	mu            sync.Mutex
	run           monitoring.ModelRun
	err           error
	calls         int
	targetCalls   int
	targetsCalls  int
	lastModelID   int64
	lastTargetID  int64
	lastTargetIDs []int64
}

func (prober *fakeModelProber) set(run monitoring.ModelRun, err error) {
	prober.mu.Lock()
	prober.run = run
	prober.err = err
	prober.mu.Unlock()
}

func (prober *fakeModelProber) ProbeModel(context.Context, int64) (monitoring.ModelRun, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	prober.calls++
	return prober.run, prober.err
}

func (prober *fakeModelProber) ProbeTarget(_ context.Context, modelID, targetID int64) (monitoring.ModelRun, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	prober.targetCalls++
	prober.lastModelID = modelID
	prober.lastTargetID = targetID
	return prober.run, prober.err
}

func (prober *fakeModelProber) ProbeTargets(_ context.Context, modelID int64, targetIDs []int64) (monitoring.ModelRun, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	prober.targetsCalls++
	prober.lastModelID = modelID
	prober.lastTargetIDs = append([]int64(nil), targetIDs...)
	return prober.run, prober.err
}

func (prober *fakeModelProber) targetCall() (int64, int64, int) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	return prober.lastModelID, prober.lastTargetID, prober.targetCalls
}

func (prober *fakeModelProber) targetsCall() (int64, []int64, int) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	return prober.lastModelID, append([]int64(nil), prober.lastTargetIDs...), prober.targetsCalls
}

func (prober *fakeModelProber) callCount() int {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	return prober.calls
}

func serveMonitor(handler http.Handler, method, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertAPIError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d, body = %s", response.Code, status, response.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != code {
		t.Fatalf("error code = %q, want %q, body = %s", body.Error.Code, code, response.Body.String())
	}
}

func int64Pointer(value int64) *int64 {
	return &value
}

func TestNewRejectsNilRepositoryAndTreatsTypedNilProberAsUnavailable(t *testing.T) {
	if _, err := New(nil, nil); err == nil {
		t.Fatal("New accepted a nil repository")
	}
	fixture := newAPIFixture(t)
	var typedNil *fakeModelProber
	handler, err := New(fixture.storage, typedNil)
	if err != nil {
		t.Fatal(err)
	}
	handler.now = func() time.Time { return fixture.now }
	path := fmt.Sprintf("%s/models/%d/probe", APIPrefix, fixture.selectedRouteID)
	assertAPIError(t, serveMonitor(handler, http.MethodPost, path, nil, nil),
		http.StatusServiceUnavailable, "probe_unavailable")
}

func TestProbeModelFuncRejectsNilFunction(t *testing.T) {
	var function ProbeModelFunc
	_, err := function.ProbeModel(context.Background(), 1)
	if err == nil {
		t.Fatal("nil ProbeModelFunc unexpectedly succeeded")
	}
}

func TestTargetStatusKeepsRecentLiveSuccessHealthyAfterProbeExemption(t *testing.T) {
	target := vnextstore.MonitorTargetView{
		ProviderModelTargetRevision: 1,
		SiteEnabled:                 true, EndpointEnabled: true, ProviderTargetEnabled: true, UsableCredentialCount: 1,
		Health: &vnextstore.TargetHealthSnapshot{State: routing.HealthState{
			Revision: 1, Phase: routing.CircuitClosed, Capability: routing.CapabilitySupported,
		}},
	}
	latest := &probePointResponse{Outcome: string(monitoring.OutcomeSkipped), PermitReason: string(routing.PermitRecentSuccess)}
	if status := targetStatus(target, latest, monitorEvidenceSummaryResponse{}, time.Now().UTC()); status != "healthy" {
		t.Fatalf("recent live success with skipped duplicate probe status = %q", status)
	}
}
