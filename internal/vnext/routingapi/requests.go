package routingapi

import (
	"encoding/json"
	"net/http"
	"strings"

	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

type optional[T any] struct {
	Set   bool
	Null  bool
	Value T
}

func (value *optional[T]) UnmarshalJSON(data []byte) error {
	value.Set = true
	if string(data) == "null" {
		value.Null = true
		return nil
	}
	return json.Unmarshal(data, &value.Value)
}

type profileRequest struct {
	Name optional[string] `json:"name"`
}

func (body profileRequest) createName(writer http.ResponseWriter) (string, bool) {
	return requiredName(writer, body.Name)
}

func (body profileRequest) updateName(writer http.ResponseWriter) (string, bool) {
	if !body.Name.Set {
		writeError(writer, http.StatusBadRequest, "invalid_request", "At least one mutable profile field is required.")
		return "", false
	}
	return requiredName(writer, body.Name)
}

func requiredName(writer http.ResponseWriter, value optional[string]) (string, bool) {
	name := strings.TrimSpace(value.Value)
	if !value.Set || value.Null || name == "" {
		writeError(writer, http.StatusBadRequest, "invalid_request", "Routing profile name is required.")
		return "", false
	}
	if len(name) > 120 {
		writeError(writer, http.StatusBadRequest, "invalid_request", "Routing profile name must not exceed 120 bytes.")
		return "", false
	}
	if strings.EqualFold(name, "Default") {
		writeError(writer, http.StatusBadRequest, "invalid_request", "Default is reserved for the default routing profile.")
		return "", false
	}
	return name, true
}

type routeCreateRequest struct {
	PublishedModelID  optional[int64]   `json:"publishedModelId"`
	PublicName        optional[string]  `json:"publicName"`
	OfficialPriceSKU  optional[string]  `json:"officialPriceSku"`
	Enabled           optional[bool]    `json:"enabled"`
	ProviderTargetIDs optional[[]int64] `json:"providerTargetIds"`
}

func (body routeCreateRequest) defaultRoute(writer http.ResponseWriter) (vnextstore.PublishedModelWrite, []int64, bool) {
	if body.PublishedModelID.Set {
		writeError(writer, http.StatusBadRequest, "invalid_request", "Default routes create a new published model and must not include publishedModelId.")
		return vnextstore.PublishedModelWrite{}, nil, false
	}
	publicName, ok := requiredRouteString(writer, body.PublicName, "publicName")
	if !ok {
		return vnextstore.PublishedModelWrite{}, nil, false
	}
	priceSKU, ok := requiredRouteString(writer, body.OfficialPriceSKU, "officialPriceSku")
	if !ok {
		return vnextstore.PublishedModelWrite{}, nil, false
	}
	if !body.Enabled.Set || body.Enabled.Null {
		writeError(writer, http.StatusBadRequest, "invalid_request", "enabled is required and must be a boolean.")
		return vnextstore.PublishedModelWrite{}, nil, false
	}
	providerTargetIDs, ok := requiredProviderTargetIDs(writer, body.ProviderTargetIDs)
	if !ok {
		return vnextstore.PublishedModelWrite{}, nil, false
	}
	return vnextstore.PublishedModelWrite{
		PublicName: publicName, OfficialPriceSKU: priceSKU, Enabled: body.Enabled.Value,
	}, providerTargetIDs, true
}

func (body routeCreateRequest) customRoute(
	writer http.ResponseWriter,
) (vnextstore.RoutingProfileRouteWrite, []int64, bool, bool) {
	if body.PublicName.Set || body.OfficialPriceSKU.Set {
		writeError(writer, http.StatusBadRequest, "invalid_request", "Custom routes inherit published model identity and pricing from the default profile.")
		return vnextstore.RoutingProfileRouteWrite{}, nil, false, false
	}
	if !body.PublishedModelID.Set || body.PublishedModelID.Null || body.PublishedModelID.Value <= 0 {
		writeError(writer, http.StatusBadRequest, "invalid_request", "publishedModelId must be a positive integer.")
		return vnextstore.RoutingProfileRouteWrite{}, nil, false, false
	}
	if !body.Enabled.Set || body.Enabled.Null {
		writeError(writer, http.StatusBadRequest, "invalid_request", "enabled is required and must be a boolean.")
		return vnextstore.RoutingProfileRouteWrite{}, nil, false, false
	}
	var providerTargetIDs []int64
	targetsSet := body.ProviderTargetIDs.Set
	if targetsSet {
		var ok bool
		providerTargetIDs, ok = requiredProviderTargetIDs(writer, body.ProviderTargetIDs)
		if !ok {
			return vnextstore.RoutingProfileRouteWrite{}, nil, false, false
		}
	}
	return vnextstore.RoutingProfileRouteWrite{
		PublishedModelID: body.PublishedModelID.Value, Enabled: body.Enabled.Value,
	}, providerTargetIDs, targetsSet, true
}

type routeUpdateRequest struct {
	PublicName       optional[string] `json:"publicName"`
	OfficialPriceSKU optional[string] `json:"officialPriceSku"`
	Enabled          optional[bool]   `json:"enabled"`
}

func (body routeUpdateRequest) defaultUpdate(
	writer http.ResponseWriter,
	current vnextstore.RoutingProfileRoute,
	revision int64,
) (vnextstore.PublishedModelUpdate, bool) {
	if !body.PublicName.Set && !body.OfficialPriceSKU.Set && !body.Enabled.Set {
		writeError(writer, http.StatusBadRequest, "invalid_request", "At least one mutable route field is required.")
		return vnextstore.PublishedModelUpdate{}, false
	}
	result := vnextstore.PublishedModelUpdate{
		ExpectedRevision: revision,
		PublicName:       current.PublicName,
		OfficialPriceSKU: current.OfficialPriceSKU,
		Enabled:          current.Enabled,
	}
	if body.PublicName.Set {
		value, ok := requiredRouteString(writer, body.PublicName, "publicName")
		if !ok {
			return vnextstore.PublishedModelUpdate{}, false
		}
		result.PublicName = value
	}
	if body.OfficialPriceSKU.Set {
		value, ok := requiredRouteString(writer, body.OfficialPriceSKU, "officialPriceSku")
		if !ok {
			return vnextstore.PublishedModelUpdate{}, false
		}
		result.OfficialPriceSKU = value
	}
	if body.Enabled.Set {
		if body.Enabled.Null {
			writeError(writer, http.StatusBadRequest, "invalid_request", "enabled must be a boolean.")
			return vnextstore.PublishedModelUpdate{}, false
		}
		result.Enabled = body.Enabled.Value
	}
	return result, true
}

func (body routeUpdateRequest) customUpdate(writer http.ResponseWriter) (bool, bool) {
	if body.PublicName.Set || body.OfficialPriceSKU.Set {
		writeError(writer, http.StatusBadRequest, "invalid_request", "Custom routes cannot rename or reprice a published model.")
		return false, false
	}
	if !body.Enabled.Set || body.Enabled.Null {
		writeError(writer, http.StatusBadRequest, "invalid_request", "enabled is required and must be a boolean.")
		return false, false
	}
	return body.Enabled.Value, true
}

type routeTargetsRequest struct {
	ProviderTargetIDs optional[[]int64] `json:"providerTargetIds"`
}

func (body routeTargetsRequest) validate(writer http.ResponseWriter) ([]int64, bool) {
	return requiredProviderTargetIDs(writer, body.ProviderTargetIDs)
}

func requiredRouteString(writer http.ResponseWriter, value optional[string], field string) (string, bool) {
	trimmed := strings.TrimSpace(value.Value)
	if !value.Set || value.Null || trimmed == "" {
		writeError(writer, http.StatusBadRequest, "invalid_request", field+" is required and must be a non-empty string.")
		return "", false
	}
	if len(trimmed) > 255 {
		writeError(writer, http.StatusBadRequest, "invalid_request", field+" must not exceed 255 bytes.")
		return "", false
	}
	return trimmed, true
}

func requiredProviderTargetIDs(writer http.ResponseWriter, value optional[[]int64]) ([]int64, bool) {
	if !value.Set || value.Null || len(value.Value) == 0 {
		writeError(writer, http.StatusBadRequest, "invalid_request", "providerTargetIds must contain at least one target.")
		return nil, false
	}
	seen := make(map[int64]struct{}, len(value.Value))
	result := append([]int64(nil), value.Value...)
	for _, id := range result {
		if id <= 0 {
			writeError(writer, http.StatusBadRequest, "invalid_request", "providerTargetIds must contain positive integers.")
			return nil, false
		}
		if _, duplicate := seen[id]; duplicate {
			writeError(writer, http.StatusBadRequest, "invalid_request", "providerTargetIds must not contain duplicates.")
			return nil, false
		}
		seen[id] = struct{}{}
	}
	return result, true
}
