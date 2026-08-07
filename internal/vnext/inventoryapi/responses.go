package inventoryapi

import (
	"encoding/json"

	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

type siteResponse struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	DashboardURL string `json:"dashboardUrl"`
	Enabled      bool   `json:"enabled"`
	Revision     int64  `json:"revision"`
	CreatedAt    int64  `json:"createdAt"`
	UpdatedAt    int64  `json:"updatedAt"`
}

func newSiteResponse(item vnextstore.Site) siteResponse {
	return siteResponse(item)
}

type endpointResponse struct {
	ID                      int64           `json:"id"`
	SiteID                  int64           `json:"siteId"`
	Name                    string          `json:"name"`
	BaseURL                 string          `json:"baseUrl"`
	WireProtocol            string          `json:"wireProtocol"`
	Surface                 string          `json:"surface"`
	AdapterKind             string          `json:"adapterKind"`
	AuthScheme              string          `json:"authScheme"`
	Headers                 json.RawMessage `json:"headers"`
	SecretHeadersConfigured bool            `json:"secretHeadersConfigured"`
	Position                int             `json:"position"`
	Enabled                 bool            `json:"enabled"`
	Revision                int64           `json:"revision"`
	CreatedAt               int64           `json:"createdAt"`
	UpdatedAt               int64           `json:"updatedAt"`
}

func newEndpointResponse(item vnextstore.SiteEndpoint) endpointResponse {
	headers := append(json.RawMessage(nil), item.HeaderTemplate...)
	if len(headers) == 0 {
		headers = json.RawMessage(`{}`)
	}
	return endpointResponse{
		ID: item.ID, SiteID: item.SiteID, Name: item.Name, BaseURL: item.BaseURL, WireProtocol: item.WireProtocol,
		Surface: item.Surface, AdapterKind: item.AdapterKind, AuthScheme: item.AuthScheme, Headers: headers,
		SecretHeadersConfigured: item.SecretHeadersConfigured, Position: item.Position, Enabled: item.Enabled,
		Revision: item.Revision, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

type credentialResponse struct {
	ID               int64  `json:"id"`
	SiteID           int64  `json:"siteId"`
	Name             string `json:"name"`
	SecretConfigured bool   `json:"secretConfigured"`
	Enabled          bool   `json:"enabled"`
	Revision         int64  `json:"revision"`
	RuntimeState     string `json:"runtimeState"`
	CoolingUntil     *int64 `json:"coolingUntil"`
	LastHTTPStatus   *int   `json:"lastHttpStatus"`
	LastErrorCode    string `json:"lastErrorCode"`
	RuntimeRevision  int64  `json:"runtimeRevision"`
	RuntimeUpdatedAt int64  `json:"runtimeUpdatedAt"`
	CreatedAt        int64  `json:"createdAt"`
	UpdatedAt        int64  `json:"updatedAt"`
}

func newCredentialResponse(record CredentialRecord) credentialResponse {
	item := record.Credential
	runtimeState := record.Runtime
	return credentialResponse{
		ID: item.ID, SiteID: item.SiteID, Name: item.Name, SecretConfigured: item.SecretConfigured,
		Enabled: item.Enabled, Revision: item.Revision, RuntimeState: runtimeState.State,
		CoolingUntil: cloneInt64(runtimeState.CoolingUntil), LastHTTPStatus: cloneInt(runtimeState.LastHTTPStatus),
		LastErrorCode: runtimeState.LastErrorCode, RuntimeRevision: runtimeState.Revision,
		RuntimeUpdatedAt: runtimeState.UpdatedAt, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

type bindingResponse struct {
	CredentialID   int64  `json:"credentialId"`
	CredentialName string `json:"credentialName"`
	Position       int    `json:"position"`
	Enabled        bool   `json:"enabled"`
	CreatedAt      int64  `json:"createdAt"`
	UpdatedAt      int64  `json:"updatedAt"`
}

func newBindingResponse(item vnextstore.CredentialEndpointBinding) bindingResponse {
	return bindingResponse{
		CredentialID: item.CredentialID, CredentialName: item.CredentialName, Position: item.Position,
		Enabled: item.Enabled, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

type providerModelResponse struct {
	ID          int64  `json:"id"`
	SiteID      int64  `json:"siteId"`
	EndpointID  int64  `json:"endpointId"`
	SourceModel string `json:"sourceModel"`
	DisplayName string `json:"displayName"`
	Enabled     bool   `json:"enabled"`
	Revision    int64  `json:"revision"`
	LastSeenAt  *int64 `json:"lastSeenAt"`
	CreatedAt   int64  `json:"createdAt"`
	UpdatedAt   int64  `json:"updatedAt"`
}

func newProviderModelResponse(item vnextstore.ProviderModelTarget) providerModelResponse {
	return providerModelResponse{
		ID: item.ID, SiteID: item.SiteID, EndpointID: item.EndpointID, SourceModel: item.SourceModel,
		DisplayName: item.DisplayName, Enabled: item.Enabled, Revision: item.Revision,
		LastSeenAt: cloneInt64(item.LastSeenAt), CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

type modelTargetCatalogResponse struct {
	providerModelResponse
	SiteName               string                     `json:"siteName"`
	SiteEnabled            bool                       `json:"siteEnabled"`
	EndpointName           string                     `json:"endpointName"`
	EndpointEnabled        bool                       `json:"endpointEnabled"`
	BaseURL                string                     `json:"baseUrl"`
	WireProtocol           string                     `json:"wireProtocol"`
	Surface                string                     `json:"surface"`
	AdapterKind            string                     `json:"adapterKind"`
	AuthScheme             string                     `json:"authScheme"`
	BoundCredentialCount   int                        `json:"boundCredentialCount"`
	UsableCredentialCount  int                        `json:"usableCredentialCount"`
	UnknownCredentialCount int                        `json:"unknownCredentialCount"`
	Capabilities           vnextprotocol.Capabilities `json:"capabilities"`
	Routable               bool                       `json:"routable"`
}

func newModelTargetCatalogResponse(entry ModelTargetCatalogEntry) modelTargetCatalogResponse {
	item := entry.Target
	return modelTargetCatalogResponse{
		providerModelResponse:  newProviderModelResponse(item.ProviderModelTarget),
		SiteName:               item.SiteName,
		SiteEnabled:            item.SiteEnabled,
		EndpointName:           item.EndpointName,
		EndpointEnabled:        item.EndpointEnabled,
		BaseURL:                item.BaseURL,
		WireProtocol:           item.WireProtocol,
		Surface:                item.Surface,
		AdapterKind:            item.AdapterKind,
		AuthScheme:             item.AuthScheme,
		BoundCredentialCount:   item.BoundCredentialCount,
		UsableCredentialCount:  item.UsableCredentialCount,
		UnknownCredentialCount: item.UnknownCredentialCount,
		Capabilities:           entry.Capabilities,
		Routable:               entry.Routable,
	}
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
