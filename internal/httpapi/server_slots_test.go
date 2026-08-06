package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestManagementRequestsCannotConsumeInferenceSlots(t *testing.T) {
	managementStarted := make(chan struct{})
	releaseManagement := make(chan struct{})
	server := &Server{
		mux:             http.NewServeMux(),
		inferenceSlots:  make(chan struct{}, 1),
		managementSlots: make(chan struct{}, 1),
	}
	server.mux.HandleFunc("GET /api/v1/slow", func(w http.ResponseWriter, _ *http.Request) {
		close(managementStarted)
		<-releaseManagement
		w.WriteHeader(http.StatusNoContent)
	})
	server.mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := server.Handler()

	managementDone := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/slow", nil))
		close(managementDone)
	}()
	select {
	case <-managementStarted:
	case <-time.After(time.Second):
		t.Fatal("management request did not acquire its slot")
	}

	inference := httptest.NewRecorder()
	handler.ServeHTTP(inference, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	if inference.Code != http.StatusNoContent {
		t.Fatalf("inference status = %d, want %d", inference.Code, http.StatusNoContent)
	}

	busyManagement := httptest.NewRecorder()
	handler.ServeHTTP(busyManagement, httptest.NewRequest(http.MethodGet, "/api/v1/slow", nil))
	if busyManagement.Code != http.StatusServiceUnavailable {
		t.Fatalf("second management status = %d, want %d", busyManagement.Code, http.StatusServiceUnavailable)
	}
	close(releaseManagement)
	select {
	case <-managementDone:
	case <-time.After(time.Second):
		t.Fatal("management request did not finish")
	}
}
