package settingsapi

import "context"

type RuntimeOverviewProvider interface {
	RuntimeOverview(context.Context) (RuntimeOverview, error)
}

type RuntimeOverview struct {
	Runtime          GatewayRuntimeSnapshot `json:"runtime"`
	MeteringWarnings []MeteringWarning      `json:"meteringWarnings"`
	BackgroundTasks  []BackgroundTaskHealth `json:"backgroundTasks"`
	ConfigHistory    []ConfigHistoryEntry   `json:"configHistory"`
}

type GatewayRuntimeSnapshot struct {
	ProcessStartedAt          int64  `json:"processStartedAt"`
	SnapshotAt                int64  `json:"snapshotAt"`
	ConfigRevision            int64  `json:"configRevision"`
	ConfigLoadedAt            int64  `json:"configLoadedAt"`
	ActivePriceCatalogVersion string `json:"activePriceCatalogVersion"`
	InflightRequests          int    `json:"inflightRequests"`
	MaxConcurrency            int    `json:"maxConcurrency"`
	QueuedRequests            int    `json:"queuedRequests"`
	MeteringMode              string `json:"meteringMode"`
}

type MeteringWarning struct {
	Code             string `json:"code"`
	Severity         string `json:"severity"`
	Title            string `json:"title"`
	Message          string `json:"message"`
	AffectedRequests int64  `json:"affectedRequests"`
	Since            int64  `json:"since"`
	LastSeenAt       int64  `json:"lastSeenAt"`
}

type BackgroundTaskHealth struct {
	ID             string `json:"id"`
	Label          string `json:"label"`
	State          string `json:"state"`
	Schedule       string `json:"schedule"`
	LastStartedAt  *int64 `json:"lastStartedAt"`
	LastFinishedAt *int64 `json:"lastFinishedAt"`
	NextRunAt      *int64 `json:"nextRunAt"`
	LastDurationMS *int64 `json:"lastDurationMs"`
	LastErrorCode  string `json:"lastErrorCode"`
}

type ConfigHistoryEntry struct {
	ID            string   `json:"id"`
	Revision      int64    `json:"revision"`
	Actor         string   `json:"actor"`
	Summary       string   `json:"summary"`
	ChangedFields []string `json:"changedFields"`
	Status        string   `json:"status"`
	CreatedAt     int64    `json:"createdAt"`
}
