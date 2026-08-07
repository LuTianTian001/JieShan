package routingapi

import (
	"context"
	"errors"
	"net/http"

	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

type Handler struct {
	repository Repository
}

func New(repository Repository) (*Handler, error) {
	if nilLike(repository) {
		return nil, errors.New("routing repository is required")
	}
	return &Handler{repository: repository}, nil
}

func NewStoreHandler(storage *vnextstore.Store) (*Handler, error) {
	if storage == nil {
		return nil, errors.New("routing store is required")
	}
	return New(storage)
}

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if handler == nil || nilLike(handler.repository) {
		writeError(writer, http.StatusServiceUnavailable, "routing_unavailable", "Routing management is unavailable.")
		return
	}
	segments, ok := routeSegments(request.URL.Path)
	if !ok {
		writeError(writer, http.StatusNotFound, "not_found", "Routing resource was not found.")
		return
	}
	if len(segments) == 0 {
		handler.handleProfiles(writer, request)
		return
	}
	profileID, ok := positiveID(writer, segments[0], "routing profile")
	if !ok {
		return
	}
	if len(segments) == 1 {
		handler.handleProfile(writer, request, profileID)
		return
	}
	if segments[1] != "routes" {
		writeError(writer, http.StatusNotFound, "not_found", "Routing resource was not found.")
		return
	}
	if len(segments) == 2 {
		handler.handleRoutes(writer, request, profileID)
		return
	}
	publishedModelID, ok := positiveID(writer, segments[2], "published model")
	if !ok {
		return
	}
	if len(segments) == 3 {
		handler.handleRoute(writer, request, profileID, publishedModelID)
		return
	}
	if len(segments) == 4 && segments[3] == "targets" {
		handler.handleRouteTargets(writer, request, profileID, publishedModelID)
		return
	}
	writeError(writer, http.StatusNotFound, "not_found", "Routing resource was not found.")
}

func (handler *Handler) handleProfiles(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		handler.listProfiles(writer, request)
	case http.MethodPost:
		handler.createProfile(writer, request)
	default:
		methodNotAllowed(writer, http.MethodGet, http.MethodPost)
	}
}

func (handler *Handler) handleProfile(writer http.ResponseWriter, request *http.Request, profileID int64) {
	switch request.Method {
	case http.MethodGet:
		handler.getProfile(writer, request, profileID)
	case http.MethodPatch:
		handler.updateProfile(writer, request, profileID)
	case http.MethodDelete:
		handler.deleteProfile(writer, request, profileID)
	default:
		methodNotAllowed(writer, http.MethodGet, http.MethodPatch, http.MethodDelete)
	}
}

func (handler *Handler) handleRoutes(writer http.ResponseWriter, request *http.Request, profileID int64) {
	switch request.Method {
	case http.MethodGet:
		handler.listRoutes(writer, request, profileID)
	case http.MethodPost:
		handler.createRoute(writer, request, profileID)
	default:
		methodNotAllowed(writer, http.MethodGet, http.MethodPost)
	}
}

func (handler *Handler) handleRoute(writer http.ResponseWriter, request *http.Request, profileID, modelID int64) {
	switch request.Method {
	case http.MethodGet:
		handler.getRoute(writer, request, profileID, modelID)
	case http.MethodPatch:
		handler.updateRoute(writer, request, profileID, modelID)
	case http.MethodDelete:
		handler.deleteRoute(writer, request, profileID, modelID)
	default:
		methodNotAllowed(writer, http.MethodGet, http.MethodPatch, http.MethodDelete)
	}
}

func (handler *Handler) handleRouteTargets(writer http.ResponseWriter, request *http.Request, profileID, modelID int64) {
	if request.Method != http.MethodPut {
		methodNotAllowed(writer, http.MethodPut)
		return
	}
	handler.replaceRouteTargets(writer, request, profileID, modelID)
}

func (handler *Handler) listProfiles(writer http.ResponseWriter, request *http.Request) {
	items, err := handler.repository.ListRoutingProfiles(request.Context())
	if err != nil {
		writeRepositoryError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": profileResponses(items)})
}

func (handler *Handler) getProfile(writer http.ResponseWriter, request *http.Request, profileID int64) {
	item, err := handler.repository.GetRoutingProfile(request.Context(), profileID)
	if err != nil {
		writeRepositoryError(writer, err)
		return
	}
	writeProfile(writer, http.StatusOK, item)
}

func (handler *Handler) createProfile(writer http.ResponseWriter, request *http.Request) {
	if !requireCreateIfMatch(writer, request) {
		return
	}
	var body profileRequest
	if !decodeJSON(writer, request, &body) {
		return
	}
	name, ok := body.createName(writer)
	if !ok {
		return
	}
	item, err := handler.repository.CreateRoutingProfile(request.Context(), name)
	if err != nil {
		writeRepositoryError(writer, err)
		return
	}
	writeProfile(writer, http.StatusCreated, item)
}

func (handler *Handler) updateProfile(writer http.ResponseWriter, request *http.Request, profileID int64) {
	revision, ok := requiredIfMatch(writer, request)
	if !ok {
		return
	}
	var body profileRequest
	if !decodeJSON(writer, request, &body) {
		return
	}
	name, ok := body.updateName(writer)
	if !ok {
		return
	}
	item, err := handler.repository.UpdateRoutingProfile(request.Context(), profileID, revision, name)
	if err != nil {
		writeRepositoryError(writer, err)
		return
	}
	writeProfile(writer, http.StatusOK, item)
}

func (handler *Handler) deleteProfile(writer http.ResponseWriter, request *http.Request, profileID int64) {
	revision, ok := requiredIfMatch(writer, request)
	if !ok {
		return
	}
	if err := handler.repository.DeleteRoutingProfile(request.Context(), profileID, revision); err != nil {
		writeRepositoryError(writer, err)
		return
	}
	writeNoContent(writer)
}

func (handler *Handler) listRoutes(writer http.ResponseWriter, request *http.Request, profileID int64) {
	profile, err := handler.repository.GetRoutingProfile(request.Context(), profileID)
	if err != nil {
		writeRepositoryError(writer, err)
		return
	}
	items, err := handler.repository.ListRoutingProfileRoutes(request.Context(), profileID)
	if err != nil {
		writeRepositoryError(writer, err)
		return
	}
	writer.Header().Set("ETag", revisionETag(profile.Revision))
	writeJSON(writer, http.StatusOK, map[string]any{"items": routeResponses(items)})
}

func (handler *Handler) getRoute(writer http.ResponseWriter, request *http.Request, profileID, modelID int64) {
	item, err := handler.repository.GetRoutingProfileRoute(request.Context(), profileID, modelID)
	if err != nil {
		writeRepositoryError(writer, err)
		return
	}
	writeRoute(writer, http.StatusOK, item)
}

func (handler *Handler) createRoute(writer http.ResponseWriter, request *http.Request, profileID int64) {
	profileRevision, ok := requiredIfMatch(writer, request)
	if !ok {
		return
	}
	profile, err := handler.repository.GetRoutingProfile(request.Context(), profileID)
	if err != nil {
		writeRepositoryError(writer, err)
		return
	}
	var body routeCreateRequest
	if !decodeJSON(writer, request, &body) {
		return
	}
	var item vnextstore.RoutingProfileRoute
	if profile.Default {
		write, providerTargetIDs, valid := body.defaultRoute(writer)
		if !valid {
			return
		}
		model, createErr := handler.repository.CreatePublishedModelCAS(
			request.Context(), profile.ID, profileRevision, write, providerTargetIDs,
		)
		if createErr != nil {
			writeRepositoryError(writer, createErr)
			return
		}
		item, err = handler.repository.GetRoutingProfileRoute(request.Context(), profile.ID, model.ID)
	} else {
		write, providerTargetIDs, targetsSet, valid := body.customRoute(writer)
		if !valid {
			return
		}
		if targetsSet {
			write.TargetIDs, err = handler.publishedTargetIDs(request.Context(), write.PublishedModelID, providerTargetIDs)
			if err != nil {
				writeTargetMappingError(writer, err)
				return
			}
		}
		item, err = handler.repository.CreateRoutingProfileRoute(request.Context(), profile.ID, profileRevision, write)
	}
	if err != nil {
		writeRepositoryError(writer, err)
		return
	}
	writeRoute(writer, http.StatusCreated, item)
}

func (handler *Handler) updateRoute(writer http.ResponseWriter, request *http.Request, profileID, modelID int64) {
	revision, ok := requiredIfMatch(writer, request)
	if !ok {
		return
	}
	profile, err := handler.repository.GetRoutingProfile(request.Context(), profileID)
	if err != nil {
		writeRepositoryError(writer, err)
		return
	}
	current, err := handler.repository.GetRoutingProfileRoute(request.Context(), profileID, modelID)
	if err != nil {
		writeRepositoryError(writer, err)
		return
	}
	var body routeUpdateRequest
	if !decodeJSON(writer, request, &body) {
		return
	}
	var item vnextstore.RoutingProfileRoute
	if profile.Default {
		input, valid := body.defaultUpdate(writer, current, revision)
		if !valid {
			return
		}
		if _, err = handler.repository.UpdatePublishedModel(request.Context(), modelID, input); err == nil {
			item, err = handler.repository.GetRoutingProfileRoute(request.Context(), profileID, modelID)
		}
	} else {
		enabled, valid := body.customUpdate(writer)
		if !valid {
			return
		}
		item, err = handler.repository.UpdateRoutingProfileRoute(request.Context(), profileID, modelID, revision, enabled)
	}
	if err != nil {
		writeRepositoryError(writer, err)
		return
	}
	writeRoute(writer, http.StatusOK, item)
}

func (handler *Handler) replaceRouteTargets(writer http.ResponseWriter, request *http.Request, profileID, modelID int64) {
	revision, ok := requiredIfMatch(writer, request)
	if !ok {
		return
	}
	profile, err := handler.repository.GetRoutingProfile(request.Context(), profileID)
	if err != nil {
		writeRepositoryError(writer, err)
		return
	}
	var body routeTargetsRequest
	if !decodeJSON(writer, request, &body) {
		return
	}
	providerTargetIDs, valid := body.validate(writer)
	if !valid {
		return
	}
	var item vnextstore.RoutingProfileRoute
	if profile.Default {
		if _, err = handler.repository.ReplacePublishedModelTargets(request.Context(), modelID, revision, providerTargetIDs); err == nil {
			item, err = handler.repository.GetRoutingProfileRoute(request.Context(), profileID, modelID)
		}
	} else {
		var publishedTargetIDs []int64
		publishedTargetIDs, err = handler.publishedTargetIDs(request.Context(), modelID, providerTargetIDs)
		if err != nil {
			writeTargetMappingError(writer, err)
			return
		}
		item, err = handler.repository.ReplaceRoutingProfileRouteTargets(
			request.Context(), profileID, modelID, revision, publishedTargetIDs,
		)
	}
	if err != nil {
		writeRepositoryError(writer, err)
		return
	}
	writeRoute(writer, http.StatusOK, item)
}

func (handler *Handler) deleteRoute(writer http.ResponseWriter, request *http.Request, profileID, modelID int64) {
	revision, ok := requiredIfMatch(writer, request)
	if !ok {
		return
	}
	profile, err := handler.repository.GetRoutingProfile(request.Context(), profileID)
	if err != nil {
		writeRepositoryError(writer, err)
		return
	}
	if profile.Default {
		err = handler.repository.DeletePublishedModel(request.Context(), modelID, revision)
	} else {
		err = handler.repository.DeleteRoutingProfileRoute(request.Context(), profileID, modelID, revision)
	}
	if err != nil {
		writeRepositoryError(writer, err)
		return
	}
	writeNoContent(writer)
}

func (handler *Handler) publishedTargetIDs(ctx context.Context, modelID int64, providerTargetIDs []int64) ([]int64, error) {
	defaultProfile, err := handler.repository.GetDefaultRoutingProfile(ctx)
	if err != nil {
		return nil, err
	}
	route, err := handler.repository.GetRoutingProfileRoute(ctx, defaultProfile.ID, modelID)
	if err != nil {
		return nil, err
	}
	available := make(map[int64]int64, len(route.Targets))
	for _, target := range route.Targets {
		available[target.ProviderModelTargetID] = target.ID
	}
	result := make([]int64, 0, len(providerTargetIDs))
	for _, providerTargetID := range providerTargetIDs {
		publishedTargetID, exists := available[providerTargetID]
		if !exists {
			return nil, errTargetOutsidePublishedModel
		}
		result = append(result, publishedTargetID)
	}
	return result, nil
}

var _ http.Handler = (*Handler)(nil)
