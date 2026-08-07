package routingapi

import vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"

type profileResponse struct {
	ID                  int64  `json:"id"`
	Name                string `json:"name"`
	IsDefault           bool   `json:"isDefault"`
	Revision            int64  `json:"revision"`
	ModelCount          int    `json:"modelCount"`
	LocalModelCount     int    `json:"localModelCount"`
	InheritedModelCount int    `json:"inheritedModelCount"`
	DownstreamKeyCount  int    `json:"downstreamKeyCount"`
	CreatedAt           int64  `json:"createdAt"`
	UpdatedAt           int64  `json:"updatedAt"`
}

type routeResponse struct {
	RoutingProfileID       int64                 `json:"routingProfileId"`
	RoutingProfileName     string                `json:"routingProfileName"`
	SourceProfileID        int64                 `json:"sourceProfileId"`
	SourceProfileName      string                `json:"sourceProfileName"`
	Inherited              bool                  `json:"inherited"`
	TargetsOverridden      bool                  `json:"targetsOverridden"`
	PublishedModelID       int64                 `json:"publishedModelId"`
	PublicName             string                `json:"publicName"`
	OfficialPriceSKU       string                `json:"officialPriceSku"`
	Enabled                bool                  `json:"enabled"`
	PublishedModelRevision int64                 `json:"publishedModelRevision"`
	Revision               int64                 `json:"revision"`
	Targets                []routeTargetResponse `json:"targets"`
	CreatedAt              int64                 `json:"createdAt"`
	UpdatedAt              int64                 `json:"updatedAt"`
}

type routeTargetResponse struct {
	ID                    int64  `json:"id"`
	PublishedModelID      int64  `json:"publishedModelId"`
	SiteID                int64  `json:"siteId"`
	SiteName              string `json:"siteName"`
	EndpointID            int64  `json:"endpointId"`
	EndpointName          string `json:"endpointName"`
	ProviderModelTargetID int64  `json:"providerModelTargetId"`
	SourceModel           string `json:"sourceModel"`
	WireProtocol          string `json:"wireProtocol"`
	APISurface            string `json:"apiSurface"`
	Position              int    `json:"position"`
	Revision              int64  `json:"revision"`
	CreatedAt             int64  `json:"createdAt"`
	UpdatedAt             int64  `json:"updatedAt"`
}

func profileResponses(items []vnextstore.RoutingProfile) []profileResponse {
	result := make([]profileResponse, 0, len(items))
	for _, item := range items {
		result = append(result, newProfileResponse(item))
	}
	return result
}

func newProfileResponse(item vnextstore.RoutingProfile) profileResponse {
	return profileResponse{
		ID: item.ID, Name: item.Name, IsDefault: item.Default, Revision: item.Revision,
		ModelCount: item.ModelCount, LocalModelCount: item.LocalModelCount,
		InheritedModelCount: item.InheritedModelCount, DownstreamKeyCount: item.DownstreamKeyCount,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

func routeResponses(items []vnextstore.RoutingProfileRoute) []routeResponse {
	result := make([]routeResponse, 0, len(items))
	for _, item := range items {
		result = append(result, newRouteResponse(item))
	}
	return result
}

func newRouteResponse(item vnextstore.RoutingProfileRoute) routeResponse {
	targets := make([]routeTargetResponse, 0, len(item.Targets))
	for _, target := range item.Targets {
		targets = append(targets, routeTargetResponse{
			ID: target.ID, PublishedModelID: target.PublishedModelID,
			SiteID: target.SiteID, SiteName: target.SiteName, EndpointID: target.EndpointID,
			EndpointName: target.EndpointName, ProviderModelTargetID: target.ProviderModelTargetID,
			SourceModel: target.SourceModel, WireProtocol: target.WireProtocol, APISurface: target.Surface,
			Position: target.Position, Revision: target.Revision, CreatedAt: target.CreatedAt, UpdatedAt: target.UpdatedAt,
		})
	}
	return routeResponse{
		RoutingProfileID: item.RoutingProfileID, RoutingProfileName: item.RoutingProfileName,
		SourceProfileID: item.SourceProfileID, SourceProfileName: item.SourceProfileName,
		Inherited: item.Inherited, TargetsOverridden: item.TargetsOverridden,
		PublishedModelID: item.PublishedModelID, PublicName: item.PublicName,
		OfficialPriceSKU: item.OfficialPriceSKU, Enabled: item.Enabled,
		PublishedModelRevision: item.PublishedModelRevision, Revision: item.Revision,
		Targets: targets, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}
