package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/LuTianTian001/JieShan/internal/store"
)

type discoveryTestResources struct {
	site        store.Site
	endpoint    store.InferenceEndpoint
	credentials []store.InferenceCredential
}

func TestV2ModelDiscoveryPersistsRunsForEveryStrategy(t *testing.T) {
	var mu sync.Mutex
	requests := map[string]int{}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Authorization")
		mu.Lock()
		requests[key]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch key {
		case "Bearer key-bad":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid key"}}`))
		case "Bearer key-good":
			_, _ = w.Write([]byte(`{"data":[{"id":"model-good"}]}`))
		case "Bearer key-later":
			_, _ = w.Write([]byte(`{"data":[{"id":"model-later"}]}`))
		default:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"unknown key"}}`))
		}
	}))
	defer provider.Close()

	fixture := newAPIContractFixture(t)
	loginDiscoveryFixture(t, fixture)
	resources := createDiscoveryResources(t, fixture, provider.URL, []discoveryCredentialInput{
		{name: "Bad", secret: "key-bad"},
		{name: "Good", secret: "key-good"},
		{name: "Later", secret: "key-later"},
	})

	selected := runDiscoveryRequest(t, fixture, resources.site.ID, http.StatusOK, map[string]any{
		"endpointId": resources.endpoint.ID, "credentialId": resources.credentials[1].ID, "strategy": "selected",
	})
	if !selected.Applied || !selected.Complete || selected.Run == nil || selected.Run.Status != "success" || len(selected.Attempts) != 1 {
		t.Fatalf("selected discovery = %+v", selected)
	}
	if selected.Attempts[0].CredentialID != resources.credentials[1].ID || selected.Models[0] != "model-good" {
		t.Fatalf("selected discovery used the wrong credential: %+v", selected)
	}

	firstSuccess := runDiscoveryRequest(t, fixture, resources.site.ID, http.StatusOK, map[string]any{
		"endpointId": resources.endpoint.ID, "strategy": "first_success",
	})
	if !firstSuccess.Applied || firstSuccess.Complete || firstSuccess.Run == nil || firstSuccess.Run.Status != "partial" {
		t.Fatalf("first-success discovery = %+v", firstSuccess)
	}
	if len(firstSuccess.Attempts) != 2 || firstSuccess.Attempts[0].Error == "" || firstSuccess.Attempts[1].Error != "" {
		t.Fatalf("first-success attempts = %+v", firstSuccess.Attempts)
	}

	all := runDiscoveryRequest(t, fixture, resources.site.ID, http.StatusOK, map[string]any{
		"endpointId": resources.endpoint.ID, "strategy": "all",
	})
	if !all.Applied || all.Complete || all.Run == nil || all.Run.Status != "partial" || len(all.Attempts) != 3 {
		t.Fatalf("all discovery = %+v", all)
	}
	if len(all.Models) != 2 || all.Models[0] != "model-good" || all.Models[1] != "model-later" {
		t.Fatalf("all discovery models = %+v", all.Models)
	}

	mu.Lock()
	badCalls := requests["Bearer key-bad"]
	goodCalls := requests["Bearer key-good"]
	laterCalls := requests["Bearer key-later"]
	mu.Unlock()
	if badCalls != 2 || goodCalls != 3 || laterCalls != 1 {
		t.Fatalf("provider calls bad=%d good=%d later=%d", badCalls, goodCalls, laterCalls)
	}

	resp, body := fixture.request(t, http.MethodGet,
		"/api/v2/sites/"+strconv.FormatInt(resources.site.ID, 10)+"/model-discoveries?limit=2", nil, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	runs := decodeContract[struct {
		Items []store.ModelDiscoveryRun `json:"items"`
	}](t, body).Items
	if len(runs) != 2 || runs[0].ID != all.Run.ID || runs[1].ID != firstSuccess.Run.ID {
		t.Fatalf("discovery run list = %+v", runs)
	}

	resp, body = fixture.request(t, http.MethodGet, "/api/v2/model-discoveries/"+firstSuccess.Run.ID, nil, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	detail := decodeContract[struct {
		Item     store.ModelDiscoveryRun       `json:"item"`
		Attempts []store.ModelDiscoveryAttempt `json:"attempts"`
	}](t, body)
	if detail.Item.ID != firstSuccess.Run.ID || len(detail.Attempts) != 2 || detail.Attempts[0].Status != "failed" || detail.Attempts[1].Status != "success" {
		t.Fatalf("discovery run detail = %+v", detail)
	}
	if detail.Attempts[0].ErrorClass != "authentication" {
		t.Fatalf("first attempt error class = %q", detail.Attempts[0].ErrorClass)
	}

	resp, body = fixture.request(t, http.MethodGet, "/api/v2/model-discoveries/"+all.Run.ID+"/attempts", nil, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	attempts := decodeContract[struct {
		Items []store.ModelDiscoveryAttempt `json:"items"`
	}](t, body).Items
	if len(attempts) != 3 {
		t.Fatalf("all discovery attempts = %+v", attempts)
	}

	models, err := fixture.store.ListEndpointModels(context.Background(), resources.endpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ModelName != "model-good" || models[1].ModelName != "model-later" {
		t.Fatalf("applied catalog = %+v", models)
	}
}

func TestV2ModelDiscoveryRevisionConflictPreservesCatalog(t *testing.T) {
	var fixture *apiContractFixture
	var endpointID int64
	var mutateOnce sync.Once
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mutateOnce.Do(func() {
			current, err := fixture.store.GetInferenceEndpoint(context.Background(), endpointID)
			if err != nil {
				t.Errorf("load endpoint during discovery: %v", err)
				return
			}
			if err := fixture.store.UpdateInferenceEndpoint(context.Background(), current.ID, current.Revision, store.InferenceEndpointWrite{
				Name: current.Name + " changed", BaseURL: current.BaseURL, WireProtocol: current.WireProtocol,
				CompatibilityProfile: current.CompatibilityProfile, AuthScheme: current.AuthScheme,
				CustomHeaders: current.CustomHeaders, Enabled: current.Enabled,
			}); err != nil {
				t.Errorf("mutate endpoint during discovery: %v", err)
			}
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"new-model"}]}`))
	}))
	defer provider.Close()

	fixture = newAPIContractFixture(t)
	loginDiscoveryFixture(t, fixture)
	resources := createDiscoveryResources(t, fixture, provider.URL, []discoveryCredentialInput{{name: "Only", secret: "key-only"}})
	endpointID = resources.endpoint.ID
	now := store.NowMS()
	if _, err := fixture.store.UpsertSiteModel(context.Background(), store.SiteModelWrite{
		SiteID: resources.site.ID, EndpointID: endpointID, ModelName: "existing-model", Enabled: true, LastSeenAt: &now,
	}); err != nil {
		t.Fatal(err)
	}

	conflict := runDiscoveryRequest(t, fixture, resources.site.ID, http.StatusConflict, map[string]any{
		"endpointId": endpointID, "credentialId": resources.credentials[0].ID, "strategy": "selected",
	})
	if conflict.Applied || conflict.Run == nil || conflict.Run.Status != "failed" || conflict.Error == "" {
		t.Fatalf("conflicted discovery = %+v", conflict)
	}
	var summary discoveryRunSummaryV2
	if err := json.Unmarshal(conflict.Run.Summary, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Outcome != "configuration_conflict" || summary.Applied {
		t.Fatalf("conflict summary = %+v", summary)
	}
	models, err := fixture.store.ListEndpointModels(context.Background(), endpointID)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ModelName != "existing-model" {
		t.Fatalf("conflict changed the catalog: %+v", models)
	}
}

func TestV2FailedModelDiscoveryPreservesCatalogAndAttempt(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"temporary upstream failure"}}`))
	}))
	defer provider.Close()

	fixture := newAPIContractFixture(t)
	loginDiscoveryFixture(t, fixture)
	resources := createDiscoveryResources(t, fixture, provider.URL, []discoveryCredentialInput{{name: "Only", secret: "key-only"}})
	now := store.NowMS()
	if _, err := fixture.store.UpsertSiteModel(context.Background(), store.SiteModelWrite{
		SiteID: resources.site.ID, EndpointID: resources.endpoint.ID, ModelName: "keep-model", Enabled: true, LastSeenAt: &now,
	}); err != nil {
		t.Fatal(err)
	}

	failed := runDiscoveryRequest(t, fixture, resources.site.ID, http.StatusBadGateway, map[string]any{
		"endpointId": resources.endpoint.ID, "credentialId": resources.credentials[0].ID, "strategy": "selected",
	})
	if failed.Applied || failed.Run == nil || failed.Run.Status != "failed" || len(failed.Attempts) != 1 || failed.Attempts[0].Error == "" {
		t.Fatalf("failed discovery = %+v", failed)
	}
	attempts, err := fixture.store.ListModelDiscoveryAttempts(context.Background(), failed.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Status != "failed" || attempts[0].ErrorClass != "upstream_error" {
		t.Fatalf("persisted failed attempt = %+v", attempts)
	}
	models, err := fixture.store.ListEndpointModels(context.Background(), resources.endpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ModelName != "keep-model" {
		t.Fatalf("failed discovery changed the catalog: %+v", models)
	}
}

type discoveryCredentialInput struct {
	name   string
	secret string
}

func loginDiscoveryFixture(t *testing.T, fixture *apiContractFixture) {
	t.Helper()
	resp, body := fixture.request(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"password": "correct horse battery staple",
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
}

func createDiscoveryResources(t *testing.T, fixture *apiContractFixture, baseURL string, credentials []discoveryCredentialInput) discoveryTestResources {
	t.Helper()
	resp, body := fixture.request(t, http.MethodPost, "/api/v2/sites", map[string]any{
		"name": "Discovery site", "enabled": true,
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusCreated)
	site := decodeContract[struct {
		Item store.Site `json:"item"`
	}](t, body).Item
	resp, body = fixture.request(t, http.MethodPost, "/api/v2/sites/"+strconv.FormatInt(site.ID, 10)+"/endpoints", map[string]any{
		"name": "Primary", "baseUrl": baseURL, "wireProtocol": "openai", "revision": site.Revision,
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusCreated)
	endpoint := decodeContract[struct {
		Item store.InferenceEndpoint `json:"item"`
	}](t, body).Item

	createdCredentials := make([]store.InferenceCredential, 0, len(credentials))
	for _, credential := range credentials {
		resp, body = fixture.request(t, http.MethodPost, "/api/v2/sites/"+strconv.FormatInt(site.ID, 10)+"/credentials", map[string]any{
			"name": credential.name, "apiKey": credential.secret, "enabled": true,
		}, nil)
		assertHTTPStatus(t, resp, body, http.StatusCreated)
		created := decodeContract[struct {
			Item store.InferenceCredential `json:"item"`
		}](t, body).Item
		createdCredentials = append(createdCredentials, created)
	}
	return discoveryTestResources{site: site, endpoint: endpoint, credentials: createdCredentials}
}

func runDiscoveryRequest(t *testing.T, fixture *apiContractFixture, siteID int64, wantStatus int, input map[string]any) discoveryResultV2 {
	t.Helper()
	resp, body := fixture.request(t, http.MethodPost,
		"/api/v2/sites/"+strconv.FormatInt(siteID, 10)+"/model-discoveries", input, nil)
	assertHTTPStatus(t, resp, body, wantStatus)
	return decodeContract[discoveryResultV2](t, body)
}
