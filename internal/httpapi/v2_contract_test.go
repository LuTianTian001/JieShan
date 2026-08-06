package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/LuTianTian001/JieShan/internal/accountsync"
	"github.com/LuTianTian001/JieShan/internal/store"
)

func TestV2SiteKeyPoolAndPublishedRouteContract(t *testing.T) {
	fixture := newAPIContractFixture(t)
	resp, body := fixture.request(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"password": "correct horse battery staple",
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)

	resp, body = fixture.request(t, http.MethodPost, "/api/v2/sites", map[string]any{
		"name": "Ciii", "dashboardUrl": "https://panel.example.test", "enabled": true,
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusCreated)
	createdSite := decodeContract[struct {
		Item store.Site `json:"item"`
	}](t, body).Item

	resp, body = fixture.request(t, http.MethodPost, "/api/v2/sites/"+strconv.FormatInt(createdSite.ID, 10)+"/endpoints", map[string]any{
		"name": "Primary", "baseUrl": "https://api.example.test", "wireProtocol": "openai", "revision": createdSite.Revision,
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusCreated)
	endpoint := decodeContract[struct {
		Item store.InferenceEndpoint `json:"item"`
	}](t, body).Item

	credentialIDs := make([]int64, 0, 2)
	for index, secret := range []string{"sk-upstream-one", "sk-upstream-two"} {
		resp, body = fixture.request(t, http.MethodPost, "/api/v2/sites/"+strconv.FormatInt(createdSite.ID, 10)+"/credentials", map[string]any{
			"name": "Key " + strconv.Itoa(index+1), "apiKey": secret,
		}, nil)
		assertHTTPStatus(t, resp, body, http.StatusCreated)
		if strings.Contains(string(body), secret) {
			t.Fatal("credential response exposed the API key")
		}
		credential := decodeContract[struct {
			Item store.InferenceCredential `json:"item"`
		}](t, body).Item
		credentialIDs = append(credentialIDs, credential.ID)
	}

	resp, body = fixture.request(t, http.MethodGet, "/api/v2/sites", nil, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	sites := decodeContract[struct {
		Items []store.SiteSummary `json:"items"`
	}](t, body).Items
	if len(sites) != 1 || sites[0].CredentialCount != 2 || sites[0].EnabledCredentialCount != 2 || sites[0].EndpointCount != 1 {
		t.Fatalf("site summary = %+v", sites)
	}

	resp, body = fixture.request(t, http.MethodGet, "/api/v2/sites/"+strconv.FormatInt(createdSite.ID, 10), nil, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	detail := decodeContract[struct {
		Item siteDetail `json:"item"`
	}](t, body).Item
	resp, body = fixture.request(t, http.MethodPut, "/api/v2/sites/"+strconv.FormatInt(createdSite.ID, 10)+"/credentials/order", map[string]any{
		"ids": []int64{credentialIDs[1], credentialIDs[0]}, "revision": detail.Site.Revision,
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)

	now := store.NowMS()
	modelID, err := fixture.store.UpsertSiteModel(context.Background(), store.SiteModelWrite{
		SiteID: createdSite.ID, EndpointID: endpoint.ID, ModelName: "provider-model", Enabled: true, LastSeenAt: &now,
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, body = fixture.request(t, http.MethodPost, "/api/v2/published-models", map[string]any{
		"publicName": "public-model", "officialPriceSku": "gpt-5.6-mini", "enabled": true, "monitorEnabled": true,
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusCreated)
	published := decodeContract[struct {
		Item store.PublishedModelRoute `json:"item"`
	}](t, body).Item
	resp, body = fixture.request(t, http.MethodPost, "/api/v2/published-models/"+strconv.FormatInt(published.ID, 10)+"/targets", map[string]any{
		"siteId": createdSite.ID, "endpointId": endpoint.ID, "siteModelId": modelID, "publishedRevision": published.Revision,
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusCreated)

	resolved, err := fixture.store.ResolvePublishedModel(context.Background(), "public-model", store.NowMS())
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Targets) != 1 || len(resolved.Targets[0].Credentials) != 2 || resolved.Targets[0].Credentials[0].ID != credentialIDs[1] {
		t.Fatalf("resolved route did not preserve site/key order: %+v", resolved)
	}
}

func TestV2SiteAccountContractKeepsManagementAuthSeparate(t *testing.T) {
	fixture := newAPIContractFixture(t)
	resp, body := fixture.request(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"password": "correct horse battery staple",
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)

	resp, body = fixture.request(t, http.MethodPost, "/api/v2/sites", map[string]any{
		"name": "Account Site", "dashboardUrl": "https://panel.example.test", "enabled": true,
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusCreated)
	site := decodeContract[struct {
		Item store.Site `json:"item"`
	}](t, body).Item
	path := "/api/v2/sites/" + strconv.FormatInt(site.ID, 10) + "/account"

	resp, body = fixture.request(t, http.MethodGet, path, nil, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	initial := decodeContract[struct {
		Account accountsync.AccountView `json:"account"`
	}](t, body).Account
	if initial.Configured || initial.DashboardURL != "https://panel.example.test" {
		t.Fatalf("initial site account = %+v", initial)
	}

	secret := "management-token-that-must-not-leak"
	resp, body = fixture.request(t, http.MethodPut, path, map[string]any{
		"adapterKey": "new_api", "dashboardUrl": "https://panel.example.test", "enabled": true,
		"auth": map[string]any{"kind": "api_token", "apiToken": secret}, "refreshNow": false,
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	if strings.Contains(string(body), secret) {
		t.Fatal("site account response exposed management credentials")
	}
	configured := decodeContract[struct {
		Account accountsync.AccountView `json:"account"`
	}](t, body).Account
	if !configured.Configured || configured.Adapter == nil || configured.Adapter.Key != "new_api" {
		t.Fatalf("configured site account = %+v", configured)
	}
	credentials, err := fixture.store.ListInferenceCredentials(context.Background(), site.ID)
	if err != nil || len(credentials) != 0 {
		t.Fatalf("site account created inference credentials: %+v, %v", credentials, err)
	}

	resp, body = fixture.request(t, http.MethodDelete, path, nil, nil)
	assertHTTPStatus(t, resp, body, http.StatusNoContent)
}

func assertHTTPStatus(t *testing.T, response *http.Response, body []byte, expected int) {
	t.Helper()
	if response.StatusCode != expected {
		t.Fatalf("unexpected status: got=%d want=%d body=%s", response.StatusCode, expected, body)
	}
}
