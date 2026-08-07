package inventoryapi

import (
	"context"
	"errors"

	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

var (
	ErrCredentialUnavailable = errors.New("credential is unavailable for this endpoint")
	ErrDiscoveryUnavailable  = errors.New("model discovery is unavailable for this endpoint")
	ErrDiscoveryFailed       = errors.New("upstream model discovery failed")
	ErrDiscoveryAuthFailed   = errors.New("upstream model discovery authentication failed")
	ErrDiscoveryForbidden    = errors.New("upstream credential cannot list models")
	ErrDiscoveryPayment      = errors.New("upstream credential has insufficient balance")
	ErrDiscoveryRateLimited  = errors.New("upstream model discovery was rate limited")
	ErrDiscoveryTimedOut     = errors.New("upstream model discovery timed out")
)

type CredentialCreate struct {
	Name    string
	Secret  []byte
	Enabled bool
}

type CredentialSecretUpdate struct {
	ExpectedRevision int64
	Secret           []byte
}

type CredentialRecord struct {
	Credential vnextstore.SiteCredential
	Runtime    vnextstore.CredentialRuntimeState
}

type TokenJSONImportItem struct {
	CredentialName string
	Secret         []byte
	EndpointName   string
	BaseURL        string
	WireProtocol   string
	Surface        string
	AdapterKind    string
	AuthScheme     string
}

type TokenJSONImportRecords struct {
	CredentialIDs []int64
	EndpointIDs   []int64
}

type ModelTargetCatalogEntry struct {
	Target       vnextstore.ProviderModelTargetInventory
	Capabilities vnextprotocol.Capabilities
	Routable     bool
}

type ModelDiscovery struct {
	Models   []string
	Complete bool
}

// ModelDiscoveryPreviewInput is deliberately store-independent. Secret exists
// only for the lifetime of one administrator request and must never be
// persisted, logged, or serialized by inventory implementations.
type ModelDiscoveryPreviewInput struct {
	BaseURL    string
	Protocol   vnextprotocol.Protocol
	Surface    vnextprotocol.Surface
	AuthScheme vnextprotocol.AuthScheme
	Secret     []byte
}

type Repository interface {
	ListSites(context.Context) ([]vnextstore.Site, error)
	CreateSite(context.Context, vnextstore.SiteWrite) (vnextstore.Site, error)
	GetSite(context.Context, int64) (vnextstore.Site, error)
	UpdateSite(context.Context, int64, vnextstore.SiteUpdate) (vnextstore.Site, error)
	DeleteSite(context.Context, int64, int64) error

	ListSiteEndpoints(context.Context, int64) ([]vnextstore.SiteEndpoint, error)
	CreateSiteEndpoint(context.Context, int64, vnextstore.SiteEndpointWrite) (vnextstore.SiteEndpoint, error)
	GetSiteEndpoint(context.Context, int64, int64) (vnextstore.SiteEndpoint, error)
	UpdateSiteEndpoint(context.Context, int64, int64, vnextstore.SiteEndpointUpdate) (vnextstore.SiteEndpoint, error)
	ListEndpointCredentialBindings(context.Context, int64, int64) ([]vnextstore.CredentialEndpointBinding, error)
	ReplaceEndpointCredentialBindings(context.Context, int64, int64, int64, []int64) (vnextstore.SiteEndpoint, error)

	ListSiteCredentials(context.Context, int64) ([]CredentialRecord, error)
	CreateSiteCredential(context.Context, int64, CredentialCreate) (CredentialRecord, error)
	GetSiteCredential(context.Context, int64, int64) (CredentialRecord, error)
	UpdateSiteCredential(context.Context, int64, int64, vnextstore.SiteCredentialUpdate) (CredentialRecord, error)
	ReplaceSiteCredentialSecret(context.Context, int64, int64, CredentialSecretUpdate) (CredentialRecord, error)
	ImportTokenJSONItems(context.Context, int64, []TokenJSONImportItem) (TokenJSONImportRecords, error)

	ListProviderModelTargets(context.Context, int64, int64) ([]vnextstore.ProviderModelTarget, error)
	CreateProviderModelTarget(context.Context, vnextstore.ProviderModelTargetWrite) (vnextstore.ProviderModelTarget, error)
	GetProviderModelTarget(context.Context, int64, int64, int64) (vnextstore.ProviderModelTarget, error)
	UpdateProviderModelTarget(context.Context, int64, int64, int64, vnextstore.ProviderModelTargetUpdate) (vnextstore.ProviderModelTarget, error)
	ImportProviderModelTargets(context.Context, int64, int64, int64, []string) ([]vnextstore.ProviderModelTarget, error)
	DiscoverModels(context.Context, int64, int64, int64) (ModelDiscovery, error)
	PreviewModels(context.Context, ModelDiscoveryPreviewInput) (ModelDiscovery, error)
	ListProviderModelTargetCatalog(context.Context) ([]ModelTargetCatalogEntry, error)
}
