package platformdetect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/siteadmin"
)

const maxProbeBody = 1 << 20

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type AdapterLookup interface {
	Lookup(string) (siteadmin.Adapter, error)
}

type ManualSelection struct {
	Platform string
	Origin   string
	Locked   bool
}

type Input struct {
	SiteID int64
	Origin string
	Manual *ManualSelection
}

type Capabilities struct {
	SessionRefresh bool `json:"sessionRefresh"`
	Balance        bool `json:"balance"`
	Usage          bool `json:"usage"`
}

type Evidence struct {
	ID            string `json:"id"`
	Source        string `json:"source"`
	Signal        string `json:"signal"`
	ObservedValue string `json:"observedValue"`
	Matched       bool   `json:"matched"`
	Weight        int    `json:"weight"`
	ObservedAt    int64  `json:"observedAt"`
}

type Candidate struct {
	Platform     string       `json:"platform"`
	Label        string       `json:"label"`
	Confidence   string       `json:"confidence"`
	Score        int          `json:"score"`
	Supported    bool         `json:"supported"`
	Capabilities Capabilities `json:"capabilities"`
	EvidenceIDs  []string     `json:"evidenceIds"`
}

type ProbeError struct {
	ProbeID    string `json:"probeId"`
	Path       string `json:"path"`
	Code       string `json:"code"`
	Status     int    `json:"status,omitempty"`
	Message    string `json:"message"`
	ObservedAt int64  `json:"observedAt"`
}

type Result struct {
	SiteID                int64        `json:"siteId"`
	State                 string       `json:"state"`
	Verdict               string       `json:"verdict"`
	SelectedPlatform      string       `json:"selectedPlatform"`
	SelectedPlatformLabel string       `json:"selectedPlatformLabel"`
	SelectionLocked       bool         `json:"selectionLocked"`
	Confidence            string       `json:"confidence"`
	Score                 int          `json:"score"`
	Capabilities          Capabilities `json:"capabilities"`
	CheckedAt             int64        `json:"checkedAt"`
	DetectedAt            *int64       `json:"detectedAt"`
	Candidates            []Candidate  `json:"candidates"`
	Evidence              []Evidence   `json:"evidence"`
	Errors                []ProbeError `json:"errors"`
}

type Detector struct {
	client HTTPDoer
	lookup AdapterLookup
	now    func() time.Time
}

func New(client HTTPDoer, lookup AdapterLookup) (*Detector, error) {
	if client == nil {
		return nil, errors.New("platform detector requires a secured HTTP client")
	}
	return &Detector{client: client, lookup: lookup, now: time.Now}, nil
}

type scoredCandidate struct {
	score       int
	evidenceIDs []string
}

func (detector *Detector) Detect(ctx context.Context, input Input) Result {
	checkedAt := detector.now().UTC().UnixMilli()
	result := Result{
		SiteID: input.SiteID, State: "unknown", Verdict: "unknown", Confidence: "unknown",
		CheckedAt: checkedAt, Candidates: []Candidate{}, Evidence: []Evidence{}, Errors: []ProbeError{},
	}
	scores := map[string]*scoredCandidate{}
	origin := strings.TrimSpace(input.Origin)
	if input.Manual != nil && strings.TrimSpace(input.Manual.Origin) != "" {
		origin = input.Manual.Origin
	}
	base, err := probeOrigin(origin)
	if err != nil {
		result.Errors = append(result.Errors, ProbeError{
			ProbeID: "origin", Code: "invalid_origin", Message: "platform detection origin is unavailable or invalid",
			ObservedAt: checkedAt,
		})
	} else {
		detector.probeStatus(ctx, base, checkedAt, scores, &result)
		detector.probeSub2API(ctx, base, checkedAt, scores, &result)
	}
	result.Candidates = detector.candidates(scores)
	if len(result.Candidates) > 0 {
		detectedAt := checkedAt
		result.DetectedAt = &detectedAt
	}
	detector.selectResult(&result, input.Manual)
	return result
}

func (detector *Detector) probeStatus(
	ctx context.Context,
	base *url.URL,
	observedAt int64,
	scores map[string]*scoredCandidate,
	result *Result,
) {
	value, ok := detector.getJSON(ctx, base, "status", "/api/status", observedAt, result)
	if !ok {
		return
	}
	envelope, ok := value.(map[string]any)
	data, dataOK := envelope["data"].(map[string]any)
	if !ok || !dataOK || envelope["success"] != true || stringValue(data["version"]) == "" || stringValue(data["system_name"]) == "" {
		return
	}
	detector.addEvidence(result, scores, observedAt, Evidence{
		ID: "status-envelope", Source: "api_shape", Signal: "/api/status response",
		ObservedValue: "success + data.version + data.system_name", Matched: true, Weight: 40,
	}, "new_api", "one_api")
	if hasKeys(data, "quota_display_type", "enable_batch_update", "self_use_mode_enabled") {
		detector.addEvidence(result, scores, observedAt, Evidence{
			ID: "new-api-status-shape", Source: "api_shape", Signal: "New API status extensions",
			ObservedValue: "quota display and batch-update fields", Matched: true, Weight: 45,
		}, "new_api")
	}
	if hasKeys(data, "lark_client_id", "top_up_link", "chat_link") {
		detector.addEvidence(result, scores, observedAt, Evidence{
			ID: "one-api-status-shape", Source: "api_shape", Signal: "One API status fields",
			ObservedValue: "Lark, top-up, and chat-link fields", Matched: true, Weight: 45,
		}, "one_api")
	}
	switch stringValue(data["system_name"]) {
	case "New API":
		detector.addEvidence(result, scores, observedAt, Evidence{
			ID: "new-api-default-name", Source: "api_shape", Signal: "default system name",
			ObservedValue: "New API default", Matched: true, Weight: 20,
		}, "new_api")
	case "One API":
		detector.addEvidence(result, scores, observedAt, Evidence{
			ID: "one-api-default-name", Source: "api_shape", Signal: "default system name",
			ObservedValue: "One API default", Matched: true, Weight: 20,
		}, "one_api")
	}
}

func (detector *Detector) probeSub2API(
	ctx context.Context,
	base *url.URL,
	observedAt int64,
	scores map[string]*scoredCandidate,
	result *Result,
) {
	value, ok := detector.getJSON(ctx, base, "sub2api-public-settings", "/api/v1/settings/public", observedAt, result)
	if !ok {
		return
	}
	envelope, ok := value.(map[string]any)
	data, dataOK := envelope["data"].(map[string]any)
	if !ok || !dataOK || stringValue(envelope["code"]) != "0" || stringValue(data["version"]) == "" || stringValue(data["site_name"]) == "" {
		return
	}
	detector.addEvidence(result, scores, observedAt, Evidence{
		ID: "sub2api-settings-envelope", Source: "well_known", Signal: "/api/v1/settings/public response",
		ObservedValue: "code=0 + data.version + data.site_name", Matched: true, Weight: 55,
	}, "sub2api")
	if hasKeys(data, "server_timezone", "server_utc_offset", "table_page_size_options") {
		detector.addEvidence(result, scores, observedAt, Evidence{
			ID: "sub2api-settings-shape", Source: "api_shape", Signal: "Sub2API public settings fields",
			ObservedValue: "timezone and table pagination fields", Matched: true, Weight: 35,
		}, "sub2api")
	}
}

func (detector *Detector) getJSON(
	ctx context.Context,
	base *url.URL,
	probeID, path string,
	observedAt int64,
	result *Result,
) (any, bool) {
	requestURL := *base
	requestURL.Path = strings.TrimRight(requestURL.Path, "/") + path
	requestURL.RawPath = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		detector.addProbeError(result, probeID, path, "request_failed", 0, observedAt)
		return nil, false
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "JieShan/vnext-platform-detector")
	response, err := detector.client.Do(request)
	if err != nil {
		detector.addProbeError(result, probeID, path, "request_failed", 0, observedAt)
		return nil, false
	}
	if response == nil || response.Body == nil {
		detector.addProbeError(result, probeID, path, "empty_response", 0, observedAt)
		return nil, false
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxProbeBody+1))
	if err != nil {
		detector.addProbeError(result, probeID, path, "read_failed", response.StatusCode, observedAt)
		return nil, false
	}
	if len(body) > maxProbeBody {
		detector.addProbeError(result, probeID, path, "body_too_large", response.StatusCode, observedAt)
		return nil, false
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		detector.addProbeError(result, probeID, path, "http_status", response.StatusCode, observedAt)
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		detector.addProbeError(result, probeID, path, "invalid_json", response.StatusCode, observedAt)
		return nil, false
	}
	return value, true
}

func (detector *Detector) addProbeError(result *Result, probeID, path, code string, status int, observedAt int64) {
	result.Errors = append(result.Errors, ProbeError{
		ProbeID: probeID, Path: path, Code: code, Status: status,
		Message: "platform probe did not return usable evidence", ObservedAt: observedAt,
	})
}

func (detector *Detector) addEvidence(
	result *Result,
	scores map[string]*scoredCandidate,
	observedAt int64,
	evidence Evidence,
	platforms ...string,
) {
	evidence.ObservedAt = observedAt
	result.Evidence = append(result.Evidence, evidence)
	for _, platform := range platforms {
		candidate := scores[platform]
		if candidate == nil {
			candidate = &scoredCandidate{}
			scores[platform] = candidate
		}
		candidate.score += evidence.Weight
		if candidate.score > 100 {
			candidate.score = 100
		}
		candidate.evidenceIDs = append(candidate.evidenceIDs, evidence.ID)
	}
}

func (detector *Detector) candidates(scores map[string]*scoredCandidate) []Candidate {
	result := make([]Candidate, 0, len(scores))
	for platform, scored := range scores {
		label := platformLabel(platform)
		supported, capabilities := detector.support(platform)
		result = append(result, Candidate{
			Platform: platform, Label: label, Confidence: confidence(scored.score), Score: scored.score,
			Supported: supported, Capabilities: capabilities, EvidenceIDs: append([]string(nil), scored.evidenceIDs...),
		})
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Score != result[j].Score {
			return result[i].Score > result[j].Score
		}
		return platformOrder(result[i].Platform) < platformOrder(result[j].Platform)
	})
	return result
}

func (detector *Detector) selectResult(result *Result, manual *ManualSelection) {
	if manual != nil && manual.Locked && strings.TrimSpace(manual.Platform) != "" {
		platform := strings.ToLower(strings.TrimSpace(manual.Platform))
		supported, capabilities := detector.support(platform)
		evidenceID := "manual-selection"
		result.Evidence = append(result.Evidence, Evidence{
			ID: evidenceID, Source: "manual", Signal: "locked adapter selection", ObservedValue: platform,
			Matched: true, Weight: 100, ObservedAt: result.CheckedAt,
		})
		found := false
		for index := range result.Candidates {
			if result.Candidates[index].Platform == platform {
				manualCandidate := result.Candidates[index]
				manualCandidate.Confidence = "high"
				manualCandidate.Score = 100
				manualCandidate.Supported = supported
				manualCandidate.Capabilities = capabilities
				manualCandidate.EvidenceIDs = append(manualCandidate.EvidenceIDs, evidenceID)
				result.Candidates = append([]Candidate{manualCandidate}, append(result.Candidates[:index], result.Candidates[index+1:]...)...)
				found = true
				break
			}
		}
		if !found {
			result.Candidates = append([]Candidate{{
				Platform: platform, Label: platformLabel(platform), Confidence: "high", Score: 100,
				Supported: supported, Capabilities: capabilities, EvidenceIDs: []string{evidenceID},
			}}, result.Candidates...)
		}
		detectedAt := result.CheckedAt
		result.State, result.Verdict = "manual", "trusted"
		result.SelectedPlatform, result.SelectedPlatformLabel = platform, platformLabel(platform)
		result.SelectionLocked, result.Confidence, result.Score = true, "high", 100
		result.Capabilities, result.DetectedAt = capabilities, &detectedAt
		return
	}
	if len(result.Candidates) == 0 {
		return
	}
	top := result.Candidates[0]
	margin := top.Score
	if len(result.Candidates) > 1 {
		margin -= result.Candidates[1].Score
	}
	result.SelectedPlatform, result.SelectedPlatformLabel = top.Platform, top.Label
	result.Confidence, result.Score, result.Capabilities = top.Confidence, top.Score, top.Capabilities
	if top.Score >= 80 && margin >= 15 {
		result.State, result.Verdict = "detected", "trusted"
		return
	}
	result.State, result.Verdict = "ambiguous", "possible"
}

func (detector *Detector) support(platform string) (bool, Capabilities) {
	if detector.lookup == nil {
		return false, Capabilities{}
	}
	adapter, err := detector.lookup.Lookup(platform)
	if err != nil || adapter == nil {
		return false, Capabilities{}
	}
	capabilities := adapter.Capabilities()
	return true, Capabilities{
		SessionRefresh: capabilities.SessionRefresh,
		Balance:        capabilities.Balance,
		Usage:          capabilities.Usage,
	}
}

func probeOrigin(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("invalid platform detection origin")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("invalid platform detection origin")
	}
	return parsed, nil
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	default:
		return ""
	}
}

func hasKeys(values map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := values[key]; !ok {
			return false
		}
	}
	return true
}

func confidence(score int) string {
	switch {
	case score >= 80:
		return "high"
	case score >= 50:
		return "medium"
	case score > 0:
		return "low"
	default:
		return "unknown"
	}
}

func platformLabel(platform string) string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "new_api":
		return "New API"
	case "one_api":
		return "One API"
	case "sub2api":
		return "Sub2API"
	case "ciii":
		return "Ciii"
	default:
		return strings.TrimSpace(platform)
	}
}

func platformOrder(platform string) int {
	switch platform {
	case "new_api":
		return 0
	case "one_api":
		return 1
	case "sub2api":
		return 2
	default:
		return 100
	}
}
