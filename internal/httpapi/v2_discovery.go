package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/LuTianTian001/JieShan/internal/store"
	"github.com/LuTianTian001/JieShan/internal/upstream"
	"github.com/google/uuid"
)

type discoveryPayloadV2 struct {
	EndpointID   int64  `json:"endpointId"`
	CredentialID int64  `json:"credentialId"`
	Strategy     string `json:"strategy"`
}

type credentialDiscoveryV2 struct {
	CredentialID   int64    `json:"credentialId"`
	CredentialName string   `json:"credentialName"`
	Models         []string `json:"models"`
	Complete       bool     `json:"complete"`
	PagesFetched   int      `json:"pagesFetched"`
	Error          string   `json:"error,omitempty"`
}

type discoveryResultV2 struct {
	SiteID       int64                    `json:"siteId"`
	EndpointID   int64                    `json:"endpointId"`
	Models       []string                 `json:"models"`
	Complete     bool                     `json:"complete"`
	Applied      bool                     `json:"applied"`
	Attempts     []credentialDiscoveryV2  `json:"attempts"`
	DiscoveredAt string                   `json:"discoveredAt"`
	Error        string                   `json:"error,omitempty"`
	Run          *store.ModelDiscoveryRun `json:"run,omitempty"`
}

type discoveryRunSummaryV2 struct {
	Outcome      string   `json:"outcome"`
	Applied      bool     `json:"applied"`
	Complete     bool     `json:"complete"`
	Models       []string `json:"models"`
	AttemptCount int      `json:"attemptCount"`
	FailureCount int      `json:"failureCount"`
}

func (s *Server) discoverSiteModelsV2(w http.ResponseWriter, r *http.Request) {
	siteID, ok := pathID(w, r)
	if !ok {
		return
	}
	var body discoveryPayloadV2
	if !decodeJSON(w, r, &body) {
		return
	}
	strategy, err := normalizeDiscoveryStrategy(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_discovery_strategy")
		return
	}
	site, err := s.store.GetSite(r.Context(), siteID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !site.Enabled {
		writeError(w, http.StatusConflict, "site is disabled", "site_disabled")
		return
	}
	endpoint, err := s.store.GetInferenceEndpoint(r.Context(), body.EndpointID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if endpoint.SiteID != siteID || !endpoint.Enabled {
		writeError(w, http.StatusBadRequest, "endpoint does not belong to the enabled site", "invalid_endpoint")
		return
	}
	credentials, err := s.discoveryCredentials(r, siteID, strategy, body.CredentialID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if len(credentials) == 0 {
		writeError(w, http.StatusConflict, "site has no enabled credentials", "no_credentials")
		return
	}

	startedAt := store.NowMS()
	runID := uuid.NewString()
	if err := s.store.InsertModelDiscoveryRun(r.Context(), store.ModelDiscoveryRun{
		ID: runID, SiteID: siteID, EndpointID: endpoint.ID, Mode: strategy,
		BaseSiteRevision: site.Revision, BaseEndpointRevision: endpoint.Revision,
		CredentialCount: len(credentials), StartedAt: startedAt,
	}); err != nil {
		writeStoreError(w, err)
		return
	}

	result := discoveryResultV2{
		SiteID: siteID, EndpointID: endpoint.ID, Models: []string{}, Complete: true,
		Attempts: []credentialDiscoveryV2{}, DiscoveredAt: iso(startedAt),
	}
	seen := make(map[string]struct{})
	successful := make([]store.ModelDiscoveryCredentialModels, 0, len(credentials))
	for index, credential := range credentials {
		attemptStartedAt := store.NowMS()
		attempt := credentialDiscoveryV2{
			CredentialID: credential.ID, CredentialName: credential.Name, Models: []string{},
		}
		attemptStatus := "failed"
		errorClass := ""
		secret, decryptErr := s.cipher.Decrypt(credential.SecretCipher)
		if decryptErr != nil {
			attempt.Error = "credential could not be decrypted"
			errorClass = "credential_decryption"
		} else {
			discovered, discoverErr := s.upstream.DiscoverEndpoint(r.Context(), upstream.EndpointDiscoveryInput{
				Protocol: endpoint.WireProtocol, BaseURL: endpoint.BaseURL, Secret: secret, CustomHeaders: endpoint.CustomHeaders,
			})
			attempt.Models = discovered.Models
			attempt.Complete = discovered.Complete
			attempt.PagesFetched = discovered.PagesFetched
			if discoverErr != nil {
				attempt.Error = discoverErr.Error()
				errorClass = classifyDiscoveryError(discoverErr)
			} else {
				attemptStatus = "success"
				models := make([]string, 0, len(discovered.Models))
				for _, name := range discovered.Models {
					name = strings.TrimSpace(name)
					if name == "" {
						continue
					}
					models = append(models, name)
					seen[name] = struct{}{}
				}
				successful = append(successful, store.ModelDiscoveryCredentialModels{
					CredentialID: credential.ID, Models: models,
				})
			}
		}
		if attemptStatus != "success" || !attempt.Complete {
			result.Complete = false
		}
		result.Attempts = append(result.Attempts, attempt)
		credentialID := credential.ID
		persistedAttempt := store.ModelDiscoveryAttempt{
			DiscoveryRunID: runID, AttemptIndex: index, InferenceCredentialID: &credentialID,
			CredentialName: credential.Name, Status: attemptStatus, ModelCount: len(attempt.Models),
			Complete: attempt.Complete, PagesFetched: attempt.PagesFetched, ErrorClass: errorClass,
			ErrorMessage: attempt.Error, StartedAt: attemptStartedAt, FinishedAt: store.NowMS(),
		}
		if _, err := s.store.InsertModelDiscoveryAttempt(r.Context(), persistedAttempt); err != nil {
			// A credential can be deleted while an outbound request is running. Keep
			// the immutable name snapshot even when its foreign key is no longer valid.
			if errors.Is(err, sql.ErrNoRows) {
				persistedAttempt.InferenceCredentialID = nil
				_, err = s.store.InsertModelDiscoveryAttempt(r.Context(), persistedAttempt)
			}
			if err != nil {
				result.Error = "could not persist discovery attempt"
				if finalizeErr := s.failDiscoveryRunV2(r, &result, runID, "attempt_persistence_failed", result.Error); finalizeErr != nil {
					writeStoreError(w, finalizeErr)
					return
				}
				writeJSON(w, http.StatusInternalServerError, result)
				return
			}
		}
		if strategy == "first_success" && attemptStatus == "success" {
			break
		}
	}

	for name := range seen {
		result.Models = append(result.Models, name)
	}
	sort.Strings(result.Models)
	if len(result.Models) == 0 {
		result.Complete = false
		result.Error = "no credential returned a complete non-empty model list"
		if err := s.failDiscoveryRunV2(r, &result, runID, "no_models", result.Error); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusBadGateway, result)
		return
	}

	if err := s.store.ApplyModelDiscoveryRun(r.Context(), runID, successful, result.Models, store.NowMS()); err != nil {
		status := http.StatusInternalServerError
		outcome := "apply_failed"
		result.Error = "model discovery could not be applied"
		if errors.Is(err, store.ErrRevisionConflict) {
			status = http.StatusConflict
			outcome = "configuration_conflict"
			result.Error = "site or endpoint configuration changed during discovery; existing catalog was preserved"
		}
		result.Complete = false
		if finalizeErr := s.failDiscoveryRunV2(r, &result, runID, outcome, result.Error); finalizeErr != nil {
			writeStoreError(w, finalizeErr)
			return
		}
		writeJSON(w, status, result)
		return
	}
	result.Applied = true
	summary := discoverySummaryV2(result, "applied")
	run, err := s.store.CompleteModelDiscoveryRun(r.Context(), runID, len(result.Models), summary, "", store.NowMS())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	result.Run = &run
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) listSiteModelDiscoveriesV2(w http.ResponseWriter, r *http.Request) {
	siteID, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := s.store.GetSite(r.Context(), siteID); err != nil {
		writeStoreError(w, err)
		return
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 200", "invalid_limit")
			return
		}
		limit = parsed
	}
	items, err := s.store.ListModelDiscoveryRuns(r.Context(), siteID, limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getModelDiscoveryRunV2(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" || len(id) > 128 {
		writeError(w, http.StatusBadRequest, "invalid discovery run id", "invalid_request")
		return
	}
	item, err := s.store.GetModelDiscoveryRun(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	attempts, err := s.store.ListModelDiscoveryAttempts(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"item": item, "attempts": attempts})
}

func (s *Server) listModelDiscoveryAttemptsV2(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" || len(id) > 128 {
		writeError(w, http.StatusBadRequest, "invalid discovery run id", "invalid_request")
		return
	}
	if _, err := s.store.GetModelDiscoveryRun(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	items, err := s.store.ListModelDiscoveryAttempts(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) discoveryCredentials(r *http.Request, siteID int64, strategy string, credentialID int64) ([]store.InferenceCredentialSecret, error) {
	if strategy == "selected" {
		item, err := s.store.GetInferenceCredentialSecret(r.Context(), credentialID)
		if err != nil {
			return nil, err
		}
		if item.SiteID != siteID || !item.Enabled {
			return nil, errors.New("credential does not belong to the enabled site")
		}
		return []store.InferenceCredentialSecret{item}, nil
	}
	items, err := s.store.ListInferenceCredentials(r.Context(), siteID)
	if err != nil {
		return nil, err
	}
	result := make([]store.InferenceCredentialSecret, 0, len(items))
	for _, item := range items {
		if !item.Enabled {
			continue
		}
		secret, err := s.store.GetInferenceCredentialSecret(r.Context(), item.ID)
		if err != nil {
			return nil, err
		}
		result = append(result, secret)
	}
	return result, nil
}

func normalizeDiscoveryStrategy(body discoveryPayloadV2) (string, error) {
	strategy := strings.ToLower(strings.TrimSpace(body.Strategy))
	if strategy == "" {
		if body.CredentialID > 0 {
			strategy = "selected"
		} else {
			strategy = "first_success"
		}
	}
	switch strategy {
	case "selected":
		if body.CredentialID <= 0 {
			return "", errors.New("selected discovery requires credentialId")
		}
	case "first_success", "all":
		if body.CredentialID > 0 {
			return "", errors.New("credentialId is only valid for selected discovery")
		}
	default:
		return "", errors.New("strategy must be selected, first_success, or all")
	}
	return strategy, nil
}

func (s *Server) failDiscoveryRunV2(r *http.Request, result *discoveryResultV2, runID, outcome, message string) error {
	run, err := s.store.FailModelDiscoveryRun(r.Context(), runID, len(result.Models), discoverySummaryV2(*result, outcome), message, store.NowMS())
	if err != nil {
		return err
	}
	result.Run = &run
	return nil
}

func discoverySummaryV2(result discoveryResultV2, outcome string) json.RawMessage {
	failures := 0
	for _, attempt := range result.Attempts {
		if attempt.Error != "" {
			failures++
		}
	}
	payload, _ := json.Marshal(discoveryRunSummaryV2{
		Outcome: outcome, Applied: result.Applied, Complete: result.Complete,
		Models: result.Models, AttemptCount: len(result.Attempts), FailureCount: failures,
	})
	return payload
}

func classifyDiscoveryError(err error) string {
	message := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, upstream.ErrEmptyModelList):
		return "empty_model_list"
	case strings.Contains(message, "deadline") || strings.Contains(message, "timeout"):
		return "timeout"
	case strings.Contains(message, "http 401") || strings.Contains(message, "http 403"):
		return "authentication"
	case strings.Contains(message, "http 429"):
		return "rate_limited"
	case strings.Contains(message, "html"):
		return "unexpected_html"
	case strings.Contains(message, "pagination"):
		return "pagination"
	case strings.Contains(message, "unsupported"):
		return "unsupported_protocol"
	default:
		return "upstream_error"
	}
}
