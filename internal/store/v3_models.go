package store

import (
	"encoding/json"

	"github.com/LuTianTian001/JieShan/internal/inferenceprotocol"
)

// Site is an upstream website. Inference transport and account management are
// deliberately modeled as separate child resources.
type Site struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	DashboardURL string `json:"dashboardUrl,omitempty"`
	Enabled      bool   `json:"enabled"`
	Revision     int64  `json:"revision"`
	CreatedAt    int64  `json:"createdAt"`
	UpdatedAt    int64  `json:"updatedAt"`
}

type SiteWrite struct {
	Name         string
	DashboardURL string
	Enabled      bool
}

// SiteSummary is the bulk list projection used by the administration UI. It
// reports concrete inventory counts instead of fabricating a site-wide health
// state or latency value.
type SiteSummary struct {
	Site
	EndpointCount              int    `json:"endpointCount"`
	EnabledEndpointCount       int    `json:"enabledEndpointCount"`
	CredentialCount            int    `json:"credentialCount"`
	EnabledCredentialCount     int    `json:"enabledCredentialCount"`
	UnavailableCredentialCount int    `json:"unavailableCredentialCount"`
	ModelCount                 int    `json:"modelCount"`
	ActiveModelCount           int    `json:"activeModelCount"`
	PublishedModelCount        int    `json:"publishedModelCount"`
	LastModelSeenAt            *int64 `json:"lastModelSeenAt,omitempty"`
}

type InferenceEndpoint struct {
	ID                   int64                          `json:"id"`
	SiteID               int64                          `json:"siteId"`
	Name                 string                         `json:"name"`
	BaseURL              string                         `json:"baseUrl"`
	WireProtocol         string                         `json:"wireProtocol"`
	CompatibilityProfile string                         `json:"compatibilityProfile"`
	AuthScheme           string                         `json:"authScheme"`
	CustomHeaders        json.RawMessage                `json:"customHeaders"`
	Capabilities         inferenceprotocol.Capabilities `json:"capabilities"`
	Position             int                            `json:"position"`
	Enabled              bool                           `json:"enabled"`
	Revision             int64                          `json:"revision"`
	CreatedAt            int64                          `json:"createdAt"`
	UpdatedAt            int64                          `json:"updatedAt"`
}

type InferenceEndpointWrite struct {
	Name                 string
	BaseURL              string
	WireProtocol         string
	CompatibilityProfile string
	AuthScheme           string
	CustomHeaders        json.RawMessage
	Enabled              bool
}

type InferenceCredential struct {
	ID               int64  `json:"id"`
	SiteID           int64  `json:"siteId"`
	Name             string `json:"name"`
	SecretConfigured bool   `json:"secretConfigured"`
	Position         int    `json:"position"`
	Enabled          bool   `json:"enabled"`
	RuntimeState     string `json:"runtimeState"`
	CooldownUntil    *int64 `json:"cooldownUntil,omitempty"`
	LastTestAt       *int64 `json:"lastTestAt,omitempty"`
	LastTestStatus   string `json:"lastTestStatus,omitempty"`
	LastErrorMessage string `json:"lastErrorMessage,omitempty"`
	Revision         int64  `json:"revision"`
	CreatedAt        int64  `json:"createdAt"`
	UpdatedAt        int64  `json:"updatedAt"`
}

type InferenceCredentialSecret struct {
	InferenceCredential
	SecretCipher []byte `json:"-"`
}

type InferenceCredentialWrite struct {
	Name         string
	SecretCipher []byte
	Enabled      bool
}

type InferenceCredentialUpdate struct {
	Name          string
	SecretCipher  []byte
	ReplaceSecret bool
	Enabled       bool
}

type InferenceCredentialRuntimeUpdate struct {
	RuntimeState     string
	CooldownUntil    *int64
	LastTestAt       *int64
	LastTestStatus   string
	LastErrorMessage string
}

type SiteModel struct {
	ID           int64  `json:"id"`
	SiteID       int64  `json:"siteId"`
	EndpointID   int64  `json:"endpointId"`
	ModelName    string `json:"modelName"`
	DisplayName  string `json:"displayName,omitempty"`
	Enabled      bool   `json:"enabled"`
	Stale        bool   `json:"stale"`
	MissingCount int    `json:"missingCount"`
	LastSeenAt   *int64 `json:"lastSeenAt,omitempty"`
	Revision     int64  `json:"revision"`
	CreatedAt    int64  `json:"createdAt"`
	UpdatedAt    int64  `json:"updatedAt"`
}

type SiteModelWrite struct {
	SiteID       int64
	EndpointID   int64
	ModelName    string
	DisplayName  string
	Enabled      bool
	Stale        bool
	MissingCount int
	LastSeenAt   *int64
}

type SiteModelCoverage struct {
	SiteModel
	CredentialCount            int `json:"credentialCount"`
	SupportedCredentialCount   int `json:"supportedCredentialCount"`
	UnsupportedCredentialCount int `json:"unsupportedCredentialCount"`
	UnknownCredentialCount     int `json:"unknownCredentialCount"`
}

type CredentialModelAccess struct {
	SiteID        int64  `json:"siteId"`
	CredentialID  int64  `json:"credentialId"`
	SiteModelID   int64  `json:"siteModelId"`
	Availability  string `json:"availability"`
	MissingCount  int    `json:"missingCount"`
	LastSeenAt    *int64 `json:"lastSeenAt,omitempty"`
	LastCheckedAt *int64 `json:"lastCheckedAt,omitempty"`
	Revision      int64  `json:"revision"`
	UpdatedAt     int64  `json:"updatedAt"`
}

type CredentialModelAccessWrite struct {
	SiteID        int64
	CredentialID  int64
	SiteModelID   int64
	Availability  string
	MissingCount  int
	LastSeenAt    *int64
	LastCheckedAt *int64
}

type PublishedModel struct {
	ID                        int64  `json:"id"`
	PublicName                string `json:"publicName"`
	DisplayName               string `json:"displayName,omitempty"`
	OfficialPriceSKU          string `json:"officialPriceSku,omitempty"`
	Enabled                   bool   `json:"enabled"`
	MonitorEnabled            bool   `json:"monitorEnabled"`
	MonitorIntervalSeconds    int    `json:"monitorIntervalSeconds"`
	CooldownSeconds           int    `json:"cooldownSeconds"`
	FailureThreshold          int    `json:"failureThreshold"`
	FailureWindowSeconds      int    `json:"failureWindowSeconds"`
	FirstOutputTimeoutSeconds int    `json:"firstOutputTimeoutSeconds"`
	StreamIdleTimeoutSeconds  int    `json:"streamIdleTimeoutSeconds"`
	RequestDeadlineSeconds    int    `json:"requestDeadlineSeconds"`
	MaxAttempts               int    `json:"maxAttempts"`
	Revision                  int64  `json:"revision"`
	CreatedAt                 int64  `json:"createdAt"`
	UpdatedAt                 int64  `json:"updatedAt"`
}

type PublishedModelWrite struct {
	PublicName                string
	DisplayName               string
	OfficialPriceSKU          string
	Enabled                   bool
	MonitorEnabled            bool
	MonitorIntervalSeconds    int
	CooldownSeconds           int
	FailureThreshold          int
	FailureWindowSeconds      int
	FirstOutputTimeoutSeconds int
	StreamIdleTimeoutSeconds  int
	RequestDeadlineSeconds    int
	MaxAttempts               int
}

type PublishedModelRoute struct {
	PublishedModel
	Targets []RouteSiteTarget `json:"targets"`
}

type RouteSiteTarget struct {
	ID               int64  `json:"id"`
	PublishedModelID int64  `json:"publishedModelId"`
	SiteID           int64  `json:"siteId"`
	SiteName         string `json:"siteName"`
	EndpointID       int64  `json:"endpointId"`
	EndpointName     string `json:"endpointName"`
	WireProtocol     string `json:"wireProtocol"`
	SiteModelID      int64  `json:"siteModelId"`
	SourceModel      string `json:"sourceModel"`
	Position         int    `json:"position"`
	Enabled          bool   `json:"enabled"`
	Revision         int64  `json:"revision"`
	CreatedAt        int64  `json:"createdAt"`
	UpdatedAt        int64  `json:"updatedAt"`
}

type RouteSiteTargetWrite struct {
	SiteID      int64
	EndpointID  int64
	SiteModelID int64
	Enabled     bool
}

type ResolvedRouteSiteTarget struct {
	RouteSiteTarget
	BaseURL              string                      `json:"baseUrl"`
	CompatibilityProfile string                      `json:"compatibilityProfile"`
	AuthScheme           string                      `json:"authScheme"`
	CustomHeaders        json.RawMessage             `json:"customHeaders"`
	Credentials          []InferenceCredentialSecret `json:"-"`
}

type ResolvedPublishedModel struct {
	PublishedModel
	RoutingProfileID   *int64                    `json:"routingProfileId,omitempty"`
	RoutingProfileName string                    `json:"routingProfileName"`
	Targets            []ResolvedRouteSiteTarget `json:"targets"`
}

const DefaultRoutingProfileName = "Default route"

type RoutingProfile struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Revision           int64  `json:"revision"`
	ModelOverrideCount int    `json:"modelOverrideCount"`
	CreatedAt          int64  `json:"createdAt"`
	UpdatedAt          int64  `json:"updatedAt"`
}

type RoutingProfileModelRoute struct {
	RoutingProfileID int64             `json:"routingProfileId"`
	PublishedModelID int64             `json:"publishedModelId"`
	ProfileRevision  int64             `json:"profileRevision"`
	InheritsDefault  bool              `json:"inheritsDefault"`
	Targets          []RouteSiteTarget `json:"targets"`
}
