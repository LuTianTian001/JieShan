package routingapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const maxRequestBytes = 64 << 10

var errTargetOutsidePublishedModel = errors.New("provider target is outside the published model default target set")

func routeSegments(path string) ([]string, bool) {
	if path == APIPrefix || path == APIPrefix+"/" {
		return []string{}, true
	}
	if !strings.HasPrefix(path, APIPrefix+"/") {
		return nil, false
	}
	rest := strings.TrimPrefix(path, APIPrefix+"/")
	if rest == "" || strings.HasSuffix(rest, "/") || strings.Contains(rest, "//") {
		return nil, false
	}
	return strings.Split(rest, "/"), true
}

func positiveID(writer http.ResponseWriter, raw, kind string) (int64, bool) {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		writeError(writer, http.StatusBadRequest, "invalid_request", fmt.Sprintf("%s ID must be a positive integer.", kind))
		return 0, false
	}
	return value, true
}

func requireCreateIfMatch(writer http.ResponseWriter, request *http.Request) bool {
	raw := strings.TrimSpace(request.Header.Get("If-Match"))
	if raw == "" {
		writeError(writer, http.StatusPreconditionRequired, "precondition_required", "If-Match: * is required when creating a routing profile.")
		return false
	}
	if raw != "*" {
		writeError(writer, http.StatusBadRequest, "invalid_revision", "If-Match must be * when creating a routing profile.")
		return false
	}
	return true
}

func requiredIfMatch(writer http.ResponseWriter, request *http.Request) (int64, bool) {
	raw := strings.TrimSpace(request.Header.Get("If-Match"))
	if raw == "" {
		writeError(writer, http.StatusPreconditionRequired, "precondition_required", "If-Match header is required.")
		return 0, false
	}
	if strings.Contains(raw, ",") || raw == "*" || strings.HasPrefix(raw, "W/") {
		writeError(writer, http.StatusBadRequest, "invalid_revision", "If-Match must contain one strong numeric revision.")
		return 0, false
	}
	if strings.HasPrefix(raw, `"`) || strings.HasSuffix(raw, `"`) {
		if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
			writeError(writer, http.StatusBadRequest, "invalid_revision", "If-Match must contain one strong numeric revision.")
			return 0, false
		}
		raw = raw[1 : len(raw)-1]
	}
	revision, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || revision <= 0 {
		writeError(writer, http.StatusBadRequest, "invalid_revision", "If-Match must contain one strong numeric revision.")
		return 0, false
	}
	return revision, true
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, destination any) bool {
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(request.Header.Get("Content-Type"), ";")[0]))
	if contentType != "application/json" {
		writeError(writer, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json.")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, maxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", "Request body must be a valid JSON object.")
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeError(writer, http.StatusBadRequest, "invalid_request", "Request body must contain exactly one JSON object.")
		return false
	}
	return true
}

func methodNotAllowed(writer http.ResponseWriter, methods ...string) {
	writer.Header().Set("Allow", strings.Join(methods, ", "))
	writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "Method is not allowed for this routing resource.")
}

func writeTargetMappingError(writer http.ResponseWriter, err error) {
	if errors.Is(err, errTargetOutsidePublishedModel) {
		writeError(writer, http.StatusBadRequest, "invalid_route_target", "Every custom route target must belong to the published model default target set.")
		return
	}
	writeRepositoryError(writer, err)
}

func writeRepositoryError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, vnextstore.ErrRevisionConflict):
		writeError(writer, http.StatusConflict, "revision_conflict", "Resource changed; refresh and try again.")
	case errors.Is(err, vnextstore.ErrDefaultRoutingProfile):
		writeError(writer, http.StatusConflict, "default_profile_immutable", "The default routing profile cannot be renamed, deleted, or overridden.")
	case errors.Is(err, vnextstore.ErrRoutingProfileInUse):
		writeError(writer, http.StatusConflict, "routing_profile_in_use", "Move downstream keys to another routing profile before deleting this one.")
	case errors.Is(err, vnextstore.ErrRoutingProfileRouteNotFound):
		writeError(writer, http.StatusNotFound, "route_override_not_found", "Routing profile override was not found.")
	case errors.Is(err, vnextstore.ErrPublishedTargetInUse):
		writeError(writer, http.StatusConflict, "route_target_in_use", "A custom routing profile still uses one of these targets.")
	case errors.Is(err, vnextstore.ErrConflict), isUniqueConstraint(err):
		writeError(writer, http.StatusConflict, "conflict", "Resource already exists or conflicts with current routing state.")
	case errors.Is(err, sql.ErrNoRows):
		writeError(writer, http.StatusNotFound, "not_found", "Routing resource was not found.")
	default:
		writeError(writer, http.StatusInternalServerError, "internal_error", "Routing request could not be completed.")
	}
}

func isUniqueConstraint(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint") || strings.Contains(message, "constraint failed: unique")
}

func writeProfile(writer http.ResponseWriter, status int, item vnextstore.RoutingProfile) {
	writer.Header().Set("ETag", revisionETag(item.Revision))
	writeJSON(writer, status, map[string]any{"item": newProfileResponse(item)})
}

func writeRoute(writer http.ResponseWriter, status int, item vnextstore.RoutingProfileRoute) {
	writer.Header().Set("ETag", revisionETag(item.Revision))
	writeJSON(writer, status, map[string]any{"item": newRouteResponse(item)})
}

func revisionETag(revision int64) string {
	if revision <= 0 {
		return ""
	}
	return strconv.Quote(strconv.FormatInt(revision, 10))
}

func writeNoContent(writer http.ResponseWriter) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(http.StatusNoContent)
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func nilLike(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
