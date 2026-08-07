package routingapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

func TestStoreHandlerImplementsDefaultAndSparseProfileRouting(t *testing.T) {
	storage := openRoutingStore(t)
	handler, err := NewStoreHandler(storage)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	targetA := createRoutingProviderTarget(t, storage, "Alpha", "https://alpha.example/v1", "shared-model")
	targetB := createRoutingProviderTarget(t, storage, "Beta", "https://beta.example/v1", "shared-model")
	targetOutside := createRoutingProviderTarget(t, storage, "Outside", "https://outside.example/v1", "outside-model")

	profiles := serveRouting(t, handler, http.MethodGet, APIPrefix, nil, "")
	if profiles.Code != http.StatusOK {
		t.Fatalf("initial profiles = %d %s", profiles.Code, profiles.Body.String())
	}
	var profileList struct {
		Items []profileResponse `json:"items"`
	}
	decodeRoutingResponse(t, profiles, &profileList)
	if len(profileList.Items) != 1 || !profileList.Items[0].IsDefault || profileList.Items[0].Revision != 1 {
		t.Fatalf("initial profile list = %+v", profileList.Items)
	}
	defaultProfileID := profileList.Items[0].ID

	createdProfile := serveRouting(t, handler, http.MethodPost, APIPrefix, map[string]any{"name": "Priority B"}, "*")
	if createdProfile.Code != http.StatusCreated || createdProfile.Header().Get("ETag") != `"1"` {
		t.Fatalf("create profile = %d %q %s", createdProfile.Code, createdProfile.Header().Get("ETag"), createdProfile.Body.String())
	}
	custom := decodeProfileItem(t, createdProfile)
	if custom.IsDefault || custom.Name != "Priority B" {
		t.Fatalf("created custom profile = %+v", custom)
	}

	renamedProfile := serveRouting(t, handler, http.MethodPatch, APIPrefix+"/"+idText(custom.ID),
		map[string]any{"name": "Priority B renamed"}, `"1"`)
	if renamedProfile.Code != http.StatusOK || renamedProfile.Header().Get("ETag") != `"2"` {
		t.Fatalf("rename profile = %d %q %s", renamedProfile.Code, renamedProfile.Header().Get("ETag"), renamedProfile.Body.String())
	}
	custom = decodeProfileItem(t, renamedProfile)

	defaultCreate := serveRouting(t, handler, http.MethodPost,
		APIPrefix+"/"+idText(defaultProfileID)+"/routes",
		map[string]any{
			"publicName": "public-shared", "officialPriceSku": "official-shared", "enabled": true,
			"providerTargetIds": []int64{targetB, targetA},
		}, `"1"`)
	if defaultCreate.Code != http.StatusCreated || defaultCreate.Header().Get("ETag") != `"1"` {
		t.Fatalf("create default route = %d %q %s", defaultCreate.Code, defaultCreate.Header().Get("ETag"), defaultCreate.Body.String())
	}
	defaultRoute := decodeRouteItem(t, defaultCreate)
	assertRouteProviderOrder(t, defaultRoute, []int64{targetB, targetA})
	if defaultRoute.Inherited || defaultRoute.TargetsOverridden || defaultRoute.PublicName != "public-shared" {
		t.Fatalf("created default route = %+v", defaultRoute)
	}

	defaultUpdate := serveRouting(t, handler, http.MethodPatch,
		routePath(defaultProfileID, defaultRoute.PublishedModelID),
		map[string]any{"publicName": "public-renamed", "enabled": false}, `"1"`)
	if defaultUpdate.Code != http.StatusOK || defaultUpdate.Header().Get("ETag") != `"2"` {
		t.Fatalf("update default route = %d %q %s", defaultUpdate.Code, defaultUpdate.Header().Get("ETag"), defaultUpdate.Body.String())
	}
	defaultRoute = decodeRouteItem(t, defaultUpdate)
	if defaultRoute.PublicName != "public-renamed" || defaultRoute.Enabled {
		t.Fatalf("updated default route = %+v", defaultRoute)
	}

	defaultTargets := serveRouting(t, handler, http.MethodPut,
		routePath(defaultProfileID, defaultRoute.PublishedModelID)+"/targets",
		map[string]any{"providerTargetIds": []int64{targetA, targetB}}, `"2"`)
	if defaultTargets.Code != http.StatusOK || defaultTargets.Header().Get("ETag") != `"3"` {
		t.Fatalf("replace default targets = %d %q %s", defaultTargets.Code, defaultTargets.Header().Get("ETag"), defaultTargets.Body.String())
	}
	defaultRoute = decodeRouteItem(t, defaultTargets)
	assertRouteProviderOrder(t, defaultRoute, []int64{targetA, targetB})

	inheritedResponse := serveRouting(t, handler, http.MethodGet,
		routePath(custom.ID, defaultRoute.PublishedModelID), nil, "")
	if inheritedResponse.Code != http.StatusOK {
		t.Fatalf("get inherited route = %d %s", inheritedResponse.Code, inheritedResponse.Body.String())
	}
	inherited := decodeRouteItem(t, inheritedResponse)
	if !inherited.Inherited || inherited.SourceProfileID != defaultProfileID || inherited.Revision != defaultRoute.Revision {
		t.Fatalf("inherited route = %+v", inherited)
	}
	assertRouteProviderOrder(t, inherited, []int64{targetA, targetB})

	customCreate := serveRouting(t, handler, http.MethodPost, APIPrefix+"/"+idText(custom.ID)+"/routes",
		map[string]any{"publishedModelId": defaultRoute.PublishedModelID, "enabled": false}, `"2"`)
	if customCreate.Code != http.StatusCreated || customCreate.Header().Get("ETag") != `"1"` {
		t.Fatalf("create sparse route = %d %q %s", customCreate.Code, customCreate.Header().Get("ETag"), customCreate.Body.String())
	}
	customRoute := decodeRouteItem(t, customCreate)
	if customRoute.Inherited || customRoute.TargetsOverridden || customRoute.Enabled {
		t.Fatalf("sparse enabled override = %+v", customRoute)
	}
	assertRouteProviderOrder(t, customRoute, []int64{targetA, targetB})

	customUpdate := serveRouting(t, handler, http.MethodPatch,
		routePath(custom.ID, defaultRoute.PublishedModelID), map[string]any{"enabled": true}, `"1"`)
	if customUpdate.Code != http.StatusOK || customUpdate.Header().Get("ETag") != `"2"` {
		t.Fatalf("update sparse route = %d %q %s", customUpdate.Code, customUpdate.Header().Get("ETag"), customUpdate.Body.String())
	}
	customRoute = decodeRouteItem(t, customUpdate)
	if !customRoute.Enabled {
		t.Fatalf("enabled sparse route = %+v", customRoute)
	}

	customTargets := serveRouting(t, handler, http.MethodPut,
		routePath(custom.ID, defaultRoute.PublishedModelID)+"/targets",
		map[string]any{"providerTargetIds": []int64{targetB}}, `"2"`)
	if customTargets.Code != http.StatusOK || customTargets.Header().Get("ETag") != `"3"` {
		t.Fatalf("replace sparse targets = %d %q %s", customTargets.Code, customTargets.Header().Get("ETag"), customTargets.Body.String())
	}
	customRoute = decodeRouteItem(t, customTargets)
	if !customRoute.TargetsOverridden {
		t.Fatalf("target override = %+v", customRoute)
	}
	assertRouteProviderOrder(t, customRoute, []int64{targetB})

	restore := serveRouting(t, handler, http.MethodDelete,
		routePath(custom.ID, defaultRoute.PublishedModelID), nil, `"3"`)
	if restore.Code != http.StatusNoContent || restore.Body.Len() != 0 {
		t.Fatalf("restore inheritance = %d %s", restore.Code, restore.Body.String())
	}
	restored := decodeRouteItem(t, serveRouting(t, handler, http.MethodGet,
		routePath(custom.ID, defaultRoute.PublishedModelID), nil, ""))
	if !restored.Inherited {
		t.Fatalf("restored route = %+v", restored)
	}

	invalidTarget := serveRouting(t, handler, http.MethodPut,
		routePath(custom.ID, defaultRoute.PublishedModelID)+"/targets",
		map[string]any{"providerTargetIds": []int64{targetOutside}}, revisionETag(restored.Revision))
	if invalidTarget.Code != http.StatusBadRequest || !bytes.Contains(invalidTarget.Body.Bytes(), []byte(`"invalid_route_target"`)) {
		t.Fatalf("outside target = %d %s", invalidTarget.Code, invalidTarget.Body.String())
	}

	staleDelete := serveRouting(t, handler, http.MethodDelete,
		routePath(defaultProfileID, defaultRoute.PublishedModelID), nil, `"2"`)
	if staleDelete.Code != http.StatusConflict {
		t.Fatalf("stale model delete = %d %s", staleDelete.Code, staleDelete.Body.String())
	}
	deleteDefault := serveRouting(t, handler, http.MethodDelete,
		routePath(defaultProfileID, defaultRoute.PublishedModelID), nil, `"3"`)
	if deleteDefault.Code != http.StatusNoContent {
		t.Fatalf("delete default route = %d %s", deleteDefault.Code, deleteDefault.Body.String())
	}
	emptyCustom := serveRouting(t, handler, http.MethodGet, APIPrefix+"/"+idText(custom.ID)+"/routes", nil, "")
	var emptyRoutes struct {
		Items []routeResponse `json:"items"`
	}
	decodeRoutingResponse(t, emptyCustom, &emptyRoutes)
	if len(emptyRoutes.Items) != 0 {
		t.Fatalf("routes after published model delete = %+v", emptyRoutes.Items)
	}

	customStore, err := storage.GetRoutingProfile(ctx, custom.ID)
	if err != nil {
		t.Fatal(err)
	}
	deleteProfile := serveRouting(t, handler, http.MethodDelete, APIPrefix+"/"+idText(custom.ID), nil,
		revisionETag(customStore.Revision))
	if deleteProfile.Code != http.StatusNoContent {
		t.Fatalf("delete custom profile = %d %s", deleteProfile.Code, deleteProfile.Body.String())
	}
}

func TestStoreHandlerEnforcesPreconditionsAndProfileKinds(t *testing.T) {
	storage := openRoutingStore(t)
	handler, err := NewStoreHandler(storage)
	if err != nil {
		t.Fatal(err)
	}
	defaultProfile, err := storage.GetDefaultRoutingProfile(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	missingCreatePrecondition := serveRouting(t, handler, http.MethodPost, APIPrefix, map[string]any{"name": "Missing"}, "")
	if missingCreatePrecondition.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing create precondition = %d %s", missingCreatePrecondition.Code, missingCreatePrecondition.Body.String())
	}
	invalidCreatePrecondition := serveRouting(t, handler, http.MethodPost, APIPrefix, map[string]any{"name": "Invalid"}, `"1"`)
	if invalidCreatePrecondition.Code != http.StatusBadRequest {
		t.Fatalf("invalid create precondition = %d %s", invalidCreatePrecondition.Code, invalidCreatePrecondition.Body.String())
	}
	defaultRename := serveRouting(t, handler, http.MethodPatch, APIPrefix+"/"+idText(defaultProfile.ID),
		map[string]any{"name": "Renamed"}, revisionETag(defaultProfile.Revision))
	if defaultRename.Code != http.StatusConflict || !bytes.Contains(defaultRename.Body.Bytes(), []byte(`"default_profile_immutable"`)) {
		t.Fatalf("default rename = %d %s", defaultRename.Code, defaultRename.Body.String())
	}
	missingContentType := httptest.NewRequest(http.MethodPost, APIPrefix, bytes.NewBufferString(`{"name":"No type"}`))
	missingContentType.Header.Set("If-Match", "*")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, missingContentType)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("missing content type = %d %s", response.Code, response.Body.String())
	}
	unknownField := serveRouting(t, handler, http.MethodPost, APIPrefix, map[string]any{"name": "Unknown", "extra": true}, "*")
	if unknownField.Code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d %s", unknownField.Code, unknownField.Body.String())
	}
}

func TestStoreHandlerRefusesToDeleteProfileAssignedToDownstreamKeys(t *testing.T) {
	storage := openRoutingStore(t)
	handler, err := NewStoreHandler(storage)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	profile, err := storage.CreateRoutingProfile(ctx, "Pinned client route")
	if err != nil {
		t.Fatal(err)
	}
	profileID := profile.ID
	if _, err := storage.ImportDigestOnlyDownstreamKey(ctx, vnextstore.DownstreamKeyWrite{
		Name: "Pinned client", KeyPrefix: "js_pinned", KeyDigest: vnextstore.DigestDownstreamKey("pinned-secret"),
		RoutingProfileID: &profileID, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	response := serveRouting(t, handler, http.MethodDelete, APIPrefix+"/"+idText(profile.ID), nil,
		revisionETag(profile.Revision))
	if response.Code != http.StatusConflict ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"routing_profile_in_use"`)) {
		t.Fatalf("delete assigned profile = %d %s", response.Code, response.Body.String())
	}
	if _, err := storage.GetRoutingProfile(ctx, profile.ID); err != nil {
		t.Fatalf("assigned profile was deleted: %v", err)
	}
}

func openRoutingStore(t *testing.T) *vnextstore.Store {
	t.Helper()
	storage, err := vnextstore.Open(context.Background(), filepath.Join(t.TempDir(), "routing.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	return storage
}

func createRoutingProviderTarget(t *testing.T, storage *vnextstore.Store, name, baseURL, sourceModel string) int64 {
	t.Helper()
	ctx := context.Background()
	siteID, err := storage.CreateSite(ctx, vnextstore.SiteWrite{Name: name, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	endpointID, err := storage.CreateSiteEndpoint(ctx, siteID, vnextstore.SiteEndpointWrite{
		Name: name + " endpoint", BaseURL: baseURL, WireProtocol: "openai",
		Surface: "openai.chat_completions", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	targetID, err := storage.CreateProviderModelTarget(ctx, vnextstore.ProviderModelTargetWrite{
		SiteID: siteID, EndpointID: endpointID, SourceModel: sourceModel, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return targetID
}

func serveRouting(
	t *testing.T,
	handler http.Handler,
	method, path string,
	body any,
	ifMatch string,
) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(payload))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeProfileItem(t *testing.T, response *httptest.ResponseRecorder) profileResponse {
	t.Helper()
	var envelope struct {
		Item profileResponse `json:"item"`
	}
	decodeRoutingResponse(t, response, &envelope)
	return envelope.Item
}

func decodeRouteItem(t *testing.T, response *httptest.ResponseRecorder) routeResponse {
	t.Helper()
	if response.Code != http.StatusOK && response.Code != http.StatusCreated {
		t.Fatalf("route response = %d %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Item routeResponse `json:"item"`
	}
	decodeRoutingResponse(t, response, &envelope)
	return envelope.Item
}

func decodeRoutingResponse(t *testing.T, response *httptest.ResponseRecorder, destination any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), destination); err != nil {
		t.Fatalf("decode %d response %q: %v", response.Code, response.Body.String(), err)
	}
}

func assertRouteProviderOrder(t *testing.T, route routeResponse, want []int64) {
	t.Helper()
	if len(route.Targets) != len(want) {
		t.Fatalf("route targets = %+v, want provider IDs %v", route.Targets, want)
	}
	for index, id := range want {
		if route.Targets[index].ProviderModelTargetID != id || route.Targets[index].Position != index {
			t.Fatalf("route target[%d] = %+v, want provider ID %d", index, route.Targets[index], id)
		}
	}
}

func routePath(profileID, modelID int64) string {
	return APIPrefix + "/" + idText(profileID) + "/routes/" + idText(modelID)
}

func idText(id int64) string {
	return strconv.FormatInt(id, 10)
}
