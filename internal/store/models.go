package store

import "encoding/json"

type Settings struct {
	DefaultCooldownSeconds int   `json:"defaultCooldownSeconds"`
	FailureThreshold       int   `json:"failureThreshold"`
	FailureWindowSeconds   int   `json:"failureWindowSeconds"`
	ProbeIntervalSeconds   int   `json:"probeIntervalSeconds"`
	RequestDeadlineSeconds int   `json:"requestDeadlineSeconds"`
	MaxAttempts            int   `json:"maxAttempts"`
	LogRetentionDays       int   `json:"logRetentionDays"`
	UpdatedAt              int64 `json:"updatedAt"`
}

type Upstream struct {
	ID                   int64           `json:"id"`
	Name                 string          `json:"name"`
	Kind                 string          `json:"kind"`
	DashboardURL         string          `json:"dashboardUrl,omitempty"`
	BaseURL              string          `json:"baseUrl"`
	Enabled              bool            `json:"enabled"`
	CustomHeaders        json.RawMessage `json:"customHeaders"`
	EndpointID           int64           `json:"endpointId"`
	CredentialID         int64           `json:"credentialId"`
	CredentialConfigured bool            `json:"credentialConfigured"`
	CredentialState      string          `json:"credentialState"`
	BalanceValue         string          `json:"balanceValue,omitempty"`
	BalanceCurrency      string          `json:"balanceCurrency,omitempty"`
	Subscription         json.RawMessage `json:"subscription,omitempty"`
	LastBalanceSyncAt    *int64          `json:"lastBalanceSyncAt,omitempty"`
	ModelCount           int             `json:"modelCount"`
	CreatedAt            int64           `json:"createdAt"`
	UpdatedAt            int64           `json:"updatedAt"`
}

type UpstreamSecret struct {
	Upstream
	SecretCipher     []byte
	ManagementCipher []byte
}

type UpstreamModel struct {
	ID           int64  `json:"id"`
	UpstreamID   int64  `json:"upstreamId"`
	ModelName    string `json:"modelName"`
	Enabled      bool   `json:"enabled"`
	Stale        bool   `json:"stale"`
	MissingCount int    `json:"missingCount"`
	LastSeenAt   *int64 `json:"lastSeenAt,omitempty"`
}

type RouteTarget struct {
	ID                   int64  `json:"id"`
	RouteID              int64  `json:"routeId"`
	UpstreamID           int64  `json:"upstreamId"`
	UpstreamName         string `json:"upstreamName"`
	UpstreamKind         string `json:"upstreamKind"`
	UpstreamModelID      int64  `json:"upstreamModelId"`
	UpstreamModel        string `json:"upstreamModel"`
	BaseURL              string `json:"baseUrl"`
	EndpointID           int64  `json:"endpointId"`
	CredentialID         int64  `json:"credentialId"`
	Position             int    `json:"position"`
	Enabled              bool   `json:"enabled"`
	CircuitPhase         string `json:"circuitPhase"`
	ConsecutiveFails     int    `json:"consecutiveFailures"`
	CooldownUntil        *int64 `json:"cooldownUntil,omitempty"`
	CapabilityState      string `json:"capabilityState"`
	LastProbeStatus      string `json:"lastProbeStatus,omitempty"`
	LastProbeLatency     *int64 `json:"lastProbeLatencyMs,omitempty"`
	LastProbeAt          *int64 `json:"lastProbeAt,omitempty"`
	SecretCipher         []byte `json:"-"`
	CustomHeaders        []byte `json:"-"`
	CredentialState      string `json:"-"`
	CredentialName       string `json:"-"`
	LastErrorMessage     string `json:"-"`
	CooldownSeconds      int    `json:"-"`
	FailureThreshold     int    `json:"-"`
	FailureWindowSeconds int    `json:"-"`
}

type Route struct {
	ID                     int64         `json:"id"`
	PublicModel            string        `json:"publicModel"`
	DisplayName            string        `json:"displayName,omitempty"`
	Enabled                bool          `json:"enabled"`
	MonitorEnabled         bool          `json:"monitorEnabled"`
	MonitorIntervalSeconds int           `json:"monitorIntervalSeconds"`
	CooldownSeconds        int           `json:"cooldownSeconds"`
	FailureThreshold       int           `json:"failureThreshold"`
	FailureWindowSeconds   int           `json:"failureWindowSeconds"`
	Revision               int64         `json:"revision"`
	Targets                []RouteTarget `json:"targets"`
	CreatedAt              int64         `json:"createdAt"`
	UpdatedAt              int64         `json:"updatedAt"`
}

type DownstreamKey struct {
	ID               int64    `json:"id"`
	Name             string   `json:"name"`
	KeyPrefix        string   `json:"keyPrefix"`
	Enabled          bool     `json:"enabled"`
	QuotaMicroUSD    *int64   `json:"quotaMicroUsd,omitempty"`
	RPMLimit         int      `json:"rpmLimit"`
	UsedMicroUSD     int64    `json:"usedMicroUsd"`
	ReservedMicroUSD int64    `json:"reservedMicroUsd"`
	AllowedModels    []string `json:"allowedModels"`
	ExpiresAt        *int64   `json:"expiresAt,omitempty"`
	LastUsedAt       *int64   `json:"lastUsedAt,omitempty"`
	CreatedAt        int64    `json:"createdAt"`
	UpdatedAt        int64    `json:"updatedAt"`
}

type RequestLog struct {
	ID              string `json:"id"`
	DownstreamKeyID *int64 `json:"downstreamKeyId,omitempty"`
	KeyName         string `json:"keyName"`
	RouteID         *int64 `json:"routeId,omitempty"`
	RouteRevision   *int64 `json:"routeRevision,omitempty"`
	RequestedModel  string `json:"requestedModel"`
	ActualModel     string `json:"actualModel,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	ThinkingBudget  *int64 `json:"thinkingBudget,omitempty"`
	Status          string `json:"status"`
	HTTPStatus      *int   `json:"httpStatus,omitempty"`
	Stream          bool   `json:"stream"`
	FirstTokenMS    *int64 `json:"firstTokenMs,omitempty"`
	DurationMS      *int64 `json:"durationMs,omitempty"`
	InputTokens     *int64 `json:"inputTokens,omitempty"`
	CacheReadTokens *int64 `json:"cacheReadTokens,omitempty"`
	OutputTokens    *int64 `json:"outputTokens,omitempty"`
	ReasoningTokens *int64 `json:"reasoningTokens,omitempty"`
	CostMicroUSD    int64  `json:"costMicroUsd"`
	SwitchCount     int    `json:"switchCount"`
	ErrorMessage    string `json:"errorMessage,omitempty"`
	StartedAt       int64  `json:"startedAt"`
	FinishedAt      *int64 `json:"finishedAt,omitempty"`
}

type RequestAttempt struct {
	ID            int64  `json:"id"`
	RequestID     string `json:"requestId"`
	AttemptIndex  int    `json:"attemptIndex"`
	TargetID      *int64 `json:"targetId,omitempty"`
	UpstreamID    *int64 `json:"upstreamId,omitempty"`
	UpstreamName  string `json:"upstreamName,omitempty"`
	UpstreamModel string `json:"upstreamModel,omitempty"`
	Status        string `json:"status"`
	HTTPStatus    *int   `json:"httpStatus,omitempty"`
	SwitchReason  string `json:"switchReason,omitempty"`
	ErrorClass    string `json:"errorClass,omitempty"`
	ErrorMessage  string `json:"errorMessage,omitempty"`
	LatencyMS     *int64 `json:"latencyMs,omitempty"`
	FirstTokenMS  *int64 `json:"firstTokenMs,omitempty"`
	CreatedAt     int64  `json:"createdAt"`
}

type MonitorCell struct {
	TargetID      int64  `json:"targetId"`
	UpstreamID    int64  `json:"upstreamId"`
	UpstreamName  string `json:"upstreamName"`
	Status        string `json:"status"`
	LatencyMS     *int64 `json:"latencyMs,omitempty"`
	CheckedAt     *int64 `json:"checkedAt,omitempty"`
	CooldownUntil *int64 `json:"cooldownUntil,omitempty"`
}

type MonitorRow struct {
	RouteID     int64         `json:"routeId"`
	PublicModel string        `json:"publicModel"`
	Enabled     bool          `json:"enabled"`
	Cells       []MonitorCell `json:"cells"`
}
