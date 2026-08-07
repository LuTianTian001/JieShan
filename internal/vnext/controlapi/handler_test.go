package controlapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/LuTianTian001/JieShan/internal/vnext/downstreamkeys"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

func TestDownstreamKeyCollectionCreateAndUpdateContract(t *testing.T) {
	quota := int64(9_000_000_000)
	hourlyQuota := int64(2_000_000_000)
	expires := int64(1_800_000_000_000)
	repository := newFakeRepository()
	repository.keys[7] = vnextstore.DownstreamKey{
		ID: 7, Name: "Personal", KeyPrefix: "js_existing", Enabled: true, Revealable: true,
		RoutingProfileID: 70, RoutingProfileName: "Default", UsesDefaultRoutingProfile: true,
		QuotaNanoUSD: &quota, UsedNanoUSD: 1_500, ReservedNanoUSD: 200,
		HourlyQuotaNanoUSD: &hourlyQuota, UsedThisHourNanoUSD: 300, ReservedThisHourNanoUSD: 40,
		HourlyWindowStartedAt: 1_799_998_400_000, BillingMultiplierBPS: 12_500,
		ExpiresAt: &expires, Revision: 4, CreatedAt: 100, UpdatedAt: 200,
	}
	repository.routes[70] = []vnextstore.RoutingProfileRoute{
		profileRoute(31, 70, "claude-sonnet-4-5", true, 3, true),
		profileRoute(32, 70, "disabled-model", false, 1, true),
		profileRoute(33, 70, "target-missing", true, 1, false),
	}
	issuer := &fakeIssuer{issued: IssuedKey{
		Key: vnextstore.DownstreamKey{
			ID: 8, Name: "Team", KeyPrefix: "js_created", Enabled: false, Revealable: true,
			RoutingProfileID: 70, RoutingProfileName: "Default", UsesDefaultRoutingProfile: true,
			QuotaNanoUSD: int64Pointer(5_000), HourlyQuotaNanoUSD: int64Pointer(1_000),
			BillingMultiplierBPS: 15_000, ExpiresAt: int64Pointer(1_900_000_000_000),
			Revision: 1, CreatedAt: 300, UpdatedAt: 300,
		},
		RawSecret: "js_one_time_secret",
	}}
	handler := New(repository, issuer, issuer)

	response := performRequest(handler, http.MethodGet, apiPrefix, "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("GET keys status = %d, body = %s", response.Code, response.Body.String())
	}
	var list struct {
		Items []struct {
			ID     int64    `json:"id"`
			Models []string `json:"models"`
		}
	}
	decodeResponse(t, response, &list)
	if len(list.Items) != 1 || list.Items[0].ID != 7 {
		t.Fatalf("GET keys items = %+v", list.Items)
	}
	if want := []string{"claude-sonnet-4-5"}; !reflect.DeepEqual(list.Items[0].Models, want) {
		t.Fatalf("key models = %v, want %v", list.Items[0].Models, want)
	}
	if !strings.Contains(response.Body.String(), `"billingMultiplierBPS":12500`) ||
		!strings.Contains(response.Body.String(), `"reservedThisHourNanoUSD":40`) {
		t.Fatalf("GET keys billing contract = %s", response.Body.String())
	}

	response = performRequest(handler, http.MethodPost, apiPrefix,
		`{"name":"  Team  ","quotaNanoUSD":5000,"hourlyQuotaNanoUSD":1000,"billingMultiplierBPS":15000,"expires":1900000000000,"enabled":false}`, "")
	if response.Code != http.StatusCreated {
		t.Fatalf("POST key status = %d, body = %s", response.Code, response.Body.String())
	}
	if issuer.calls != 1 {
		t.Fatalf("issuer calls = %d, want 1", issuer.calls)
	}
	if issuer.input.Name != "Team" || issuer.input.BillingMultiplierBPS != 15_000 || issuer.input.Enabled {
		t.Fatalf("issuer input = %+v", issuer.input)
	}
	if issuer.input.QuotaNanoUSD == nil || *issuer.input.QuotaNanoUSD != 5_000 {
		t.Fatalf("issuer quota = %v", issuer.input.QuotaNanoUSD)
	}
	if issuer.input.HourlyQuotaNanoUSD == nil || *issuer.input.HourlyQuotaNanoUSD != 1_000 {
		t.Fatalf("issuer hourly quota = %v", issuer.input.HourlyQuotaNanoUSD)
	}
	if issuer.input.Expires == nil || *issuer.input.Expires != 1_900_000_000_000 {
		t.Fatalf("issuer expires = %v", issuer.input.Expires)
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("secret response cache headers = %v", response.Header())
	}
	if response.Header().Get("ETag") != `"1"` {
		t.Fatalf("create ETag = %q", response.Header().Get("ETag"))
	}
	if !strings.Contains(response.Body.String(), `"secret":"js_one_time_secret"`) {
		t.Fatalf("create response does not contain the one-time secret: %s", response.Body.String())
	}

	response = performRequest(handler, http.MethodPost, apiPrefix,
		`{"name":"Bad","allowedModels":["gpt-5"]}`, "")
	assertError(t, response, http.StatusBadRequest, "invalid_request")
	if issuer.calls != 1 {
		t.Fatalf("issuer called for an unknown field; calls = %d", issuer.calls)
	}
	response = performRequest(handler, http.MethodGet, apiPrefix+"/7", "", "")
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"4"` {
		t.Fatalf("GET key status/ETag = %d/%q, body = %s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}

	response = performRequest(handler, http.MethodPatch, apiPrefix+"/7", `{"enabled":false}`, "")
	assertError(t, response, http.StatusPreconditionRequired, "precondition_required")

	response = performRequest(handler, http.MethodPatch, apiPrefix+"/7", `{"enabled":false}`, `"3"`)
	assertError(t, response, http.StatusConflict, "revision_conflict")
	if repository.lastUpdate.ExpectedRevision != 0 {
		t.Fatalf("stale PATCH reached repository update: %+v", repository.lastUpdate)
	}

	response = performRequest(handler, http.MethodPatch, apiPrefix+"/7",
		`{"name":"  Renamed  ","quotaNanoUSD":null,"hourlyQuotaNanoUSD":null,"billingMultiplierBPS":8000,"expires":null,"enabled":false}`, `"4"`)
	if response.Code != http.StatusOK {
		t.Fatalf("PATCH key status = %d, body = %s", response.Code, response.Body.String())
	}
	if repository.lastUpdate.ExpectedRevision != 4 || repository.lastUpdate.Name != "Renamed" ||
		repository.lastUpdate.QuotaNanoUSD != nil || repository.lastUpdate.HourlyQuotaNanoUSD != nil ||
		repository.lastUpdate.BillingMultiplierBPS != 8_000 ||
		repository.lastUpdate.Expires != nil || repository.lastUpdate.Enabled {
		t.Fatalf("update input = %+v", repository.lastUpdate)
	}
	if response.Header().Get("ETag") != `"5"` {
		t.Fatalf("update ETag = %q", response.Header().Get("ETag"))
	}
	body := response.Body.String()
	for _, forbidden := range []string{"keyDigest", "encryptedSecret", "rawSecret", "allowedModels"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("update response leaks forbidden field %q: %s", forbidden, body)
		}
	}
}

func TestDownstreamKeySecretLifecycleContract(t *testing.T) {
	repository := newFakeRepository()
	repository.keys[7] = vnextstore.DownstreamKey{
		ID: 7, Name: "Personal", KeyPrefix: "js_existing", Revealable: true, Enabled: true,
		RoutingProfileID: 70, RoutingProfileName: "Default", UsesDefaultRoutingProfile: true,
		Revision: 4, CreatedAt: 100, UpdatedAt: 200,
	}
	repository.routes[70] = []vnextstore.RoutingProfileRoute{
		profileRoute(31, 70, "gpt-5", true, 1, true),
	}
	secrets := &fakeIssuer{revealedSecret: "js_revealed_secret"}
	secrets.rotateIssued = IssuedKey{
		Key: vnextstore.DownstreamKey{
			ID: 7, Name: "Personal", KeyPrefix: "js_rotated", Revealable: true, Enabled: true,
			RoutingProfileID: 70, RoutingProfileName: "Default", UsesDefaultRoutingProfile: true,
			Revision: 5, CreatedAt: 100, UpdatedAt: 300,
		},
		RawSecret: "js_rotated_secret",
	}
	handler := New(repository, secrets, secrets)

	reveal := performRequest(handler, http.MethodPost, apiPrefix+"/7/reveal", "", "")
	if reveal.Code != http.StatusOK || secrets.revealID != 7 ||
		reveal.Header().Get("Cache-Control") != "no-store" || reveal.Header().Get("Pragma") != "no-cache" ||
		!strings.Contains(reveal.Body.String(), `"secret":"js_revealed_secret"`) {
		t.Fatalf("reveal response = %d %v %s", reveal.Code, reveal.Header(), reveal.Body.String())
	}

	secrets.revealErr = downstreamkeys.ErrRecentReauthenticationRequired
	response := performRequest(handler, http.MethodPost, apiPrefix+"/7/reveal", "", "")
	assertError(t, response, http.StatusForbidden, "recent_reauthentication_required")
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("reauth error cache headers = %v", response.Header())
	}
	secrets.revealErr = downstreamkeys.ErrNotRevealable
	response = performRequest(handler, http.MethodPost, apiPrefix+"/7/reveal", "", "")
	assertError(t, response, http.StatusConflict, "key_not_revealable")
	secrets.revealErr = nil

	response = performRequest(handler, http.MethodPost, apiPrefix+"/7/rotate", "", "")
	assertError(t, response, http.StatusPreconditionRequired, "precondition_required")
	secrets.rotateErr = vnextstore.ErrRevisionConflict
	response = performRequest(handler, http.MethodPost, apiPrefix+"/7/rotate", "", `"3"`)
	assertError(t, response, http.StatusConflict, "revision_conflict")
	if secrets.rotateID != 7 || secrets.rotateRevision != 3 {
		t.Fatalf("rotate precondition = id %d revision %d", secrets.rotateID, secrets.rotateRevision)
	}

	secrets.rotateErr = nil
	response = performRequest(handler, http.MethodPost, apiPrefix+"/7/rotate", "", `"4"`)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"5"` ||
		response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Pragma") != "no-cache" ||
		!strings.Contains(response.Body.String(), `"secret":"js_rotated_secret"`) ||
		!strings.Contains(response.Body.String(), `"models":["gpt-5"]`) {
		t.Fatalf("rotate response = %d %v %s", response.Code, response.Header(), response.Body.String())
	}
}

func TestLegacyPerKeyRouteEndpointsAreNotExposed(t *testing.T) {
	repository := newFakeRepository()
	repository.keys[7] = vnextstore.DownstreamKey{
		ID: 7, Name: "Personal", RoutingProfileID: 70, RoutingProfileName: "Default",
		UsesDefaultRoutingProfile: true, Revision: 1,
	}
	secrets := &fakeIssuer{}
	handler := New(repository, secrets, secrets)

	response := performRequest(handler, http.MethodGet, apiPrefix+"/7/routes", "", "")
	assertError(t, response, http.StatusNotFound, "not_found")
	response = performRequest(handler, http.MethodPut, apiPrefix+"/7/routes/31/targets", `{"targetIds":[90,80]}`, `"6"`)
	assertError(t, response, http.StatusNotFound, "not_found")
}

func TestModelsExposeOnlyEnabledEffectiveProfileRoutesWithTargets(t *testing.T) {
	repository := newFakeRepository()
	repository.keys[2] = vnextstore.DownstreamKey{
		ID: 2, Name: "Client", RoutingProfileID: 20, RoutingProfileName: "Default",
		UsesDefaultRoutingProfile: true, Revision: 1,
	}
	repository.routes[20] = []vnextstore.RoutingProfileRoute{
		profileRoute(10, 20, "gpt-5", true, 2, true),
		profileRoute(11, 20, "disabled-route", false, 1, true),
		profileRoute(12, 20, "no-target", true, 1, false),
		{PublishedModelID: 13, RoutingProfileID: 20, PublicName: "empty", Enabled: true, Revision: 1},
		{PublishedModelID: 14, RoutingProfileID: 20, PublicName: "", Enabled: true, Revision: 1,
			Targets: []vnextstore.PublishedModelTarget{{ID: 140}}},
	}
	secrets := &fakeIssuer{}
	handler := New(repository, secrets, secrets)

	response := performRequest(handler, http.MethodGet, apiPrefix+"/2/models", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("GET models status = %d, body = %s", response.Code, response.Body.String())
	}
	var result struct {
		Items []modelResponse `json:"items"`
	}
	decodeResponse(t, response, &result)
	if len(result.Items) != 1 || result.Items[0].ID != "gpt-5" || result.Items[0].EnabledTargetCount != 1 {
		t.Fatalf("models = %+v", result.Items)
	}

	response = performRequest(handler, http.MethodGet, apiPrefix+"/999/models", "", "")
	assertError(t, response, http.StatusNotFound, "not_found")
}

func TestControlAPIErrorsAreJSONAndDoNotLeakRepositoryDetails(t *testing.T) {
	repository := newFakeRepository()
	repository.listErr = errors.New("SQLITE_ERROR near key_digest: secret=js_should_never_escape")
	secrets := &fakeIssuer{}
	handler := New(repository, secrets, secrets)

	response := performRequest(handler, http.MethodGet, apiPrefix, "", "")
	assertError(t, response, http.StatusInternalServerError, "internal_error")
	for _, secret := range []string{"SQLITE_ERROR", "key_digest", "js_should_never_escape"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("internal error leaked %q: %s", secret, response.Body.String())
		}
	}
	if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("error content type = %q", got)
	}

	response = performRequest(handler, http.MethodDelete, apiPrefix, "", "")
	assertError(t, response, http.StatusMethodNotAllowed, "method_not_allowed")
	if response.Header().Get("Allow") != "GET, POST" {
		t.Fatalf("Allow = %q", response.Header().Get("Allow"))
	}

	response = performRequest(handler, http.MethodGet, "/api/vnext/not-a-resource", "", "")
	assertError(t, response, http.StatusNotFound, "not_found")

	repository.listErr = nil
	response = performRequest(handler, http.MethodPost, apiPrefix, `{"name":"Client"} {}`, "")
	assertError(t, response, http.StatusBadRequest, "invalid_request")
}

type fakeIssuer struct {
	issued IssuedKey
	err    error
	input  KeyCreate
	calls  int

	revealedSecret string
	revealErr      error
	revealID       int64
	rotateIssued   IssuedKey
	rotateErr      error
	rotateID       int64
	rotateRevision int64
}

func (issuer *fakeIssuer) IssueDownstreamKey(_ context.Context, input KeyCreate) (IssuedKey, error) {
	issuer.calls++
	issuer.input = input
	return issuer.issued, issuer.err
}

func (issuer *fakeIssuer) RevealDownstreamKey(_ context.Context, id int64) (string, error) {
	issuer.revealID = id
	return issuer.revealedSecret, issuer.revealErr
}

func (issuer *fakeIssuer) RotateDownstreamKey(_ context.Context, id, revision int64) (IssuedKey, error) {
	issuer.rotateID = id
	issuer.rotateRevision = revision
	return issuer.rotateIssued, issuer.rotateErr
}

type fakeRepository struct {
	keys   map[int64]vnextstore.DownstreamKey
	routes map[int64][]vnextstore.RoutingProfileRoute

	listErr   error
	getErr    error
	updateErr error

	lastUpdate KeyUpdate
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{
		keys:   make(map[int64]vnextstore.DownstreamKey),
		routes: make(map[int64][]vnextstore.RoutingProfileRoute),
	}
}

func (repository *fakeRepository) ListDownstreamKeys(context.Context) ([]vnextstore.DownstreamKey, error) {
	if repository.listErr != nil {
		return nil, repository.listErr
	}
	items := make([]vnextstore.DownstreamKey, 0, len(repository.keys))
	for id := int64(1); id < 10_000; id++ {
		if item, exists := repository.keys[id]; exists {
			items = append(items, item)
		}
	}
	return items, nil
}

func (repository *fakeRepository) GetDownstreamKey(_ context.Context, id int64) (vnextstore.DownstreamKey, error) {
	if repository.getErr != nil {
		return vnextstore.DownstreamKey{}, repository.getErr
	}
	item, exists := repository.keys[id]
	if !exists {
		return vnextstore.DownstreamKey{}, sql.ErrNoRows
	}
	return item, nil
}

func (repository *fakeRepository) UpdateDownstreamKey(_ context.Context, id int64, input KeyUpdate) (vnextstore.DownstreamKey, error) {
	repository.lastUpdate = input
	if repository.updateErr != nil {
		return vnextstore.DownstreamKey{}, repository.updateErr
	}
	item, exists := repository.keys[id]
	if !exists {
		return vnextstore.DownstreamKey{}, sql.ErrNoRows
	}
	if item.Revision != input.ExpectedRevision {
		return vnextstore.DownstreamKey{}, vnextstore.ErrRevisionConflict
	}
	item.Name = input.Name
	if input.RoutingProfileID != nil {
		item.RoutingProfileID = *input.RoutingProfileID
		item.RoutingProfileName = "Custom"
		item.UsesDefaultRoutingProfile = false
	}
	item.QuotaNanoUSD = cloneInt64(input.QuotaNanoUSD)
	item.HourlyQuotaNanoUSD = cloneInt64(input.HourlyQuotaNanoUSD)
	item.BillingMultiplierBPS = input.BillingMultiplierBPS
	item.ExpiresAt = cloneInt64(input.Expires)
	item.Enabled = input.Enabled
	item.Revision++
	item.UpdatedAt++
	repository.keys[id] = item
	return item, nil
}

func (repository *fakeRepository) ListRoutingProfileRoutes(_ context.Context, profileID int64) ([]vnextstore.RoutingProfileRoute, error) {
	items := repository.routes[profileID]
	return append([]vnextstore.RoutingProfileRoute(nil), items...), nil
}

func profileRoute(id, profileID int64, model string, enabled bool, revision int64, withTarget bool) vnextstore.RoutingProfileRoute {
	item := vnextstore.RoutingProfileRoute{
		PublishedModelID: id, PublishedModelRevision: 1, RoutingProfileID: profileID,
		RoutingProfileName: "Default", SourceProfileID: profileID, SourceProfileName: "Default",
		PublicName: model, OfficialPriceSKU: model, Enabled: enabled, Revision: revision,
		CreatedAt: 10, UpdatedAt: 20,
	}
	if withTarget {
		item.Targets = []vnextstore.PublishedModelTarget{{
			ID: id + 100, PublishedModelID: id, SiteID: 5, SiteName: "Upstream",
			EndpointID: 6, EndpointName: "Default", ProviderModelTargetID: id + 200,
			SourceModel: model, Position: 0, Revision: 1,
		}}
	}
	return item
}

func performRequest(handler http.Handler, method, path, body, ifMatch string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, status, response.Body.String())
	}
	var result struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	decodeResponse(t, response, &result)
	if result.Error.Code != code || result.Error.Message == "" {
		t.Fatalf("error = %+v, want code %q", result.Error, code)
	}
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, destination any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), destination); err != nil {
		t.Fatalf("decode response %q: %v", response.Body.String(), err)
	}
}

func int64Pointer(value int64) *int64 {
	return &value
}
