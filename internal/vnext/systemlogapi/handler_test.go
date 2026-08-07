package systemlogapi

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LuTianTian001/JieShan/internal/vnext/systemlog"
)

func TestHandlerListsFilteredSystemLogs(t *testing.T) {
	recorder, err := systemlog.New(systemlog.Options{MemoryEntries: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
	slog.New(recorder).Info("probe started", "module", "monitor", "task_id", "task-1")
	handler, err := New(recorder)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, APIPrefix+"?module=monitor&taskId=task-1", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"message":"probe started"`) ||
		!strings.Contains(response.Body.String(), `"nextBefore":0`) {
		t.Fatalf("list response = %d %s", response.Code, response.Body.String())
	}
}

func TestHandlerRejectsInvalidCursorAndMethod(t *testing.T) {
	recorder, _ := systemlog.New(systemlog.Options{MemoryEntries: 2})
	t.Cleanup(func() { _ = recorder.Close() })
	handler, _ := New(recorder)

	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, APIPrefix+"?before=nope", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor status = %d", invalid.Code)
	}

	method := httptest.NewRecorder()
	handler.ServeHTTP(method, httptest.NewRequest(http.MethodPost, APIPrefix, nil))
	if method.Code != http.StatusMethodNotAllowed || method.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("method response = %d %q", method.Code, method.Header().Get("Allow"))
	}
}
