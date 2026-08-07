package capacityapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/capacity"
)

const APIPrefix = "/api/vnext/capacity"

type SnapshotProvider interface {
	Snapshot() capacity.Snapshot
}

type Handler struct {
	provider SnapshotProvider
}

type snapshotResponse struct {
	UpdatedAt      int64                  `json:"updatedAt"`
	QueuedRequests int                    `json:"queuedRequests"`
	Sites          []siteSnapshotResponse `json:"sites"`
}

type siteSnapshotResponse struct {
	SiteID           capacity.SiteID `json:"siteId"`
	InflightRequests int             `json:"inflightRequests"`
	MaxConcurrency   int             `json:"maxConcurrency"`
	QueuedRequests   int             `json:"queuedRequests"`
	ThrottledUntil   *string         `json:"throttledUntil"`
}

func New(provider SnapshotProvider) (*Handler, error) {
	if provider == nil {
		return nil, errors.New("capacity snapshot provider is required")
	}
	return &Handler{provider: provider}, nil
}

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != APIPrefix && request.URL.Path != APIPrefix+"/" {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{
			"error": map[string]string{"code": "method_not_allowed", "message": "method not allowed"},
		}, request.Method == http.MethodHead)
		return
	}
	writeJSON(writer, http.StatusOK, presentSnapshot(handler.provider.Snapshot()), request.Method == http.MethodHead)
}

func presentSnapshot(snapshot capacity.Snapshot) snapshotResponse {
	response := snapshotResponse{
		UpdatedAt: snapshot.UpdatedAt, QueuedRequests: snapshot.Queued,
		Sites: make([]siteSnapshotResponse, 0, len(snapshot.Sites)),
	}
	for _, site := range snapshot.Sites {
		var throttledUntil *string
		if !site.ThrottledUntil.IsZero() {
			formatted := site.ThrottledUntil.UTC().Format(time.RFC3339Nano)
			throttledUntil = &formatted
		}
		response.Sites = append(response.Sites, siteSnapshotResponse{
			SiteID: site.SiteID, InflightRequests: site.InFlight, MaxConcurrency: site.MaxInFlight,
			QueuedRequests: site.Queued, ThrottledUntil: throttledUntil,
		})
	}
	return response
}

func writeJSON(writer http.ResponseWriter, status int, value any, head bool) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(status)
	if !head {
		_ = json.NewEncoder(writer).Encode(value)
	}
}
