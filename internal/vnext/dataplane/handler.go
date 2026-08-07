package dataplane

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/LuTianTian001/JieShan/internal/vnext/capacity"
	"github.com/LuTianTian001/JieShan/internal/vnext/gateway"
	"github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	"github.com/LuTianTian001/JieShan/internal/vnext/resolver"
)

const maxRequestBodyBytes = int64(8 << 20)

type Executor interface {
	Execute(context.Context, gateway.Input, gateway.StreamSink) (gateway.Result, error)
}

type ModelResolver interface {
	ListModels(context.Context, string, protocol.Protocol) ([]resolver.Model, error)
}

type Handler struct {
	executor Executor
	models   ModelResolver
}

func New(executor Executor, models ModelResolver) (*Handler, error) {
	if executor == nil || models == nil {
		return nil, errors.New("dataplane executor and model resolver are required")
	}
	return &Handler{executor: executor, models: models}, nil
}

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	switch {
	case request.URL.Path == "/v1/models":
		wire, style := modelListProtocol(request)
		handler.handleModels(writer, request, wire, style)
	case request.URL.Path == "/v1/chat/completions":
		handler.handleModelBody(writer, request, protocol.OpenAI, protocol.OpenAIChatCompletions, openAIErrorStyle)
	case request.URL.Path == "/v1/responses":
		handler.handleModelBody(writer, request, protocol.OpenAI, protocol.OpenAIResponses, openAIErrorStyle)
	case request.URL.Path == "/v1/messages":
		handler.handleModelBody(writer, request, protocol.Anthropic, protocol.AnthropicMessages, anthropicErrorStyle)
	case request.URL.Path == "/v1beta/models":
		handler.handleGeminiModels(writer, request)
	case strings.HasPrefix(request.URL.Path, "/v1beta/models/"):
		handler.handleGeminiGenerateContent(writer, request)
	default:
		writeProtocolError(writer, openAIErrorStyle, http.StatusNotFound, "not_found", "API endpoint was not found")
	}
}

func (handler *Handler) handleModels(
	writer http.ResponseWriter,
	request *http.Request,
	wire protocol.Protocol,
	style errorStyle,
) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, style, http.MethodGet)
		return
	}
	key, ok := downstreamKey(writer, request, wire, style)
	if !ok {
		return
	}
	models, err := handler.models.ListModels(request.Context(), key, wire)
	if err != nil {
		writeRuntimeError(writer, style, err)
		return
	}
	if wire == protocol.Anthropic {
		handler.writeAnthropicModels(writer, models)
		return
	}
	type modelItem struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	items := make([]modelItem, 0, len(models))
	for _, model := range models {
		items = append(items, modelItem{ID: model.ID, Object: "model", OwnedBy: "jieshan"})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"object": "list", "data": items})
}

func (handler *Handler) writeAnthropicModels(writer http.ResponseWriter, models []resolver.Model) {
	type modelItem struct {
		ID          string `json:"id"`
		Type        string `json:"type"`
		DisplayName string `json:"display_name"`
	}
	items := make([]modelItem, 0, len(models))
	for _, model := range models {
		items = append(items, modelItem{ID: model.ID, Type: "model", DisplayName: model.ID})
	}
	firstID, lastID := "", ""
	if len(items) > 0 {
		firstID = items[0].ID
		lastID = items[len(items)-1].ID
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"data": items, "has_more": false, "first_id": firstID, "last_id": lastID,
	})
}

func (handler *Handler) handleGeminiModels(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, geminiErrorStyle, http.MethodGet)
		return
	}
	key, ok := downstreamKey(writer, request, protocol.Gemini, geminiErrorStyle)
	if !ok {
		return
	}
	models, err := handler.models.ListModels(request.Context(), key, protocol.Gemini)
	if err != nil {
		writeRuntimeError(writer, geminiErrorStyle, err)
		return
	}
	type modelItem struct {
		Name                       string   `json:"name"`
		DisplayName                string   `json:"displayName"`
		SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
	}
	items := make([]modelItem, 0, len(models))
	for _, model := range models {
		name := strings.TrimSpace(model.ID)
		if !strings.HasPrefix(name, "models/") {
			name = "models/" + name
		}
		items = append(items, modelItem{
			Name: name, DisplayName: strings.TrimPrefix(name, "models/"),
			SupportedGenerationMethods: []string{"generateContent"},
		})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"models": items})
}

func (handler *Handler) handleModelBody(
	writer http.ResponseWriter,
	request *http.Request,
	wire protocol.Protocol,
	surface protocol.Surface,
	style errorStyle,
) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, style, http.MethodPost)
		return
	}
	key, ok := downstreamKey(writer, request, wire, style)
	if !ok {
		return
	}
	if contentType := strings.ToLower(strings.TrimSpace(strings.Split(request.Header.Get("Content-Type"), ";")[0])); contentType != "application/json" {
		writeProtocolError(writer, style, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return
	}
	body, err := readRequestBody(request.Body)
	if err != nil {
		writeProtocolError(writer, style, http.StatusBadRequest, "invalid_request", "Request body is invalid or too large")
		return
	}
	var envelope struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := decodeSingleJSON(body, &envelope); err != nil || strings.TrimSpace(envelope.Model) == "" {
		writeProtocolError(writer, style, http.StatusBadRequest, "invalid_request", "Request must contain a model")
		return
	}
	handler.execute(writer, request, key, strings.TrimSpace(envelope.Model), body, envelope.Stream, wire, surface, style)
}

func (handler *Handler) handleGeminiGenerateContent(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, geminiErrorStyle, http.MethodPost)
		return
	}
	model, stream, ok := geminiPathModel(request.URL.Path)
	if !ok {
		writeProtocolError(writer, geminiErrorStyle, http.StatusNotFound, "not_found", "Gemini API endpoint was not found")
		return
	}
	key, ok := downstreamKey(writer, request, protocol.Gemini, geminiErrorStyle)
	if !ok {
		return
	}
	if contentType := strings.ToLower(strings.TrimSpace(strings.Split(request.Header.Get("Content-Type"), ";")[0])); contentType != "application/json" {
		writeProtocolError(writer, geminiErrorStyle, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return
	}
	body, err := readRequestBody(request.Body)
	if err != nil {
		writeProtocolError(writer, geminiErrorStyle, http.StatusBadRequest, "invalid_request", "Request body is invalid or too large")
		return
	}
	var payload map[string]json.RawMessage
	if err := decodeSingleJSON(body, &payload); err != nil || payload == nil {
		writeProtocolError(writer, geminiErrorStyle, http.StatusBadRequest, "invalid_request", "Request body must be one JSON object")
		return
	}
	payload["stream"], _ = json.Marshal(stream)
	body, err = json.Marshal(payload)
	if err != nil || int64(len(body)) > maxRequestBodyBytes {
		writeProtocolError(writer, geminiErrorStyle, http.StatusBadRequest, "invalid_request", "Request body is invalid or too large")
		return
	}
	handler.execute(writer, request, key, model, body, stream, protocol.Gemini, protocol.GeminiGenerateContent, geminiErrorStyle)
}

func (handler *Handler) execute(
	writer http.ResponseWriter,
	request *http.Request,
	key, publicModel string,
	body []byte,
	stream bool,
	wire protocol.Protocol,
	surface protocol.Surface,
	style errorStyle,
) {
	requestID := requestID(request.Header.Get("X-Request-Id"))
	input := gateway.Input{
		RequestID: requestID, DownstreamKey: key, PublicModel: publicModel,
		IngressProtocol: wire, IngressSurface: surface, Payload: body, Stream: stream,
	}
	var sink gateway.StreamSink
	var responseSink *responseStreamSink
	if stream {
		responseSink = &responseStreamSink{writer: writer, requestID: requestID}
		sink = responseSink
	}
	result, err := handler.executor.Execute(request.Context(), input, sink)
	if err != nil {
		if responseSink != nil && responseSink.committed {
			return
		}
		writeRuntimeError(writer, style, err)
		return
	}
	if stream {
		return
	}
	copyHeaders(writer.Header(), result.Header)
	writer.Header().Set("X-JieShan-Request-Id", result.RequestID)
	status := result.StatusCode
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		status = http.StatusOK
	}
	writer.WriteHeader(status)
	_, _ = writer.Write(result.Body)
}

func geminiPathModel(path string) (string, bool, bool) {
	const prefix = "/v1beta/models/"
	if !strings.HasPrefix(path, prefix) {
		return "", false, false
	}
	remainder := strings.TrimPrefix(path, prefix)
	actionAt := strings.LastIndexByte(remainder, ':')
	if actionAt <= 0 {
		return "", false, false
	}
	rawModel, action := remainder[:actionAt], remainder[actionAt+1:]
	stream := false
	switch action {
	case "generateContent":
	case "streamGenerateContent":
		stream = true
	default:
		return "", false, false
	}
	model, err := url.PathUnescape(rawModel)
	model = strings.TrimSpace(model)
	if err != nil || model == "" || len(model) > 256 || strings.ContainsAny(model, "\x00\r\n?#") ||
		strings.HasPrefix(model, "/") || strings.HasSuffix(model, "/") {
		return "", false, false
	}
	return model, stream, true
}

type responseStreamSink struct {
	writer    http.ResponseWriter
	requestID string
	committed bool
}

func (sink *responseStreamSink) Commit(header http.Header) error {
	if sink.committed {
		return errors.New("stream response was already committed")
	}
	copyHeaders(sink.writer.Header(), header)
	sink.writer.Header().Set("X-JieShan-Request-Id", sink.requestID)
	sink.writer.Header().Set("X-Accel-Buffering", "no")
	sink.writer.WriteHeader(http.StatusOK)
	sink.committed = true
	if flusher, ok := sink.writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func (sink *responseStreamSink) Write(body []byte) error {
	if !sink.committed {
		return errors.New("stream response is not committed")
	}
	if _, err := sink.writer.Write(body); err != nil {
		return err
	}
	if flusher, ok := sink.writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func modelListProtocol(request *http.Request) (protocol.Protocol, errorStyle) {
	if len(request.Header.Values("x-api-key")) > 0 || strings.TrimSpace(request.Header.Get("anthropic-version")) != "" {
		return protocol.Anthropic, anthropicErrorStyle
	}
	return protocol.OpenAI, openAIErrorStyle
}

func downstreamKey(
	writer http.ResponseWriter,
	request *http.Request,
	wire protocol.Protocol,
	style errorStyle,
) (string, bool) {
	candidates := make([]string, 0, 2)
	invalid := false
	addHeader := func(name string) {
		values := request.Header.Values(name)
		if len(values) == 0 {
			return
		}
		if len(values) != 1 || strings.TrimSpace(values[0]) == "" || strings.ContainsAny(strings.TrimSpace(values[0]), " \t\r\n") {
			invalid = true
			return
		}
		candidates = append(candidates, strings.TrimSpace(values[0]))
	}
	addBearer := func() {
		values := request.Header.Values("Authorization")
		if len(values) == 0 {
			return
		}
		if len(values) != 1 {
			invalid = true
			return
		}
		parts := strings.Fields(values[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			invalid = true
			return
		}
		candidates = append(candidates, parts[1])
	}

	switch wire {
	case protocol.OpenAI:
		addBearer()
	case protocol.Anthropic:
		addHeader("x-api-key")
		addBearer()
	case protocol.Gemini:
		addHeader("x-goog-api-key")
		values, exists := request.URL.Query()["key"]
		if exists {
			if len(values) != 1 || strings.TrimSpace(values[0]) == "" || strings.ContainsAny(strings.TrimSpace(values[0]), " \t\r\n") {
				invalid = true
			} else {
				candidates = append(candidates, strings.TrimSpace(values[0]))
			}
		}
		addBearer()
	default:
		invalid = true
	}
	if invalid || len(candidates) != 1 || !strings.HasPrefix(candidates[0], "js_") {
		writeProtocolError(writer, style, http.StatusUnauthorized, "invalid_api_key", "A single valid JieShan API key is required")
		return "", false
	}
	return candidates[0], true
}

func readRequestBody(body io.ReadCloser) ([]byte, error) {
	if body == nil {
		return nil, io.ErrUnexpectedEOF
	}
	defer body.Close()
	value, err := io.ReadAll(io.LimitReader(body, maxRequestBodyBytes+1))
	if err != nil || len(value) == 0 || int64(len(value)) > maxRequestBodyBytes {
		return nil, errors.New("invalid request body")
	}
	return value, nil
}

func decodeSingleJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(raw)))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func writeRuntimeError(writer http.ResponseWriter, style errorStyle, err error) {
	switch {
	case errors.Is(err, resolver.ErrInvalidKey):
		writeProtocolError(writer, style, http.StatusUnauthorized, "invalid_api_key", "API key is invalid or unavailable")
	case errors.Is(err, resolver.ErrModelNotFound):
		writeProtocolError(writer, style, http.StatusNotFound, "model_not_found", "Model is not configured for this API key")
	case errors.Is(err, resolver.ErrNoRoutableTargets):
		writeProtocolError(writer, style, http.StatusServiceUnavailable, "model_unavailable", "Model has no compatible upstream target")
	case errors.Is(err, gateway.ErrInvalidRequest):
		writeProtocolError(writer, style, http.StatusBadRequest, "invalid_request", "Request is not valid for this API surface")
	case errors.Is(err, gateway.ErrQuotaExceeded):
		writeProtocolError(writer, style, http.StatusTooManyRequests, "insufficient_quota", "API key quota is exhausted or cannot admit this request")
	case errors.Is(err, gateway.ErrPricingUnavailable):
		writeProtocolError(writer, style, http.StatusServiceUnavailable, "pricing_unavailable", "Official pricing is unavailable for this model")
	case errors.Is(err, gateway.ErrRequestAlreadyStarted):
		writeProtocolError(writer, style, http.StatusConflict, "request_conflict", "Request ID has already been used")
	case errors.Is(err, capacity.ErrUpstreamBusy):
		writeProtocolError(writer, style, http.StatusServiceUnavailable, capacity.UpstreamBusyCode, "Upstream capacity is currently busy")
	case errors.Is(err, gateway.ErrNoAvailableUpstream):
		writeProtocolError(writer, style, http.StatusServiceUnavailable, "upstream_unavailable", "No upstream target is currently available")
	case errors.Is(err, gateway.ErrRequestTimeout), errors.Is(err, gateway.ErrFirstOutputTimeout), errors.Is(err, gateway.ErrStreamIdleTimeout):
		writeProtocolError(writer, style, http.StatusGatewayTimeout, "upstream_timeout", "Upstream request exceeded the configured timeout")
	case errors.Is(err, gateway.ErrDownstreamClosed), errors.Is(err, context.Canceled):
		return
	default:
		writeProtocolError(writer, style, http.StatusServiceUnavailable, "gateway_unavailable", "Gateway could not complete the request")
	}
}

func copyHeaders(destination, source http.Header) {
	for name, values := range source {
		destination.Del(name)
		for _, value := range values {
			if !strings.ContainsAny(value, "\r\n") {
				destination.Add(name, value)
			}
		}
	}
}

func methodNotAllowed(writer http.ResponseWriter, style errorStyle, methods ...string) {
	writer.Header().Set("Allow", strings.Join(methods, ", "))
	writeProtocolError(writer, style, http.StatusMethodNotAllowed, "method_not_allowed", "HTTP method is not allowed")
}

type errorStyle uint8

const (
	openAIErrorStyle errorStyle = iota
	anthropicErrorStyle
	geminiErrorStyle
)

func writeProtocolError(writer http.ResponseWriter, style errorStyle, status int, code, message string) {
	switch style {
	case anthropicErrorStyle:
		writeJSON(writer, status, map[string]any{
			"type":  "error",
			"error": map[string]string{"type": anthropicErrorType(status), "message": message, "code": code},
		})
	case geminiErrorStyle:
		writeJSON(writer, status, map[string]any{"error": map[string]any{
			"code": status, "message": message, "status": geminiErrorStatus(status),
		}})
	default:
		writeJSON(writer, status, map[string]any{"error": map[string]string{
			"message": message, "type": "jieshan_error", "code": code,
		}})
	}
}

func anthropicErrorType(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusUnsupportedMediaType, http.StatusMethodNotAllowed:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	default:
		return "api_error"
	}
}

func geminiErrorStatus(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusUnsupportedMediaType, http.StatusMethodNotAllowed:
		return "INVALID_ARGUMENT"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	default:
		return "UNAVAILABLE"
	}
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

var fallbackRequestID atomic.Uint64

func requestID(value string) string {
	value = strings.TrimSpace(value)
	if value != "" && len(value) <= 128 && !strings.ContainsAny(value, "\r\n") {
		return value
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err == nil {
		return "req_" + hex.EncodeToString(random)
	}
	return "req_fallback_" + strconv.FormatUint(fallbackRequestID.Add(1), 10)
}
