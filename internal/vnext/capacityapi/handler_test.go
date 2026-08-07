package capacityapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LuTianTian001/JieShan/internal/vnext/capacity"
)

type staticSnapshot struct {
	value capacity.Snapshot
}

func (provider staticSnapshot) Snapshot() capacity.Snapshot { return provider.value }

func TestHandlerReturnsReadOnlyCapacitySnapshot(t *testing.T) {
	handler, err := New(staticSnapshot{value: capacity.Snapshot{
		Queued: 2,
		Sites:  []capacity.SiteSnapshot{{SiteID: 7, InFlight: 3, MaxInFlight: 4, Queued: 1}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, APIPrefix, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if body := response.Body.String(); !strings.Contains(body, `"maxConcurrency":4`) || strings.Contains(body, "max_in_flight") {
		t.Fatalf("public capacity contract = %s", body)
	}
	var snapshot capacity.Snapshot
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Queued != 2 || len(snapshot.Sites) != 1 || snapshot.Sites[0].MaxInFlight != 4 {
		t.Fatalf("snapshot = %+v", snapshot)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, APIPrefix, nil))
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("write response = %d %v", response.Code, response.Header())
	}
}
