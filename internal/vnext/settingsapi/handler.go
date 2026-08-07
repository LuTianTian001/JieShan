package settingsapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/settings"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const (
	APIPrefix       = "/api/vnext/settings"
	maxRequestBytes = 16 << 10
)

type Service interface {
	Current() vnextstore.RuntimeSettings
	UpdateCAS(context.Context, int64, vnextstore.RuntimeSettingsWrite) (vnextstore.RuntimeSettings, error)
}

type Handler struct {
	service Service
}

func New(service Service) (*Handler, error) {
	if nilLike(service) {
		return nil, errors.New("runtime settings service is required")
	}
	return &Handler{service: service}, nil
}

func NewServiceHandler(service *settings.Service) (*Handler, error) {
	if service == nil {
		return nil, errors.New("runtime settings service is required")
	}
	return New(service)
}

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if handler == nil || handler.service == nil {
		writeError(writer, http.StatusServiceUnavailable, "settings_unavailable", "Runtime settings are unavailable.")
		return
	}
	if request.URL.Path != APIPrefix && request.URL.Path != APIPrefix+"/" {
		writeError(writer, http.StatusNotFound, "not_found", "Settings resource was not found.")
		return
	}
	switch request.Method {
	case http.MethodGet:
		handler.get(writer)
	case http.MethodPatch:
		handler.update(writer, request)
	default:
		writer.Header().Set("Allow", "GET, PATCH")
		writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "Method is not allowed for this settings resource.")
	}
}

func (handler *Handler) get(writer http.ResponseWriter) {
	record := handler.service.Current()
	if record.Revision <= 0 {
		writeError(writer, http.StatusServiceUnavailable, "settings_unavailable", "Runtime settings are unavailable.")
		return
	}
	writeRecord(writer, http.StatusOK, record)
}

func (handler *Handler) update(writer http.ResponseWriter, request *http.Request) {
	revision, ok := requireRevision(writer, request)
	if !ok {
		return
	}
	var body settingsRequest
	if err := decodeJSON(writer, request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	input, err := body.storeWrite()
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	record, err := handler.service.UpdateCAS(request.Context(), revision, input)
	if err != nil {
		switch {
		case errors.Is(err, vnextstore.ErrRevisionConflict):
			writeError(writer, http.StatusConflict, "revision_conflict", "Settings changed; refresh and try again.")
		default:
			if validationErr := vnextstore.ValidateRuntimeSettingsWrite(input); validationErr != nil {
				writeError(writer, http.StatusBadRequest, "invalid_request", validationErr.Error())
				return
			}
			writeError(writer, http.StatusInternalServerError, "internal_error", "Settings could not be saved.")
		}
		return
	}
	writeRecord(writer, http.StatusOK, record)
}

type settingsRequest struct {
	FailureThreshold     int   `json:"failureThreshold"`
	FailureWindowMS      int64 `json:"failureWindowMs"`
	CooldownMS           int64 `json:"cooldownMs"`
	ProbeIntervalMS      int64 `json:"probeIntervalMs"`
	FirstOutputTimeoutMS int64 `json:"firstOutputTimeoutMs"`
	StreamIdleTimeoutMS  int64 `json:"streamIdleTimeoutMs"`
	RequestTimeoutMS     int64 `json:"requestTimeoutMs"`
	MaxAttempts          int   `json:"maxAttempts"`
	LogRetentionDays     int   `json:"logRetentionDays"`
}

func (body settingsRequest) storeWrite() (vnextstore.RuntimeSettingsWrite, error) {
	failureWindow, err := milliseconds(body.FailureWindowMS, "failureWindowMs")
	if err != nil {
		return vnextstore.RuntimeSettingsWrite{}, err
	}
	cooldown, err := milliseconds(body.CooldownMS, "cooldownMs")
	if err != nil {
		return vnextstore.RuntimeSettingsWrite{}, err
	}
	probeInterval, err := milliseconds(body.ProbeIntervalMS, "probeIntervalMs")
	if err != nil {
		return vnextstore.RuntimeSettingsWrite{}, err
	}
	firstOutput, err := milliseconds(body.FirstOutputTimeoutMS, "firstOutputTimeoutMs")
	if err != nil {
		return vnextstore.RuntimeSettingsWrite{}, err
	}
	streamIdle, err := milliseconds(body.StreamIdleTimeoutMS, "streamIdleTimeoutMs")
	if err != nil {
		return vnextstore.RuntimeSettingsWrite{}, err
	}
	requestTimeout, err := milliseconds(body.RequestTimeoutMS, "requestTimeoutMs")
	if err != nil {
		return vnextstore.RuntimeSettingsWrite{}, err
	}
	input := vnextstore.RuntimeSettingsWrite{
		FailureThreshold: body.FailureThreshold, FailureWindow: failureWindow,
		Cooldown: cooldown, ProbeInterval: probeInterval, FirstOutputTimeout: firstOutput,
		StreamIdleTimeout: streamIdle, RequestTimeout: requestTimeout,
		MaxAttempts: body.MaxAttempts, LogRetentionDays: body.LogRetentionDays,
	}
	if err := vnextstore.ValidateRuntimeSettingsWrite(input); err != nil {
		return vnextstore.RuntimeSettingsWrite{}, err
	}
	return input, nil
}

type settingsResponse struct {
	FailureThreshold     int   `json:"failureThreshold"`
	FailureWindowMS      int64 `json:"failureWindowMs"`
	CooldownMS           int64 `json:"cooldownMs"`
	ProbeIntervalMS      int64 `json:"probeIntervalMs"`
	FirstOutputTimeoutMS int64 `json:"firstOutputTimeoutMs"`
	StreamIdleTimeoutMS  int64 `json:"streamIdleTimeoutMs"`
	RequestTimeoutMS     int64 `json:"requestTimeoutMs"`
	MaxAttempts          int   `json:"maxAttempts"`
	LogRetentionDays     int   `json:"logRetentionDays"`
	Revision             int64 `json:"revision"`
}

func newResponse(record vnextstore.RuntimeSettings) settingsResponse {
	return settingsResponse{
		FailureThreshold: record.FailureThreshold, FailureWindowMS: record.FailureWindow.Milliseconds(),
		CooldownMS: record.Cooldown.Milliseconds(), ProbeIntervalMS: record.ProbeInterval.Milliseconds(),
		FirstOutputTimeoutMS: record.FirstOutputTimeout.Milliseconds(),
		StreamIdleTimeoutMS:  record.StreamIdleTimeout.Milliseconds(),
		RequestTimeoutMS:     record.RequestTimeout.Milliseconds(), MaxAttempts: record.MaxAttempts,
		LogRetentionDays: record.LogRetentionDays, Revision: record.Revision,
	}
}

func milliseconds(value int64, name string) (time.Duration, error) {
	if value <= 0 || value > int64((1<<63-1)/time.Millisecond) {
		return 0, fmt.Errorf("%s must be a positive integer within range", name)
	}
	return time.Duration(value) * time.Millisecond, nil
}

func requireRevision(writer http.ResponseWriter, request *http.Request) (int64, bool) {
	raw := strings.TrimSpace(request.Header.Get("If-Match"))
	if raw == "" {
		writeError(writer, http.StatusPreconditionRequired, "precondition_required", "If-Match is required when saving settings.")
		return 0, false
	}
	if strings.Contains(raw, ",") || raw == "*" || strings.HasPrefix(raw, "W/") ||
		len(raw) < 3 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		writeError(writer, http.StatusBadRequest, "invalid_revision", "If-Match must contain one strong numeric revision.")
		return 0, false
	}
	revision, err := strconv.ParseInt(raw[1:len(raw)-1], 10, 64)
	if err != nil || revision <= 0 {
		writeError(writer, http.StatusBadRequest, "invalid_revision", "If-Match must contain one strong numeric revision.")
		return 0, false
	}
	return revision, true
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, destination any) error {
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(request.Header.Get("Content-Type"), ";")[0]))
	if contentType != "application/json" {
		return errors.New("Content-Type must be application/json")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, maxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("request body must be a valid JSON object with every settings field")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain exactly one JSON object")
	}
	return nil
}

func writeRecord(writer http.ResponseWriter, status int, record vnextstore.RuntimeSettings) {
	writer.Header().Set("ETag", strconv.Quote(strconv.FormatInt(record.Revision, 10)))
	writeJSON(writer, status, newResponse(record))
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

func nilLike(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var (
	_ http.Handler = (*Handler)(nil)
	_ Service      = (*settings.Service)(nil)
)
