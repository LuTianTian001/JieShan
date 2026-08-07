package vnextmigration

import (
	"encoding/json"
	"io"
)

const ReportFormatVersion = 1

type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

type Issue struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
}

type SourceReport struct {
	Path               string `json:"path,omitempty"`
	SchemaVersion      int    `json:"schemaVersion,omitempty"`
	HasLegacyRouting   bool   `json:"hasLegacyRouting"`
	HasV3Routing       bool   `json:"hasV3Routing"`
	HasRoutingProfiles bool   `json:"hasRoutingProfiles"`
}

type Summary struct {
	KeyCount              int `json:"keyCount"`
	ModelCount            int `json:"modelCount"`
	RoutableModelCount    int `json:"routableModelCount"`
	TargetCount           int `json:"targetCount"`
	RoutableTargetCount   int `json:"routableTargetCount"`
	ImplicitRouteCount    int `json:"implicitRouteCount"`
	NonRevealableKeyCount int `json:"nonRevealableKeyCount"`
	IssueCount            int `json:"issueCount"`
}

type Report struct {
	FormatVersion int          `json:"formatVersion"`
	GeneratedAtMS int64        `json:"generatedAtMs"`
	Source        SourceReport `json:"source"`
	Summary       Summary      `json:"summary"`
	Keys          []KeyReport  `json:"keys"`
	Issues        []Issue      `json:"issues,omitempty"`
}

type KeyReport struct {
	LegacyID            int64           `json:"legacyId"`
	Name                string          `json:"name"`
	Prefix              string          `json:"prefix"`
	Enabled             bool            `json:"enabled"`
	ExpiresAtMS         *int64          `json:"expiresAtMs,omitempty"`
	AllowedModels       []string        `json:"allowedModels"`
	AllowedModelsMode   string          `json:"allowedModelsMode"`
	RoutingProfileID    *int64          `json:"routingProfileId,omitempty"`
	RoutingProfileName  string          `json:"routingProfileName,omitempty"`
	SecretRevealable    bool            `json:"secretRevealable"`
	NonRevealableReason string          `json:"nonRevealableReason,omitempty"`
	Models              []ModelReport   `json:"models"`
	ExcludedModels      []ExcludedModel `json:"excludedModels,omitempty"`
	UnresolvedAllowlist []string        `json:"unresolvedAllowlist,omitempty"`
	Issues              []Issue         `json:"issues,omitempty"`
}

type ExcludedModel struct {
	PublicName string   `json:"publicName"`
	Generation string   `json:"generation"`
	Reasons    []string `json:"reasons"`
}

type ModelReport struct {
	PublicName                string         `json:"publicName"`
	Generation                string         `json:"generation"`
	LegacyRouteID             *int64         `json:"legacyRouteId,omitempty"`
	PublishedModelID          *int64         `json:"publishedModelId,omitempty"`
	ShadowedLegacyRouteID     *int64         `json:"shadowedLegacyRouteId,omitempty"`
	Revision                  int64          `json:"revision"`
	Enabled                   bool           `json:"enabled"`
	ExplicitPriceSKU          string         `json:"explicitPriceSku,omitempty"`
	EffectivePriceSKU         string         `json:"effectivePriceSku"`
	ExplicitPriceSKUMissing   bool           `json:"explicitPriceSkuMissing"`
	ResolutionSource          string         `json:"resolutionSource"`
	AppliedRoutingProfileID   *int64         `json:"appliedRoutingProfileId,omitempty"`
	AppliedRoutingProfileName string         `json:"appliedRoutingProfileName,omitempty"`
	ImplicitInheritance       bool           `json:"implicitInheritance"`
	Routable                  bool           `json:"routable"`
	Targets                   []TargetReport `json:"targets"`
	Issues                    []Issue        `json:"issues,omitempty"`
}

type CredentialReport struct {
	LegacyID     int64  `json:"legacyId"`
	Name         string `json:"name"`
	Enabled      bool   `json:"enabled"`
	RuntimeState string `json:"runtimeState"`
	Configured   bool   `json:"configured"`
	Eligible     bool   `json:"eligible"`
}

type TargetReport struct {
	LegacyID                 int64              `json:"legacyId"`
	Position                 int                `json:"position"`
	SiteID                   *int64             `json:"siteId,omitempty"`
	SiteName                 string             `json:"siteName,omitempty"`
	UpstreamID               *int64             `json:"upstreamId,omitempty"`
	UpstreamName             string             `json:"upstreamName,omitempty"`
	EndpointID               *int64             `json:"endpointId,omitempty"`
	EndpointName             string             `json:"endpointName,omitempty"`
	BaseURL                  string             `json:"baseUrl,omitempty"`
	SourceProtocol           string             `json:"sourceProtocol,omitempty"`
	WireProtocol             string             `json:"wireProtocol,omitempty"`
	Surface                  string             `json:"surface,omitempty"`
	SurfaceCandidates        []string           `json:"surfaceCandidates,omitempty"`
	ProtocolMappingAmbiguous bool               `json:"protocolMappingAmbiguous"`
	SourceModel              string             `json:"sourceModel,omitempty"`
	SiteModelID              *int64             `json:"siteModelId,omitempty"`
	CredentialID             *int64             `json:"credentialId,omitempty"`
	CredentialName           string             `json:"credentialName,omitempty"`
	Credentials              []CredentialReport `json:"credentials,omitempty"`
	CredentialCount          int                `json:"credentialCount"`
	EligibleCredentialCount  int                `json:"eligibleCredentialCount"`
	Enabled                  bool               `json:"enabled"`
	EndpointMissing          bool               `json:"endpointMissing"`
	CredentialMissing        bool               `json:"credentialMissing"`
	Routable                 bool               `json:"routable"`
	Issues                   []Issue            `json:"issues,omitempty"`
}

func WriteJSON(w io.Writer, report Report) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(report)
}
