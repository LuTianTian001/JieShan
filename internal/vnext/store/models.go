package store

import "encoding/json"

const DefaultSiteMaxInFlight = 4

type Site struct {
	ID           int64
	Name         string
	DashboardURL string
	Enabled      bool
	MaxInFlight  int
	Revision     int64
	CreatedAt    int64
	UpdatedAt    int64
}

type SiteWrite struct {
	Name         string
	DashboardURL string
	Enabled      bool
	MaxInFlight  int
}

type SiteEndpoint struct {
	ID                         int64
	SiteID                     int64
	Name                       string
	BaseURL                    string
	WireProtocol               string
	Surface                    string
	AdapterKind                string
	AuthScheme                 string
	HeaderTemplate             json.RawMessage
	SecretHeadersConfigured    bool
	SecretHeadersCipherVersion int64
	Position                   int
	Enabled                    bool
	Revision                   int64
	CreatedAt                  int64
	UpdatedAt                  int64
}

type SiteEndpointWrite struct {
	Name                       string
	BaseURL                    string
	WireProtocol               string
	Surface                    string
	AdapterKind                string
	AuthScheme                 string
	HeaderTemplate             json.RawMessage
	SecretHeadersCipher        []byte
	SecretHeadersCipherVersion int64
	Enabled                    bool
}

type SiteCredential struct {
	ID               int64
	SiteID           int64
	Name             string
	SecretConfigured bool
	CipherVersion    int64
	Enabled          bool
	Revision         int64
	CreatedAt        int64
	UpdatedAt        int64
}

type SiteCredentialWrite struct {
	Name          string
	SecretCipher  []byte
	CipherVersion int64
	Enabled       bool
}

type CredentialEndpointBinding struct {
	SiteID         int64
	EndpointID     int64
	CredentialID   int64
	CredentialName string
	Position       int
	Enabled        bool
	CreatedAt      int64
	UpdatedAt      int64
}

type ProviderModelTarget struct {
	ID          int64
	SiteID      int64
	EndpointID  int64
	SourceModel string
	DisplayName string
	Enabled     bool
	Revision    int64
	LastSeenAt  *int64
	CreatedAt   int64
	UpdatedAt   int64
}

type ProviderModelTargetWrite struct {
	SiteID      int64
	EndpointID  int64
	SourceModel string
	DisplayName string
	Enabled     bool
	LastSeenAt  *int64
}

type CredentialTargetAccess struct {
	SiteID                int64
	EndpointID            int64
	CredentialID          int64
	ProviderModelTargetID int64
	Availability          string
	LastHTTPStatus        *int
	LastErrorCode         string
	LastCheckedAt         *int64
	Revision              int64
	UpdatedAt             int64
}

type CredentialTargetAccessWrite struct {
	SiteID                int64
	EndpointID            int64
	CredentialID          int64
	ProviderModelTargetID int64
	Availability          string
	LastHTTPStatus        *int
	LastErrorCode         string
	LastCheckedAt         *int64
}

type DownstreamKey struct {
	ID                        int64
	Name                      string
	KeyPrefix                 string
	RoutingProfileID          int64
	RoutingProfileName        string
	UsesDefaultRoutingProfile bool
	Enabled                   bool
	Revealable                bool
	RevealVersion             int64
	QuotaNanoUSD              *int64
	UsedNanoUSD               int64
	ReservedNanoUSD           int64
	HourlyQuotaNanoUSD        *int64
	UsedThisHourNanoUSD       int64
	ReservedThisHourNanoUSD   int64
	HourlyWindowStartedAt     int64
	BillingMultiplierBPS      int
	ExpiresAt                 *int64
	LastUsedAt                *int64
	Revision                  int64
	CreatedAt                 int64
	UpdatedAt                 int64
}

type DownstreamKeyWrite struct {
	Name                 string
	KeyPrefix            string
	KeyDigest            []byte
	RoutingProfileID     *int64
	Enabled              bool
	QuotaNanoUSD         *int64
	HourlyQuotaNanoUSD   *int64
	BillingMultiplierBPS *int
	ExpiresAt            *int64
}

type DownstreamKeyUpdate struct {
	ExpectedRevision     int64
	Name                 string
	RoutingProfileID     *int64
	Enabled              bool
	QuotaNanoUSD         *int64
	HourlyQuotaNanoUSD   *int64
	BillingMultiplierBPS int
	ExpiresAt            *int64
}

type PublishedModel struct {
	ID               int64
	PublicName       string
	OfficialPriceSKU string
	Enabled          bool
	Revision         int64
	Targets          []PublishedModelTarget
	CreatedAt        int64
	UpdatedAt        int64
}

type PublishedModelWrite struct {
	PublicName       string
	OfficialPriceSKU string
	Enabled          bool
}

type PublishedModelUpdate struct {
	ExpectedRevision int64
	PublicName       string
	OfficialPriceSKU string
	Enabled          bool
}

type PublishedModelTarget struct {
	ID                    int64
	PublishedModelID      int64
	SiteID                int64
	SiteName              string
	EndpointID            int64
	EndpointName          string
	ProviderModelTargetID int64
	SourceModel           string
	WireProtocol          string
	Surface               string
	Position              int
	Revision              int64
	CreatedAt             int64
	UpdatedAt             int64
}

type RoutingProfile struct {
	ID                  int64
	Name                string
	Default             bool
	Revision            int64
	ModelCount          int
	LocalModelCount     int
	InheritedModelCount int
	DownstreamKeyCount  int
	CreatedAt           int64
	UpdatedAt           int64
}

type RoutingProfileRoute struct {
	RoutingProfileID       int64
	RoutingProfileName     string
	SourceProfileID        int64
	SourceProfileName      string
	Inherited              bool
	TargetsOverridden      bool
	PublishedModelID       int64
	PublicName             string
	OfficialPriceSKU       string
	Enabled                bool
	PublishedModelRevision int64
	Revision               int64
	Targets                []PublishedModelTarget
	CreatedAt              int64
	UpdatedAt              int64
}

type RoutingProfileRouteWrite struct {
	PublishedModelID int64
	Enabled          bool
	TargetIDs        []int64
}
