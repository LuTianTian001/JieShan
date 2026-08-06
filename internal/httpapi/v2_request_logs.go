package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/LuTianTian001/JieShan/internal/store"
)

const (
	defaultRequestLogPageSize = 50
	maxRequestLogPageSize     = 200
)

func (s *Server) registerV2RequestLogRoutes() {
	s.mux.Handle("GET /api/v2/request-logs", s.admin(http.HandlerFunc(s.listRequestLogsV2)))
	s.mux.Handle("GET /api/v2/request-logs/summary", s.admin(http.HandlerFunc(s.summarizeRequestLogsV2)))
	s.mux.Handle("GET /api/v2/request-logs/{id}", s.admin(http.HandlerFunc(s.getRequestLogV2)))
}

func (s *Server) getRequestLogV2(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	item, attempts, err := s.store.GetRequestLog(r.Context(), strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, store.RequestLogDetail{RequestLog: item, Attempts: attempts})
}

func (s *Server) listRequestLogsV2(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	filter, err := parseRequestLogFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	limit, err := requestLogLimit(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	page, err := s.store.ListRequestLogsPage(r.Context(), filter, limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) summarizeRequestLogsV2(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	filter, err := parseRequestLogFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	summary, err := s.store.SummarizeRequestLogs(r.Context(), filter)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func parseRequestLogFilter(r *http.Request) (store.RequestLogFilter, error) {
	query := r.URL.Query()
	filter := store.RequestLogFilter{
		Status: strings.ToLower(strings.TrimSpace(query.Get("status"))),
		Model:  strings.TrimSpace(query.Get("model")),
	}
	if filter.Status == "all" {
		filter.Status = ""
	}

	siteID, err := optionalPositiveAliasInt64(r, "site", "siteId")
	if err != nil {
		return store.RequestLogFilter{}, err
	}
	filter.SiteID = siteID
	upstreamID, err := optionalPositiveAliasInt64(r, "upstream", "upstreamId")
	if err != nil {
		return store.RequestLogFilter{}, err
	}
	filter.UpstreamID = upstreamID
	downstreamKeyID, err := optionalPositiveAliasInt64(r, "downstreamKey", "downstreamKeyId")
	if err != nil {
		return store.RequestLogFilter{}, err
	}
	filter.DownstreamKeyID = downstreamKeyID
	filter.Stream, err = optionalQueryBool(r, "stream")
	if err != nil {
		return store.RequestLogFilter{}, err
	}
	filter.Switched, err = optionalQueryBool(r, "switched")
	if err != nil {
		return store.RequestLogFilter{}, err
	}

	beforeTimeRaw := strings.TrimSpace(query.Get("beforeTime"))
	filter.BeforeID = strings.TrimSpace(query.Get("beforeId"))
	if (beforeTimeRaw == "") != (filter.BeforeID == "") {
		return store.RequestLogFilter{}, errors.New("beforeTime and beforeId must be provided together")
	}
	if beforeTimeRaw != "" {
		beforeTime, parseErr := strconv.ParseInt(beforeTimeRaw, 10, 64)
		if parseErr != nil || beforeTime < 0 {
			return store.RequestLogFilter{}, errors.New("beforeTime must be a non-negative Unix timestamp in milliseconds")
		}
		filter.BeforeTime = &beforeTime
	}
	return filter, nil
}

func requestLogLimit(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return defaultRequestLogPageSize, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > maxRequestLogPageSize {
		return 0, errors.New("limit must be an integer between 1 and 200")
	}
	return limit, nil
}

func optionalPositiveAliasInt64(r *http.Request, names ...string) (*int64, error) {
	var selected *int64
	for _, name := range names {
		raw := strings.TrimSpace(r.URL.Query().Get(name))
		if raw == "" {
			continue
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			return nil, errors.New(strings.Join(names, "/") + " must be a positive integer")
		}
		if selected != nil && *selected != value {
			return nil, errors.New(strings.Join(names, "/") + " aliases must identify the same resource")
		}
		copy := value
		selected = &copy
	}
	return selected, nil
}

func optionalQueryBool(r *http.Request, name string) (*bool, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, errors.New(name + " must be true or false")
	}
	return &value, nil
}
