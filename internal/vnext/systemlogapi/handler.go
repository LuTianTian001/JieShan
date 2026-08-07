package systemlogapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/LuTianTian001/JieShan/internal/vnext/systemlog"
)

const APIPrefix = "/api/vnext/system-logs"

type Source interface {
	List(systemlog.Filter) (systemlog.Page, error)
}

type Handler struct {
	source Source
}

func New(source Source) (*Handler, error) {
	if source == nil {
		return nil, errors.New("system log source is required")
	}
	return &Handler{source: source}, nil
}

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if request.URL.Path != APIPrefix {
		writeError(writer, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	filter, err := parseFilter(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	page, err := handler.source.List(filter)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error", "system logs could not be loaded")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"items": page.Items, "hasMore": page.HasMore, "nextBefore": page.NextBefore,
	})
}

func parseFilter(request *http.Request) (systemlog.Filter, error) {
	values := request.URL.Query()
	filter := systemlog.Filter{
		Level: strings.TrimSpace(values.Get("level")), Module: strings.TrimSpace(values.Get("module")),
		Search: strings.TrimSpace(values.Get("search")), RequestID: strings.TrimSpace(values.Get("requestId")),
		TaskID: strings.TrimSpace(values.Get("taskId")), Limit: 50,
	}
	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil {
			return systemlog.Filter{}, errors.New("limit must be an integer")
		}
		filter.Limit = limit
	}
	if filter.Limit < 1 || filter.Limit > 200 {
		return systemlog.Filter{}, errors.New("limit must be between 1 and 200")
	}
	if raw := strings.TrimSpace(values.Get("before")); raw != "" {
		before, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || before <= 0 {
			return systemlog.Filter{}, errors.New("before must be a positive system log cursor")
		}
		filter.Before = before
	}
	return filter, nil
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
