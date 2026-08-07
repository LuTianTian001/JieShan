package systemlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultMemoryEntries = 2_000
	defaultMaxFileBytes  = 32 << 20
	defaultMaxFiles      = 15
	defaultMaxAge        = 7 * 24 * time.Hour
	redactedValue        = "[REDACTED]"
)

type Event struct {
	Sequence  uint64         `json:"-"`
	ID        string         `json:"id"`
	Timestamp int64          `json:"timestamp"`
	Level     string         `json:"level"`
	Module    string         `json:"module"`
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"requestId,omitempty"`
	TaskID    string         `json:"taskId,omitempty"`
	Fields    map[string]any `json:"fields,omitempty"`
}

type Filter struct {
	Level     string
	Module    string
	Search    string
	RequestID string
	TaskID    string
	Before    uint64
	Limit     int
}

type Page struct {
	Items      []Event
	HasMore    bool
	NextBefore uint64
}

type Options struct {
	Path          string
	MemoryEntries int
	MaxFileBytes  int64
	MaxFiles      int
	MaxAge        time.Duration
	MinLevel      slog.Level
	Now           func() time.Time
}

type Recorder struct {
	state *sharedState
	attrs []slog.Attr
	group string
}

type sharedState struct {
	mu             sync.RWMutex
	events         []Event
	start          int
	count          int
	options        Options
	file           *os.File
	size           int64
	segmentStarted time.Time
	closed         bool
	sequence       atomic.Uint64
}

func New(options Options) (*Recorder, error) {
	options.Path = strings.TrimSpace(options.Path)
	if options.MemoryEntries <= 0 {
		options.MemoryEntries = defaultMemoryEntries
	}
	if options.MaxFileBytes <= 0 {
		options.MaxFileBytes = defaultMaxFileBytes
	}
	if options.MaxFiles <= 0 {
		options.MaxFiles = defaultMaxFiles
	}
	if options.MaxAge <= 0 {
		options.MaxAge = defaultMaxAge
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	state := &sharedState{events: make([]Event, options.MemoryEntries), options: options}
	if options.Path != "" {
		state.pruneExpired()
		if err := state.openFile(); err != nil {
			return nil, err
		}
	}
	return &Recorder{state: state}, nil
}

func (recorder *Recorder) Enabled(_ context.Context, level slog.Level) bool {
	return recorder != nil && recorder.state != nil && level >= recorder.state.options.MinLevel
}

func (recorder *Recorder) Handle(_ context.Context, record slog.Record) error {
	if recorder == nil || recorder.state == nil {
		return errors.New("system log recorder is unavailable")
	}
	fields := make(map[string]any, len(recorder.attrs)+record.NumAttrs())
	for _, attr := range recorder.attrs {
		appendAttribute(fields, recorder.group, attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		appendAttribute(fields, recorder.group, attr)
		return true
	})
	module := takeString(fields, "module")
	code := takeString(fields, "code")
	requestID := takeString(fields, "request_id")
	if requestID == "" {
		requestID = takeString(fields, "requestId")
	}
	taskID := takeString(fields, "task_id")
	if taskID == "" {
		taskID = takeString(fields, "taskId")
	}
	for key, value := range fields {
		fields[key] = redactValue(key, value)
	}
	now := record.Time
	if now.IsZero() {
		now = recorder.state.options.Now()
	}
	sequence := recorder.state.sequence.Add(1)
	event := Event{
		Sequence: sequence, ID: fmt.Sprintf("sys-%d-%d", now.UTC().UnixMilli(), sequence),
		Timestamp: now.UTC().UnixMilli(),
		Level:     strings.ToLower(record.Level.String()), Module: module, Code: code,
		Message: strings.TrimSpace(record.Message), RequestID: requestID, TaskID: taskID, Fields: fields,
	}
	return recorder.state.append(event)
}

func (recorder *Recorder) WithAttrs(attrs []slog.Attr) slog.Handler {
	if recorder == nil {
		return recorder
	}
	copy := *recorder
	copy.attrs = append(append([]slog.Attr(nil), recorder.attrs...), attrs...)
	return &copy
}

func (recorder *Recorder) WithGroup(name string) slog.Handler {
	if recorder == nil {
		return recorder
	}
	copy := *recorder
	name = strings.TrimSpace(name)
	if name != "" {
		if copy.group == "" {
			copy.group = name
		} else {
			copy.group += "." + name
		}
	}
	return &copy
}

func (recorder *Recorder) List(filter Filter) (Page, error) {
	if recorder == nil || recorder.state == nil {
		return Page{}, errors.New("system log recorder is unavailable")
	}
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	if filter.Limit < 1 || filter.Limit > 200 {
		return Page{}, errors.New("system log limit must be between 1 and 200")
	}
	filter.Level = strings.ToLower(strings.TrimSpace(filter.Level))
	filter.Module = strings.ToLower(strings.TrimSpace(filter.Module))
	filter.Search = strings.ToLower(strings.TrimSpace(filter.Search))
	filter.RequestID = strings.TrimSpace(filter.RequestID)
	filter.TaskID = strings.TrimSpace(filter.TaskID)

	recorder.state.mu.RLock()
	defer recorder.state.mu.RUnlock()
	items := make([]Event, 0, filter.Limit+1)
	for offset := recorder.state.count - 1; offset >= 0; offset-- {
		index := (recorder.state.start + offset) % len(recorder.state.events)
		event := recorder.state.events[index]
		if filter.Before > 0 && event.Sequence >= filter.Before {
			continue
		}
		if !matches(event, filter) {
			continue
		}
		items = append(items, cloneEvent(event))
		if len(items) > filter.Limit {
			break
		}
	}
	page := Page{Items: items}
	if len(items) > filter.Limit {
		page.HasMore = true
		page.Items = items[:filter.Limit]
		page.NextBefore = page.Items[len(page.Items)-1].Sequence
	}
	return page, nil
}

func (recorder *Recorder) Close() error {
	if recorder == nil || recorder.state == nil {
		return nil
	}
	recorder.state.mu.Lock()
	defer recorder.state.mu.Unlock()
	if recorder.state.closed {
		return nil
	}
	recorder.state.closed = true
	if recorder.state.file == nil {
		return nil
	}
	err := recorder.state.file.Close()
	recorder.state.file = nil
	return err
}

func (state *sharedState) append(event Event) error {
	line, err := json.Marshal(event)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return errors.New("system log recorder is closed")
	}
	if state.count < len(state.events) {
		index := (state.start + state.count) % len(state.events)
		state.events[index] = cloneEvent(event)
		state.count++
	} else {
		state.events[state.start] = cloneEvent(event)
		state.start = (state.start + 1) % len(state.events)
	}
	if state.file == nil {
		return nil
	}
	if state.size+int64(len(line)) > state.options.MaxFileBytes ||
		state.options.Now().UTC().Sub(state.segmentStarted) >= state.options.MaxAge {
		if err := state.rotate(); err != nil {
			return err
		}
	}
	written, err := state.file.Write(line)
	state.size += int64(written)
	return err
}

func (state *sharedState) openFile() error {
	if err := os.MkdirAll(filepath.Dir(state.options.Path), 0o700); err != nil {
		return fmt.Errorf("create system log directory: %w", err)
	}
	file, err := os.OpenFile(state.options.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open system log: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("stat system log: %w", err)
	}
	state.file = file
	state.size = info.Size()
	state.segmentStarted = info.ModTime().UTC()
	if state.segmentStarted.IsZero() {
		state.segmentStarted = state.options.Now().UTC()
	}
	return nil
}

func (state *sharedState) rotate() error {
	if state.file != nil {
		if err := state.file.Close(); err != nil {
			return err
		}
		state.file = nil
	}
	if state.options.MaxFiles <= 1 {
		if err := os.Remove(state.options.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove expired system log: %w", err)
		}
		state.size = 0
		return state.openFile()
	}
	lastBackup := state.options.MaxFiles - 1
	_ = os.Remove(fmt.Sprintf("%s.%d", state.options.Path, lastBackup))
	for index := lastBackup - 1; index >= 1; index-- {
		source := fmt.Sprintf("%s.%d", state.options.Path, index)
		destination := fmt.Sprintf("%s.%d", state.options.Path, index+1)
		if err := os.Rename(source, destination); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("rotate system log segment %d: %w", index, err)
		}
	}
	if err := os.Rename(state.options.Path, state.options.Path+".1"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("rotate active system log: %w", err)
	}
	state.size = 0
	state.pruneExpired()
	return state.openFile()
}

func (state *sharedState) pruneExpired() {
	if state.options.Path == "" || state.options.MaxAge <= 0 {
		return
	}
	cutoff := state.options.Now().UTC().Add(-state.options.MaxAge)
	matches, _ := filepath.Glob(state.options.Path + ".*")
	for _, path := range matches {
		info, err := os.Stat(path)
		if err == nil && info.ModTime().UTC().Before(cutoff) {
			_ = os.Remove(path)
		}
	}
}

func appendAttribute(fields map[string]any, group string, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()
	key := strings.TrimSpace(attr.Key)
	if key == "" {
		return
	}
	if group != "" {
		key = group + "." + key
	}
	if attr.Value.Kind() == slog.KindGroup {
		for _, child := range attr.Value.Group() {
			appendAttribute(fields, key, child)
		}
		return
	}
	fields[key] = slogValue(attr.Value)
}

func slogValue(value slog.Value) any {
	switch value.Kind() {
	case slog.KindBool:
		return value.Bool()
	case slog.KindDuration:
		return value.Duration().String()
	case slog.KindFloat64:
		return value.Float64()
	case slog.KindInt64:
		return value.Int64()
	case slog.KindString:
		return value.String()
	case slog.KindTime:
		return value.Time().UTC().Format(time.RFC3339Nano)
	case slog.KindUint64:
		return value.Uint64()
	case slog.KindAny:
		if err, ok := value.Any().(error); ok {
			return err.Error()
		}
		return value.Any()
	default:
		return fmt.Sprint(value.Any())
	}
}

func takeString(fields map[string]any, key string) string {
	value, ok := fields[key]
	if !ok {
		return ""
	}
	delete(fields, key)
	return strings.TrimSpace(fmt.Sprint(value))
}

func redactValue(key string, value any) any {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), ".", "_"))
	if normalized == "token" || strings.HasSuffix(normalized, "_token") {
		return redactedValue
	}
	for _, sensitive := range []string{
		"authorization", "cookie", "password", "secret", "api_key", "apikey", "access_token",
		"refresh_token", "credential", "prompt", "response_body", "request_body",
	} {
		if strings.Contains(normalized, sensitive) {
			return redactedValue
		}
	}
	return value
}

func matches(event Event, filter Filter) bool {
	if filter.Level != "" && strings.ToLower(event.Level) != filter.Level {
		return false
	}
	if filter.Module != "" && !strings.Contains(strings.ToLower(event.Module), filter.Module) {
		return false
	}
	if filter.RequestID != "" && event.RequestID != filter.RequestID {
		return false
	}
	if filter.TaskID != "" && event.TaskID != filter.TaskID {
		return false
	}
	if filter.Search == "" {
		return true
	}
	parts := []string{event.Message, event.Module, event.Code, event.RequestID, event.TaskID}
	keys := make([]string, 0, len(event.Fields))
	for key := range event.Fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		parts = append(parts, key, fmt.Sprint(event.Fields[key]))
	}
	return strings.Contains(strings.ToLower(strings.Join(parts, " ")), filter.Search)
}

func cloneEvent(event Event) Event {
	copy := event
	if event.Fields != nil {
		copy.Fields = make(map[string]any, len(event.Fields))
		for key, value := range event.Fields {
			copy.Fields[key] = value
		}
	}
	return copy
}

type teeHandler struct {
	handlers []slog.Handler
}

func Tee(handlers ...slog.Handler) slog.Handler {
	filtered := make([]slog.Handler, 0, len(handlers))
	for _, handler := range handlers {
		if handler != nil {
			filtered = append(filtered, handler)
		}
	}
	return teeHandler{handlers: filtered}
}

func (handler teeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, child := range handler.handlers {
		if child.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (handler teeHandler) Handle(ctx context.Context, record slog.Record) error {
	var result error
	for _, child := range handler.handlers {
		if child.Enabled(ctx, record.Level) {
			result = errors.Join(result, child.Handle(ctx, record.Clone()))
		}
	}
	return result
}

func (handler teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	children := make([]slog.Handler, 0, len(handler.handlers))
	for _, child := range handler.handlers {
		children = append(children, child.WithAttrs(attrs))
	}
	return teeHandler{handlers: children}
}

func (handler teeHandler) WithGroup(name string) slog.Handler {
	children := make([]slog.Handler, 0, len(handler.handlers))
	for _, child := range handler.handlers {
		children = append(children, child.WithGroup(name))
	}
	return teeHandler{handlers: children}
}

var _ slog.Handler = (*Recorder)(nil)
var _ slog.Handler = teeHandler{}
