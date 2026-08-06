package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/LuTianTian001/JieShan/internal/store"
)

func TestV2NativeEndpointIsDiscoveryOnlyAndRejectedByRoutes(t *testing.T) {
	fixture := newAPIContractFixture(t)
	resp, body := fixture.request(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"password": "correct horse battery staple",
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)

	resp, body = fixture.request(t, http.MethodPost, "/api/v2/sites", map[string]any{
		"name": "Native", "dashboardUrl": "https://native.example.test", "enabled": true,
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusCreated)
	site := decodeContract[struct {
		Item store.Site `json:"item"`
	}](t, body).Item

	resp, body = fixture.request(t, http.MethodPost, "/api/v2/sites/"+strconv.FormatInt(site.ID, 10)+"/endpoints", map[string]any{
		"name": "Anthropic", "baseUrl": "https://api.anthropic.com", "wireProtocol": "anthropic", "revision": site.Revision,
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusCreated)
	endpoint := decodeContract[struct {
		Item store.InferenceEndpoint `json:"item"`
	}](t, body).Item
	if !endpoint.Capabilities.ModelDiscovery || endpoint.Capabilities.RouteEligible || endpoint.AuthScheme != "x-api-key" {
		t.Fatalf("native endpoint contract = %+v", endpoint)
	}
	modelID, err := fixture.store.CreateSiteModel(context.Background(), store.SiteModelWrite{
		SiteID: site.ID, EndpointID: endpoint.ID, ModelName: "claude-native", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, body = fixture.request(t, http.MethodPost, "/api/v2/published-models", map[string]any{
		"publicName": "public-native", "enabled": true,
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusCreated)
	published := decodeContract[struct {
		Item store.PublishedModelRoute `json:"item"`
	}](t, body).Item

	resp, body = fixture.request(t, http.MethodPost, "/api/v2/published-models/"+strconv.FormatInt(published.ID, 10)+"/targets", map[string]any{
		"siteId": site.ID, "endpointId": endpoint.ID, "siteModelId": modelID, "publishedRevision": published.Revision,
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusBadRequest)
	if !strings.Contains(string(body), "model discovery only") {
		t.Fatalf("route rejection body = %s", body)
	}

	resp, body = fixture.request(t, http.MethodPost, "/api/v2/sites", map[string]any{
		"name": "Other", "dashboardUrl": "https://other.example.test", "enabled": true,
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusCreated)
	otherSite := decodeContract[struct {
		Item store.Site `json:"item"`
	}](t, body).Item
	resp, body = fixture.request(t, http.MethodPost, "/api/v2/sites/"+strconv.FormatInt(otherSite.ID, 10)+"/endpoints", map[string]any{
		"name": "Compatible", "baseUrl": "https://other.example.test/v1", "wireProtocol": "compatible", "revision": otherSite.Revision,
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusCreated)
	otherEndpoint := decodeContract[struct {
		Item store.InferenceEndpoint `json:"item"`
	}](t, body).Item
	otherModelID, err := fixture.store.CreateSiteModel(context.Background(), store.SiteModelWrite{
		SiteID: otherSite.ID, EndpointID: otherEndpoint.ID, ModelName: "other-model", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, body = fixture.request(t, http.MethodPost, "/api/v2/published-models/"+strconv.FormatInt(published.ID, 10)+"/targets", map[string]any{
		"siteId": site.ID, "endpointId": otherEndpoint.ID, "siteModelId": otherModelID, "publishedRevision": published.Revision,
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusBadRequest)
	if !strings.Contains(string(body), "same site and endpoint") {
		t.Fatalf("relationship rejection body = %s", body)
	}
}
