package logsapi

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const apiPrefix = "/api/vnext/request-logs"

type Repository interface {
	ListRequestLogs(context.Context, vnextstore.RequestLogFilter) (vnextstore.RequestLogPage, error)
	SummarizeRequestLogs(context.Context, vnextstore.RequestLogFilter) (vnextstore.RequestLogSummary, error)
	GetRequestLogDetail(context.Context, string) (vnextstore.RequestLogDetail, error)
}

type Handler struct {
	repository Repository
}

func New(repository Repository) (*Handler, error) {
	if repository == nil {
		return nil, errors.New("request log repository is required")
	}
	return &Handler{repository: repository}, nil
}

func NewStoreHandler(store *vnextstore.Store) (*Handler, error) {
	if store == nil {
		return nil, errors.New("VNext store is required")
	}
	return New(store)
}

func (handler *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	switch {
	case r.URL.Path == apiPrefix:
		handler.list(w, r)
	case r.URL.Path == apiPrefix+"/summary":
		handler.summary(w, r)
	case strings.HasPrefix(r.URL.Path, apiPrefix+"/"):
		requestID := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, apiPrefix+"/"))
		if requestID == "" || len(requestID) > 128 || strings.Contains(requestID, "/") {
			writeError(w, http.StatusBadRequest, "invalid_request", "request ID is invalid")
			return
		}
		handler.detail(w, r, requestID)
	default:
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	}
}

func (handler *Handler) list(w http.ResponseWriter, r *http.Request) {
	filter, err := parseFilter(r, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	page, err := handler.repository.ListRequestLogs(r.Context(), filter)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	items := make([]requestResponse, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, newRequestResponse(item))
	}
	nextCursor := ""
	if page.HasMore && page.NextBeforeStartedAt != nil {
		nextCursor = encodeCursor(*page.NextBeforeStartedAt, page.NextBeforeID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "hasMore": page.HasMore, "nextCursor": nextCursor,
	})
}

func (handler *Handler) summary(w http.ResponseWriter, r *http.Request) {
	filter, err := parseFilter(r, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	item, err := handler.repository.SummarizeRequestLogs(r.Context(), filter)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, summaryResponse{
		Requests: item.Requests, Succeeded: item.Succeeded, Failed: item.Failed, Cancelled: item.Cancelled,
		Running: item.Running, SuccessBasisPoints: item.SuccessBasisPoints,
		TotalChargedNanoUSD: item.TotalChargedNanoUSD, TotalOfficialNanoUSD: item.TotalOfficialNanoUSD,
		TotalAttempts: item.TotalAttempts, RequestsWithSwitches: item.RequestsWithSwitches,
		AverageDurationMS: cloneInt64(item.AverageDurationMS), P50DurationMS: cloneInt64(item.P50DurationMS),
		P95DurationMS: cloneInt64(item.P95DurationMS), P50FirstOutputMS: cloneInt64(item.P50FirstOutputMS),
		P95FirstOutputMS: cloneInt64(item.P95FirstOutputMS),
	})
}

func (handler *Handler) detail(w http.ResponseWriter, r *http.Request, requestID string) {
	item, err := handler.repository.GetRequestLogDetail(r.Context(), requestID)
	if err != nil {
		writeRepositoryError(w, err)
		return
	}
	attempts := make([]attemptResponse, 0, len(item.Attempts))
	for _, attempt := range item.Attempts {
		attempts = append(attempts, newAttemptResponse(attempt))
	}
	routeCandidates := make([]routeCandidateResponse, 0, len(item.RouteCandidates))
	for _, candidate := range item.RouteCandidates {
		routeCandidates = append(routeCandidates, newRouteCandidateResponse(candidate))
	}
	ledger := make([]ledgerResponse, 0, len(item.Ledger))
	for _, entry := range item.Ledger {
		ledger = append(ledger, ledgerResponse{
			ID: entry.ID, EventType: entry.EventType, ReservedDeltaNanoUSD: entry.ReservedDeltaNanoUSD,
			UsedDeltaNanoUSD: entry.UsedDeltaNanoUSD, PriceCatalogVersion: entry.PriceCatalogVersion,
			PriceSKU: entry.PriceSKU, CreatedAt: entry.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"request": newRequestResponse(item.Request), "routeCandidates": routeCandidates,
		"attempts": attempts, "ledger": ledger,
	})
}

type requestResponse struct {
	ID                          string           `json:"id"`
	DownstreamKeyID             int64            `json:"downstreamKeyId"`
	DownstreamKeyName           string           `json:"downstreamKeyName"`
	PublishedModelID            int64            `json:"publishedModelId"`
	PublishedModelRevision      int64            `json:"publishedModelRevision"`
	EffectiveRoutingProfileID   int64            `json:"effectiveRoutingProfileId"`
	EffectiveRoutingProfileName string           `json:"effectiveRoutingProfileName"`
	SourceRoutingProfileID      int64            `json:"sourceRoutingProfileId"`
	SourceRoutingProfileName    string           `json:"sourceRoutingProfileName"`
	RouteRevision               int64            `json:"routeRevision"`
	PublicModel                 string           `json:"publicModel"`
	APISurface                  string           `json:"apiSurface"`
	ReasoningEffort             string           `json:"reasoningEffort"`
	ThinkingBudgetTokens        *int64           `json:"thinkingBudgetTokens"`
	Stream                      bool             `json:"stream"`
	PriceCatalogVersion         string           `json:"priceCatalogVersion"`
	PriceSKU                    string           `json:"priceSku"`
	BillingMultiplierBPS        int              `json:"billingMultiplierBPS"`
	ReservationNanoUSD          int64            `json:"reservationNanoUsd"`
	Status                      string           `json:"status"`
	MeteringStatus              string           `json:"meteringStatus"`
	MeteringErrorCode           string           `json:"meteringErrorCode"`
	FinalAttemptIndex           *int             `json:"finalAttemptIndex"`
	HTTPStatus                  *int             `json:"httpStatus"`
	FirstOutputMS               *int64           `json:"firstOutputMs"`
	TotalDurationMS             *int64           `json:"totalDurationMs"`
	InputTokens                 *int64           `json:"inputTokens"`
	OutputTokens                *int64           `json:"outputTokens"`
	CacheReadTokens             *int64           `json:"cacheReadTokens"`
	CacheWriteTokens            *int64           `json:"cacheWriteTokens"`
	CacheWrite5MTokens          *int64           `json:"cacheWrite5mTokens"`
	CacheWrite1HTokens          *int64           `json:"cacheWrite1hTokens"`
	ReasoningTokens             *int64           `json:"reasoningTokens"`
	OfficialCostNanoUSD         int64            `json:"officialCostNanoUsd"`
	ChargedNanoUSD              int64            `json:"chargedNanoUsd"`
	QuotaCapped                 bool             `json:"quotaCapped"`
	ErrorCode                   string           `json:"errorCode"`
	StartedAt                   int64            `json:"startedAt"`
	FinishedAt                  *int64           `json:"finishedAt"`
	FinalAttempt                *attemptResponse `json:"finalAttempt"`
}

func newRequestResponse(item vnextstore.RequestLog) requestResponse {
	var finalAttempt *attemptResponse
	if item.FinalAttempt != nil {
		response := newAttemptResponse(*item.FinalAttempt)
		finalAttempt = &response
	}
	return requestResponse{
		ID: item.ID, DownstreamKeyID: item.DownstreamKeyID, DownstreamKeyName: item.DownstreamKeyName,
		PublishedModelID: item.PublishedModelID, PublishedModelRevision: item.PublishedModelRevision,
		EffectiveRoutingProfileID: item.EffectiveRoutingProfileID, EffectiveRoutingProfileName: item.EffectiveRoutingProfileName,
		SourceRoutingProfileID: item.SourceRoutingProfileID, SourceRoutingProfileName: item.SourceRoutingProfileName,
		RouteRevision: item.RouteRevision,
		PublicModel:   item.PublicModel, APISurface: item.APISurface, ReasoningEffort: item.ReasoningEffort,
		ThinkingBudgetTokens: cloneInt64(item.ThinkingBudgetTokens), Stream: item.Stream,
		PriceCatalogVersion: item.PriceCatalogVersion, PriceSKU: item.PriceSKU,
		BillingMultiplierBPS: item.BillingMultiplierBPS, ReservationNanoUSD: item.ReservationNanoUSD,
		Status: item.Status, MeteringStatus: item.MeteringStatus, MeteringErrorCode: item.MeteringErrorCode,
		FinalAttemptIndex: cloneInt(item.FinalAttemptIndex), HTTPStatus: cloneInt(item.HTTPStatus),
		FirstOutputMS: cloneInt64(item.FirstTokenMS), TotalDurationMS: cloneInt64(item.DurationMS),
		InputTokens: cloneInt64(item.InputTokens), OutputTokens: cloneInt64(item.OutputTokens),
		CacheReadTokens: cloneInt64(item.CacheReadTokens), CacheWriteTokens: cloneInt64(item.CacheWriteTokens),
		CacheWrite5MTokens: cloneInt64(item.CacheWrite5MTokens), CacheWrite1HTokens: cloneInt64(item.CacheWrite1HTokens),
		ReasoningTokens: cloneInt64(item.ReasoningTokens), OfficialCostNanoUSD: item.OfficialCostNanoUSD,
		ChargedNanoUSD: item.ChargedNanoUSD, QuotaCapped: item.QuotaCapped, ErrorCode: item.ErrorCode,
		StartedAt: item.StartedAt, FinishedAt: cloneInt64(item.FinishedAt), FinalAttempt: finalAttempt,
	}
}

type routeCredentialResponse struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Position     int    `json:"position"`
	RuntimeState string `json:"runtimeState"`
	CoolingUntil *int64 `json:"coolingUntil"`
}

type routeCandidateResponse struct {
	Position                     int                       `json:"position"`
	PublishedModelTargetID       int64                     `json:"publishedModelTargetId"`
	PublishedModelTargetRevision int64                     `json:"publishedModelTargetRevision"`
	ProviderModelTargetID        int64                     `json:"providerModelTargetId"`
	ProviderModelTargetRevision  int64                     `json:"providerModelTargetRevision"`
	SiteID                       int64                     `json:"siteId"`
	SiteName                     string                    `json:"siteName"`
	EndpointID                   int64                     `json:"endpointId"`
	EndpointName                 string                    `json:"endpointName"`
	SourceModel                  string                    `json:"sourceModel"`
	WireProtocol                 string                    `json:"wireProtocol"`
	APISurface                   string                    `json:"apiSurface"`
	Credentials                  []routeCredentialResponse `json:"credentials"`
	InitialEligibility           string                    `json:"initialEligibility"`
	InitialReason                string                    `json:"initialReason"`
	Disposition                  string                    `json:"disposition"`
	DispositionReason            string                    `json:"dispositionReason"`
	AttemptCount                 int                       `json:"attemptCount"`
	FirstAttemptIndex            *int                      `json:"firstAttemptIndex"`
	LastAttemptIndex             *int                      `json:"lastAttemptIndex"`
}

func newRouteCandidateResponse(item vnextstore.RequestRouteCandidate) routeCandidateResponse {
	credentials := make([]routeCredentialResponse, 0, len(item.Credentials))
	for _, credential := range item.Credentials {
		credentials = append(credentials, routeCredentialResponse{
			ID: credential.ID, Name: credential.Name, Position: credential.Position,
			RuntimeState: credential.RuntimeState, CoolingUntil: cloneInt64(credential.CoolingUntil),
		})
	}
	return routeCandidateResponse{
		Position: item.Position, PublishedModelTargetID: item.PublishedModelTargetID,
		PublishedModelTargetRevision: item.PublishedModelTargetRevision,
		ProviderModelTargetID:        item.ProviderModelTargetID, ProviderModelTargetRevision: item.ProviderModelTargetRevision,
		SiteID: item.SiteID, SiteName: item.SiteName, EndpointID: item.EndpointID, EndpointName: item.EndpointName,
		SourceModel: item.SourceModel, WireProtocol: item.WireProtocol, APISurface: item.APISurface,
		Credentials: credentials, InitialEligibility: item.InitialEligibility, InitialReason: item.InitialReason,
		Disposition: item.Disposition, DispositionReason: item.DispositionReason, AttemptCount: item.AttemptCount,
		FirstAttemptIndex: cloneInt(item.FirstAttemptIndex), LastAttemptIndex: cloneInt(item.LastAttemptIndex),
	}
}

type attemptResponse struct {
	ID                           int64  `json:"id"`
	AttemptIndex                 int    `json:"attemptIndex"`
	PublishedModelTargetID       int64  `json:"publishedModelTargetId"`
	PublishedModelTargetRevision int64  `json:"publishedModelTargetRevision"`
	ProviderModelTargetID        int64  `json:"providerModelTargetId"`
	ProviderModelTargetRevision  int64  `json:"providerModelTargetRevision"`
	SiteID                       int64  `json:"siteId"`
	SiteName                     string `json:"siteName"`
	EndpointID                   int64  `json:"endpointId"`
	EndpointName                 string `json:"endpointName"`
	CredentialID                 int64  `json:"credentialId"`
	CredentialName               string `json:"credentialName"`
	SourceModel                  string `json:"sourceModel"`
	ResponseModel                string `json:"responseModel"`
	WireProtocol                 string `json:"wireProtocol"`
	APISurface                   string `json:"apiSurface"`
	Status                       string `json:"status"`
	HTTPStatus                   *int   `json:"httpStatus"`
	FailureKind                  string `json:"failureKind"`
	ErrorCode                    string `json:"errorCode"`
	SwitchReason                 string `json:"switchReason"`
	FirstOutputMS                *int64 `json:"firstOutputMs"`
	DurationMS                   int64  `json:"durationMs"`
	StartedAt                    int64  `json:"startedAt"`
	FinishedAt                   int64  `json:"finishedAt"`
}

func newAttemptResponse(item vnextstore.RequestAttempt) attemptResponse {
	return attemptResponse{
		ID: item.ID, AttemptIndex: item.AttemptIndex, PublishedModelTargetID: item.PublishedModelTargetID,
		PublishedModelTargetRevision: item.PublishedModelTargetRevision,
		ProviderModelTargetID:        item.ProviderModelTargetID, ProviderModelTargetRevision: item.ProviderModelTargetRevision,
		SiteID: item.SiteID, SiteName: item.SiteName, EndpointID: item.EndpointID, EndpointName: item.EndpointName,
		CredentialID: item.CredentialID, CredentialName: item.CredentialName, SourceModel: item.SourceModel,
		ResponseModel: item.ResponseModel,
		WireProtocol:  item.WireProtocol, APISurface: item.APISurface, Status: item.Status,
		HTTPStatus: cloneInt(item.HTTPStatus), FailureKind: item.FailureKind, ErrorCode: item.ErrorCode,
		SwitchReason: item.SwitchReason, FirstOutputMS: cloneInt64(item.FirstTokenMS), DurationMS: item.DurationMS,
		StartedAt: item.StartedAt, FinishedAt: item.FinishedAt,
	}
}

type ledgerResponse struct {
	ID                   int64  `json:"id"`
	EventType            string `json:"eventType"`
	ReservedDeltaNanoUSD int64  `json:"reservedDeltaNanoUsd"`
	UsedDeltaNanoUSD     int64  `json:"usedDeltaNanoUsd"`
	PriceCatalogVersion  string `json:"priceCatalogVersion"`
	PriceSKU             string `json:"priceSku"`
	CreatedAt            int64  `json:"createdAt"`
}

type summaryResponse struct {
	Requests             int64  `json:"requests"`
	Succeeded            int64  `json:"succeeded"`
	Failed               int64  `json:"failed"`
	Cancelled            int64  `json:"cancelled"`
	Running              int64  `json:"running"`
	SuccessBasisPoints   int    `json:"successBasisPoints"`
	TotalChargedNanoUSD  int64  `json:"totalChargedNanoUsd"`
	TotalOfficialNanoUSD int64  `json:"totalOfficialNanoUsd"`
	TotalAttempts        int64  `json:"totalAttempts"`
	RequestsWithSwitches int64  `json:"requestsWithSwitches"`
	AverageDurationMS    *int64 `json:"averageDurationMs"`
	P50DurationMS        *int64 `json:"p50DurationMs"`
	P95DurationMS        *int64 `json:"p95DurationMs"`
	P50FirstOutputMS     *int64 `json:"p50FirstOutputMs"`
	P95FirstOutputMS     *int64 `json:"p95FirstOutputMs"`
}

func parseFilter(r *http.Request, includePage bool) (vnextstore.RequestLogFilter, error) {
	values := r.URL.Query()
	filter := vnextstore.RequestLogFilter{
		Limit: 50, PublicModel: strings.TrimSpace(values.Get("model")), APISurface: strings.TrimSpace(values.Get("surface")),
		Status: strings.TrimSpace(values.Get("status")), Search: strings.TrimSpace(values.Get("search")),
	}
	if includePage {
		if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
			limit, err := strconv.Atoi(raw)
			if err != nil {
				return vnextstore.RequestLogFilter{}, errors.New("limit must be an integer")
			}
			filter.Limit = limit
		}
		if cursor := strings.TrimSpace(values.Get("cursor")); cursor != "" {
			startedAt, id, err := decodeCursor(cursor)
			if err != nil {
				return vnextstore.RequestLogFilter{}, err
			}
			filter.BeforeStartedAt, filter.BeforeID = &startedAt, id
		}
	}
	for name, target := range map[string]**int64{
		"from": &filter.From, "to": &filter.To, "downstreamKeyId": &filter.DownstreamKeyID, "siteId": &filter.SiteID,
	} {
		raw := strings.TrimSpace(values.Get(name))
		if raw == "" {
			continue
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			return vnextstore.RequestLogFilter{}, fmt.Errorf("%s must be a positive integer", name)
		}
		copy := value
		*target = &copy
	}
	if filter.From != nil && filter.To != nil && *filter.To < *filter.From {
		return vnextstore.RequestLogFilter{}, errors.New("to cannot be before from")
	}
	if includePage && (filter.Limit < 1 || filter.Limit > 200) {
		return vnextstore.RequestLogFilter{}, errors.New("limit must be between 1 and 200")
	}
	return filter, nil
}

func encodeCursor(startedAt int64, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(startedAt, 10) + ":" + id))
}

func decodeCursor(raw string) (int64, string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0, "", errors.New("cursor is invalid")
	}
	timePart, id, ok := strings.Cut(string(decoded), ":")
	startedAt, timeErr := strconv.ParseInt(timePart, 10, 64)
	id = strings.TrimSpace(id)
	if !ok || timeErr != nil || startedAt < 0 || id == "" || len(id) > 128 {
		return 0, "", errors.New("cursor is invalid")
	}
	return startedAt, id, nil
}

func writeRepositoryError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "request log was not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "the request could not be completed")
}

func methodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
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

var _ Repository = (*vnextstore.Store)(nil)
