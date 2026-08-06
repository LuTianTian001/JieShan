package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/LuTianTian001/JieShan/internal/store"
)

func TestV2ProbeAndMonitorQueryContracts(t *testing.T) {
	fixture := newAPIContractFixture(t)
	resp, body := fixture.request(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"password": "correct horse battery staple",
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)

	ctx := context.Background()
	siteID, err := fixture.store.CreateSite(ctx, store.SiteWrite{Name: "Probe API", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	site, _ := fixture.store.GetSite(ctx, siteID)
	endpointID, err := fixture.store.CreateInferenceEndpoint(ctx, siteID, site.Revision, store.InferenceEndpointWrite{
		Name: "Primary", BaseURL: "https://example.invalid", WireProtocol: "openai", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	site, _ = fixture.store.GetSite(ctx, siteID)
	credentialID, err := fixture.store.CreateInferenceCredential(ctx, siteID, site.Revision, store.InferenceCredentialWrite{
		Name: "Key 1", SecretCipher: []byte("not-a-valid-ciphertext"), Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	siteModelID, err := fixture.store.CreateSiteModel(ctx, store.SiteModelWrite{
		SiteID: siteID, EndpointID: endpointID, ModelName: "source-probe", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishedID, err := fixture.store.CreatePublishedModel(ctx, store.PublishedModelWrite{
		PublicName: "public-probe", Enabled: true, MonitorEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	published, _ := fixture.store.GetPublishedModel(ctx, publishedID)
	targetID, err := fixture.store.CreateRouteSiteTarget(ctx, publishedID, published.Revision, store.RouteSiteTargetWrite{
		SiteID: siteID, EndpointID: endpointID, SiteModelID: siteModelID, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	base := "/api/v2/published-models/" + strconv.FormatInt(publishedID, 10)
	resp, body = fixture.request(t, http.MethodPost, base+"/probe", map[string]any{"targetId": targetID}, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	probe := decodeContract[struct {
		Run      store.ProbeRun       `json:"run"`
		Attempts []store.ProbeAttempt `json:"attempts"`
	}](t, body)
	if probe.Run.ID == "" || probe.Run.TargetCount != 1 || len(probe.Attempts) != 1 || probe.Attempts[0].InferenceCredentialID == nil || *probe.Attempts[0].InferenceCredentialID != credentialID {
		t.Fatalf("probe response = %+v", probe)
	}

	resp, body = fixture.request(t, http.MethodGet, base+"/probe-runs?limit=10", nil, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	runs := decodeContract[struct {
		Items []store.ProbeRun `json:"items"`
	}](t, body).Items
	if len(runs) != 1 || runs[0].ID != probe.Run.ID {
		t.Fatalf("probe runs = %+v", runs)
	}

	resp, body = fixture.request(t, http.MethodGet, "/api/v2/probe-runs/"+probe.Run.ID, nil, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	loaded := decodeContract[struct {
		Item store.ProbeRun `json:"item"`
	}](t, body).Item
	if loaded.ID != probe.Run.ID {
		t.Fatalf("probe run detail = %+v", loaded)
	}

	resp, body = fixture.request(t, http.MethodGet, "/api/v2/probe-runs/"+probe.Run.ID+"/attempts", nil, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	attempts := decodeContract[struct {
		Items []store.ProbeAttempt `json:"items"`
	}](t, body).Items
	if len(attempts) != 1 || attempts[0].ProbeRunID != probe.Run.ID {
		t.Fatalf("probe attempt list = %+v", attempts)
	}

	resp, body = fixture.request(t, http.MethodGet, "/api/v2/monitor/matrix", nil, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	matrix := decodeContract[struct {
		GeneratedAt int64                         `json:"generatedAt"`
		Models      []store.PublishedModelMonitor `json:"models"`
	}](t, body)
	if matrix.GeneratedAt <= 0 || len(matrix.Models) != 1 || len(matrix.Models[0].Targets) != 1 || matrix.Models[0].Targets[0].LastProbe == nil {
		t.Fatalf("monitor matrix = %+v", matrix)
	}
}
