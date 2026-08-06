package httpapi

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"testing"

	"github.com/LuTianTian001/JieShan/internal/store"
)

func TestV2RoutingProfileAndDownstreamKeyContract(t *testing.T) {
	fixture := newAPIContractFixture(t)
	resp, body := fixture.request(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"password": "correct horse battery staple",
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)

	publishedID, targetIDs := createHTTPRoutingProfileRoute(t, fixture.store, "profile-http-model", "HTTP One", "HTTP Two")
	resp, body = fixture.request(t, http.MethodPost, "/api/v2/routing-profiles", map[string]any{"name": "Private lane"}, nil)
	assertHTTPStatus(t, resp, body, http.StatusCreated)
	profile := decodeContract[struct {
		Item store.RoutingProfile `json:"item"`
	}](t, body).Item

	resp, body = fixture.request(t, http.MethodPatch, "/api/v2/routing-profiles/"+strconv.FormatInt(profile.ID, 10), map[string]any{
		"name": "Private priority", "revision": profile.Revision,
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	profile = decodeContract[struct {
		Item store.RoutingProfile `json:"item"`
	}](t, body).Item

	modelPath := "/api/v2/routing-profiles/" + strconv.FormatInt(profile.ID, 10) + "/models/" + strconv.FormatInt(publishedID, 10)
	resp, body = fixture.request(t, http.MethodGet, modelPath, nil, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	fallback := decodeContract[struct {
		Item store.RoutingProfileModelRoute `json:"item"`
	}](t, body).Item
	if !fallback.InheritsDefault || len(fallback.Targets) != 2 {
		t.Fatalf("initial profile model route = %+v", fallback)
	}

	resp, body = fixture.request(t, http.MethodPut, modelPath, map[string]any{
		"targetIds": []int64{targetIDs[1]}, "revision": profile.Revision,
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	override := decodeContract[struct {
		Item store.RoutingProfileModelRoute `json:"item"`
	}](t, body).Item
	if override.InheritsDefault || len(override.Targets) != 1 || override.Targets[0].ID != targetIDs[1] {
		t.Fatalf("profile override = %+v", override)
	}

	resp, body = fixture.request(t, http.MethodPost, "/api/v1/keys", map[string]any{
		"name": "Profile key", "routingProfileId": profile.ID,
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusCreated)
	createdKey := decodeContract[struct {
		Item struct {
			ID                 int64  `json:"id"`
			RoutingProfileID   *int64 `json:"routingProfileId"`
			RoutingProfileName string `json:"routingProfileName"`
			UsesDefaultRouting bool   `json:"usesDefaultRouting"`
		} `json:"item"`
	}](t, body).Item
	if createdKey.RoutingProfileID == nil || *createdKey.RoutingProfileID != profile.ID || createdKey.RoutingProfileName != "Private priority" || createdKey.UsesDefaultRouting {
		t.Fatalf("created profile key = %+v", createdKey)
	}

	resp, body = fixture.request(t, http.MethodDelete, modelPath+"?revision="+strconv.FormatInt(override.ProfileRevision, 10), nil, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	cleared := decodeContract[struct {
		Item store.RoutingProfileModelRoute `json:"item"`
	}](t, body).Item
	if !cleared.InheritsDefault || len(cleared.Targets) != 2 {
		t.Fatalf("cleared profile route = %+v", cleared)
	}

	resp, body = fixture.request(t, http.MethodDelete, "/api/v2/routing-profiles/"+strconv.FormatInt(profile.ID, 10)+"?revision="+strconv.FormatInt(cleared.ProfileRevision, 10), nil, nil)
	assertHTTPStatus(t, resp, body, http.StatusNoContent)
	key, err := fixture.store.GetDownstreamKey(context.Background(), createdKey.ID)
	if err != nil || key.RoutingProfileID != nil || key.RoutingProfileName != store.DefaultRoutingProfileName {
		t.Fatalf("key after profile deletion = %+v, %v", key, err)
	}

	resp, body = fixture.request(t, http.MethodGet, "/api/v2/routing-profiles", nil, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	profiles := decodeContract[struct {
		Items []store.RoutingProfile `json:"items"`
	}](t, body).Items
	if len(profiles) != 0 {
		t.Fatalf("profiles after deletion = %+v", profiles)
	}
}

func TestDownstreamQuotaUSDConversionRoundsAndValidates(t *testing.T) {
	for _, test := range []struct {
		name  string
		value float64
		want  int64
	}{
		{name: "tenths", value: 0.1, want: 100_000},
		{name: "fractional micro rounds up", value: 0.0000015, want: 2},
		{name: "zero", value: 0, want: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := quotaMicroUSDFromUSD(test.value)
			if err != nil || got != test.want {
				t.Fatalf("quotaMicroUSDFromUSD(%v) = %d, %v; want %d", test.value, got, err, test.want)
			}
		})
	}
	for _, value := range []float64{-1, math.NaN(), math.Inf(1), math.MaxFloat64} {
		if _, err := quotaMicroUSDFromUSD(value); err == nil {
			t.Fatalf("quotaMicroUSDFromUSD(%v) unexpectedly succeeded", value)
		}
	}

	fixture := newAPIContractFixture(t)
	resp, body := fixture.request(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"password": "correct horse battery staple",
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	resp, body = fixture.request(t, http.MethodPost, "/api/v1/keys", map[string]any{
		"name": "Rounded", "quotaUsd": 0.3,
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusCreated)
	keyID := decodeContract[struct {
		Item struct {
			ID int64 `json:"id"`
		} `json:"item"`
	}](t, body).Item.ID
	key, err := fixture.store.GetDownstreamKey(context.Background(), keyID)
	if err != nil || key.QuotaMicroUSD == nil || *key.QuotaMicroUSD != 300_000 {
		t.Fatalf("created quota = %+v, %v", key.QuotaMicroUSD, err)
	}
	resp, body = fixture.request(t, http.MethodPatch, "/api/v1/keys/"+strconv.FormatInt(keyID, 10), map[string]any{
		"quotaUsd": 0.0000015,
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusOK)
	key, err = fixture.store.GetDownstreamKey(context.Background(), keyID)
	if err != nil || key.QuotaMicroUSD == nil || *key.QuotaMicroUSD != 2 {
		t.Fatalf("updated quota = %+v, %v", key.QuotaMicroUSD, err)
	}
	resp, body = fixture.request(t, http.MethodPatch, "/api/v1/keys/"+strconv.FormatInt(keyID, 10), map[string]any{
		"quotaUsd": -0.01,
	}, nil)
	assertHTTPStatus(t, resp, body, http.StatusBadRequest)
}

func createHTTPRoutingProfileRoute(t *testing.T, s *store.Store, publicName string, siteNames ...string) (int64, []int64) {
	t.Helper()
	ctx := context.Background()
	type resource struct {
		siteID, endpointID, modelID int64
	}
	resources := make([]resource, 0, len(siteNames))
	for _, siteName := range siteNames {
		siteID, err := s.CreateSite(ctx, store.SiteWrite{Name: siteName, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		site, _ := s.GetSite(ctx, siteID)
		endpointID, err := s.CreateInferenceEndpoint(ctx, siteID, site.Revision, store.InferenceEndpointWrite{
			Name: "Primary", BaseURL: "https://" + strconv.FormatInt(siteID, 10) + ".example.test/v1", WireProtocol: "openai", Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		modelID, err := s.CreateSiteModel(ctx, store.SiteModelWrite{
			SiteID: siteID, EndpointID: endpointID, ModelName: "source-" + strconv.FormatInt(siteID, 10), Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		resources = append(resources, resource{siteID: siteID, endpointID: endpointID, modelID: modelID})
	}
	publishedID, err := s.CreatePublishedModel(ctx, store.PublishedModelWrite{PublicName: publicName, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	targetIDs := make([]int64, 0, len(resources))
	for _, item := range resources {
		published, _ := s.GetPublishedModel(ctx, publishedID)
		targetID, err := s.CreateRouteSiteTarget(ctx, publishedID, published.Revision, store.RouteSiteTargetWrite{
			SiteID: item.siteID, EndpointID: item.endpointID, SiteModelID: item.modelID, Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		targetIDs = append(targetIDs, targetID)
	}
	return publishedID, targetIDs
}
