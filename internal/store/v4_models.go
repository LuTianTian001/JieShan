package store

import (
	"encoding/json"
	"time"

	"github.com/LuTianTian001/JieShan/internal/health"
)

type PublishedModelHealthPolicy struct {
	FailureThreshold     int
	FailureWindowSeconds int
	CooldownSeconds      int
}

type RouteSiteTargetHealth struct {
	TargetID            int64  `json:"targetId"`
	CircuitPhase        string `json:"circuitPhase"`
	ConsecutiveFailures int    `json:"consecutiveFailures"`
	LastFailureAt       *int64 `json:"lastFailureAt,omitempty"`
	LastSuccessAt       *int64 `json:"lastSuccessAt,omitempty"`
	CooldownUntil       *int64 `json:"cooldownUntil,omitempty"`
	HalfOpenLeaseUntil  *int64 `json:"halfOpenLeaseUntil,omitempty"`
	CapabilityState     string `json:"capabilityState"`
	LastErrorClass      string `json:"lastErrorClass,omitempty"`
	LastErrorMessage    string `json:"lastErrorMessage,omitempty"`
	LastIncidentID      string `json:"lastIncidentId,omitempty"`
	UpdatedAt           int64  `json:"updatedAt"`
}

type RouteSiteTargetHealthEvent struct {
	Kind         string
	Decision     health.Decision
	IncidentID   string
	ErrorMessage string
	OccurredAt   int64
	RetryAfter   time.Duration
}

type ProbeRun struct {
	ID                     string `json:"id"`
	PublishedModelID       int64  `json:"publishedModelId"`
	PublicModel            string `json:"publicModel,omitempty"`
	PublishedModelRevision int64  `json:"publishedModelRevision"`
	TriggerKind            string `json:"triggerKind"`
	Status                 string `json:"status"`
	TargetCount            int    `json:"targetCount"`
	SuccessCount           int    `json:"successCount"`
	FailureCount           int    `json:"failureCount"`
	SkippedCount           int    `json:"skippedCount"`
	ErrorMessage           string `json:"errorMessage,omitempty"`
	StartedAt              int64  `json:"startedAt"`
	FinishedAt             *int64 `json:"finishedAt,omitempty"`
}

type ProbeAttempt struct {
	ID                    int64  `json:"id"`
	ProbeRunID            string `json:"probeRunId"`
	AttemptIndex          int    `json:"attemptIndex"`
	RouteSiteTargetID     *int64 `json:"routeSiteTargetId,omitempty"`
	SiteID                *int64 `json:"siteId,omitempty"`
	EndpointID            *int64 `json:"endpointId,omitempty"`
	InferenceCredentialID *int64 `json:"inferenceCredentialId,omitempty"`
	SiteModelID           *int64 `json:"siteModelId,omitempty"`
	SiteName              string `json:"siteName"`
	EndpointName          string `json:"endpointName"`
	CredentialName        string `json:"credentialName,omitempty"`
	SourceModel           string `json:"sourceModel"`
	Status                string `json:"status"`
	HTTPStatus            *int   `json:"httpStatus,omitempty"`
	LatencyMS             *int64 `json:"latencyMs,omitempty"`
	FirstOutputMS         *int64 `json:"firstOutputMs,omitempty"`
	ErrorClass            string `json:"errorClass,omitempty"`
	ErrorMessage          string `json:"errorMessage,omitempty"`
	StartedAt             int64  `json:"startedAt"`
	FinishedAt            int64  `json:"finishedAt"`
}

// RouteSiteTargetMonitor combines the administrator's configured target with
// its reduced circuit state and the latest persisted probe observation.
type RouteSiteTargetMonitor struct {
	RouteSiteTarget
	Health    RouteSiteTargetHealth `json:"health"`
	LastProbe *ProbeAttempt         `json:"lastProbe,omitempty"`
}

type PublishedModelMonitor struct {
	PublishedModel
	Targets []RouteSiteTargetMonitor `json:"targets"`
}

type ModelDiscoveryRun struct {
	ID                   string          `json:"id"`
	SiteID               int64           `json:"siteId"`
	EndpointID           int64           `json:"endpointId"`
	Mode                 string          `json:"mode"`
	Status               string          `json:"status"`
	BaseSiteRevision     int64           `json:"baseSiteRevision"`
	BaseEndpointRevision int64           `json:"baseEndpointRevision"`
	CredentialCount      int             `json:"credentialCount"`
	SuccessCount         int             `json:"successCount"`
	ModelCount           int             `json:"modelCount"`
	Summary              json.RawMessage `json:"summary"`
	ErrorMessage         string          `json:"errorMessage,omitempty"`
	StartedAt            int64           `json:"startedAt"`
	FinishedAt           *int64          `json:"finishedAt,omitempty"`
}

type ModelDiscoveryAttempt struct {
	ID                    int64  `json:"id"`
	DiscoveryRunID        string `json:"discoveryRunId"`
	AttemptIndex          int    `json:"attemptIndex"`
	InferenceCredentialID *int64 `json:"inferenceCredentialId,omitempty"`
	CredentialName        string `json:"credentialName"`
	Status                string `json:"status"`
	ModelCount            int    `json:"modelCount"`
	Complete              bool   `json:"complete"`
	PagesFetched          int    `json:"pagesFetched"`
	ErrorClass            string `json:"errorClass,omitempty"`
	ErrorMessage          string `json:"errorMessage,omitempty"`
	StartedAt             int64  `json:"startedAt"`
	FinishedAt            int64  `json:"finishedAt"`
}
