package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/secrets"
	"github.com/LuTianTian001/JieShan/internal/store"
	"github.com/LuTianTian001/JieShan/internal/upstream"
)

type v3SiteSpec struct {
	name    string
	baseURL string
	keys    []string
}

type v3GatewayFixture struct {
	store         *store.Store
	gateway       *Gateway
	apiKey        string
	model         string
	targetIDs     []int64
	credentialIDs [][]int64
}

func newV3GatewayFixture(t *testing.T, specs ...v3SiteSpec) *v3GatewayFixture {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "v3-gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	cipher, err := secrets.LoadOrCreate(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}

	type siteResource struct {
		siteID, endpointID, modelID int64
		credentialIDs               []int64
	}
	resources := make([]siteResource, 0, len(specs))
	for _, spec := range specs {
		siteID, err := database.CreateSite(ctx, store.SiteWrite{Name: spec.name, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		site, _ := database.GetSite(ctx, siteID)
		endpointID, err := database.CreateInferenceEndpoint(ctx, siteID, site.Revision, store.InferenceEndpointWrite{
			Name: "Primary", BaseURL: spec.baseURL, WireProtocol: "openai_chat_completions", Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		credentialIDs := make([]int64, 0, len(spec.keys))
		for index, raw := range spec.keys {
			encrypted, encryptErr := cipher.Encrypt(raw)
			if encryptErr != nil {
				t.Fatal(encryptErr)
			}
			site, _ = database.GetSite(ctx, siteID)
			credentialID, createErr := database.CreateInferenceCredential(ctx, siteID, site.Revision, store.InferenceCredentialWrite{
				Name: fmt.Sprintf("Key %d", index+1), SecretCipher: encrypted, Enabled: true,
			})
			if createErr != nil {
				t.Fatal(createErr)
			}
			credentialIDs = append(credentialIDs, credentialID)
		}
		modelID, err := database.CreateSiteModel(ctx, store.SiteModelWrite{
			SiteID: siteID, EndpointID: endpointID, ModelName: "provider-" + strings.ToLower(spec.name), Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		resources = append(resources, siteResource{siteID: siteID, endpointID: endpointID, modelID: modelID, credentialIDs: credentialIDs})
	}

	const publicModel = "public-v3"
	publishedID, err := database.CreatePublishedModel(ctx, store.PublishedModelWrite{
		PublicName: publicModel, Enabled: true, MonitorEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	targetIDs := make([]int64, 0, len(resources))
	credentialIDs := make([][]int64, 0, len(resources))
	for _, resource := range resources {
		published, _ := database.GetPublishedModel(ctx, publishedID)
		targetID, createErr := database.CreateRouteSiteTarget(ctx, publishedID, published.Revision, store.RouteSiteTargetWrite{
			SiteID: resource.siteID, EndpointID: resource.endpointID, SiteModelID: resource.modelID, Enabled: true,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		targetIDs = append(targetIDs, targetID)
		credentialIDs = append(credentialIDs, resource.credentialIDs)
	}

	const downstreamKey = "js_v3_gateway_integration_key"
	if _, err := database.CreateDownstreamKey(ctx, store.DownstreamKeyWrite{
		Name: "V3 integration", Enabled: true, AllowedModels: []string{publicModel},
	}, downstreamKey[:12], downstreamKey); err != nil {
		t.Fatal(err)
	}
	client := upstream.NewClient(database, cipher, 2*time.Second, upstream.ClientOptions{AllowPrivateUpstreams: true})
	return &v3GatewayFixture{
		store: database, gateway: New(database, client), apiKey: downstreamKey, model: publicModel,
		targetIDs: targetIDs, credentialIDs: credentialIDs,
	}
}

func (f *v3GatewayFixture) chat(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"model": f.model, "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+f.apiKey)
	recorder := httptest.NewRecorder()
	f.gateway.ChatCompletions(recorder, req)
	return recorder
}

func TestV3GatewayRotatesKeysInsideSiteBeforeFailingOver(t *testing.T) {
	var mu sync.Mutex
	order := make([]string, 0, 2)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		mu.Lock()
		order = append(order, auth)
		mu.Unlock()
		if auth == "Bearer first-bad" {
			writeOpenAIError(w, http.StatusUnauthorized, "invalid key", "invalid_api_key")
			return
		}
		successResponse(w, "provider-first")
	}))
	defer first.Close()
	secondHits := 0
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondHits++
		successResponse(w, "provider-second")
	}))
	defer second.Close()

	fixture := newV3GatewayFixture(t,
		v3SiteSpec{name: "First", baseURL: first.URL, keys: []string{"first-bad", "first-good"}},
		v3SiteSpec{name: "Second", baseURL: second.URL, keys: []string{"second-good"}},
	)
	response := fixture.chat(t)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	mu.Lock()
	gotOrder := strings.Join(order, ",")
	mu.Unlock()
	if gotOrder != "Bearer first-bad,Bearer first-good" || secondHits != 0 {
		t.Fatalf("key/site order = %q, second hits = %d", gotOrder, secondHits)
	}
	credential, err := fixture.store.GetInferenceCredential(context.Background(), fixture.credentialIDs[0][0])
	if err != nil || credential.RuntimeState != "invalid" {
		t.Fatalf("bad key runtime state = %+v, %v", credential, err)
	}
	var failures int
	if err := fixture.store.DB.QueryRow(`SELECT consecutive_failures FROM route_site_target_health WHERE target_id=?`, fixture.targetIDs[0]).Scan(&failures); err != nil {
		t.Fatal(err)
	}
	if failures != 0 {
		t.Fatalf("key-local auth failure penalized the site: failures=%d", failures)
	}
	logs, err := fixture.store.ListRequestLogs(context.Background(), 10, 0)
	if err != nil || len(logs) != 1 {
		t.Fatalf("V3 logs = %+v, %v", logs, err)
	}
	logItem, attempts, err := fixture.store.GetRequestLog(context.Background(), logs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if logItem.RoutingGeneration != "v3" || logItem.Surface != "chat_completions" || logItem.PublishedModelID == nil || logItem.SwitchCount != 0 {
		t.Fatalf("V3 request linkage = %+v", logItem)
	}
	if len(attempts) != 2 || attempts[0].SiteName != "First" || attempts[0].CredentialName != "Key 1" || attempts[1].CredentialName != "Key 2" {
		t.Fatalf("V3 attempt linkage = %+v", attempts)
	}
}

func TestV3GatewayUsesKeyRoutingProfileAndLogsEffectiveRouteSnapshot(t *testing.T) {
	firstHits, secondHits := 0, 0
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstHits++
		successResponse(w, "provider-first")
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondHits++
		successResponse(w, "provider-second")
	}))
	defer second.Close()

	fixture := newV3GatewayFixture(t,
		v3SiteSpec{name: "First", baseURL: first.URL, keys: []string{"first"}},
		v3SiteSpec{name: "Second", baseURL: second.URL, keys: []string{"second"}},
	)
	ctx := context.Background()
	model, err := fixture.store.GetPublishedModelByName(ctx, fixture.model)
	if err != nil {
		t.Fatal(err)
	}
	profileID, err := fixture.store.CreateRoutingProfile(ctx, "Second only")
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := fixture.store.GetRoutingProfile(ctx, profileID)
	if err := fixture.store.SetRoutingProfileModelTargets(ctx, profileID, model.ID, profile.Revision, []int64{fixture.targetIDs[1]}); err != nil {
		t.Fatal(err)
	}
	keys, err := fixture.store.ListDownstreamKeys(ctx)
	if err != nil || len(keys) != 1 {
		t.Fatalf("downstream keys = %+v, %v", keys, err)
	}
	key := keys[0]
	if err := fixture.store.UpdateDownstreamKey(ctx, key.ID, store.DownstreamKeyWrite{
		Name: key.Name, Enabled: key.Enabled, QuotaMicroUSD: key.QuotaMicroUSD, RPMLimit: key.RPMLimit,
		AllowedModels: key.AllowedModels, RoutingProfileID: &profileID, ExpiresAt: key.ExpiresAt,
	}); err != nil {
		t.Fatal(err)
	}

	if response := fixture.chat(t); response.Code != http.StatusOK {
		t.Fatalf("profile request: status=%d body=%s", response.Code, response.Body.String())
	}
	if firstHits != 0 || secondHits != 1 {
		t.Fatalf("profile subset hits: first=%d second=%d", firstHits, secondHits)
	}
	logs, err := fixture.store.ListRequestLogs(ctx, 10, 0)
	if err != nil || len(logs) != 1 {
		t.Fatalf("profile logs = %+v, %v", logs, err)
	}
	profileRequestID := logs[0].ID
	if logs[0].RoutingProfileID == nil || *logs[0].RoutingProfileID != profileID || logs[0].RoutingProfileName != "Second only" {
		t.Fatalf("profile request snapshot = %+v", logs[0])
	}

	profile, _ = fixture.store.GetRoutingProfile(ctx, profileID)
	if err := fixture.store.ClearRoutingProfileModelTargets(ctx, profileID, model.ID, profile.Revision); err != nil {
		t.Fatal(err)
	}
	if response := fixture.chat(t); response.Code != http.StatusOK {
		t.Fatalf("fallback request: status=%d body=%s", response.Code, response.Body.String())
	}
	if firstHits != 1 || secondHits != 1 {
		t.Fatalf("fallback hits: first=%d second=%d", firstHits, secondHits)
	}
	logs, err = fixture.store.ListRequestLogs(ctx, 10, 0)
	if err != nil || len(logs) != 2 || logs[0].RoutingProfileID != nil || logs[0].RoutingProfileName != store.DefaultRoutingProfileName {
		t.Fatalf("fallback request snapshot = %+v, %v", logs, err)
	}

	profile, _ = fixture.store.GetRoutingProfile(ctx, profileID)
	if err := fixture.store.DeleteRoutingProfile(ctx, profileID, profile.Revision); err != nil {
		t.Fatal(err)
	}
	key, err = fixture.store.GetDownstreamKey(ctx, key.ID)
	if err != nil || key.RoutingProfileID != nil {
		t.Fatalf("key after profile deletion = %+v, %v", key, err)
	}
	if response := fixture.chat(t); response.Code != http.StatusOK {
		t.Fatalf("post-delete request: status=%d body=%s", response.Code, response.Body.String())
	}
	if firstHits != 2 || secondHits != 1 {
		t.Fatalf("post-delete fallback hits: first=%d second=%d", firstHits, secondHits)
	}
	historical, _, err := fixture.store.GetRequestLog(ctx, profileRequestID)
	if err != nil || historical.RoutingProfileID == nil || *historical.RoutingProfileID != profileID || historical.RoutingProfileName != "Second only" {
		t.Fatalf("historical profile snapshot after delete = %+v, %v", historical, err)
	}
}

func TestV3GatewaySecondIndependentSiteFailureOpensCooldown(t *testing.T) {
	firstHits, secondHits := 0, 0
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstHits++
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondHits++
		successResponse(w, "provider-second")
	}))
	defer second.Close()

	fixture := newV3GatewayFixture(t,
		v3SiteSpec{name: "First", baseURL: first.URL, keys: []string{"first"}},
		v3SiteSpec{name: "Second", baseURL: second.URL, keys: []string{"second"}},
	)
	for attempt := 0; attempt < 2; attempt++ {
		if response := fixture.chat(t); response.Code != http.StatusOK {
			t.Fatalf("request %d: status=%d body=%s", attempt+1, response.Code, response.Body.String())
		}
	}
	var phase string
	var cooldown int64
	if err := fixture.store.DB.QueryRow(`SELECT circuit_phase,cooldown_until FROM route_site_target_health WHERE target_id=?`, fixture.targetIDs[0]).Scan(&phase, &cooldown); err != nil {
		t.Fatal(err)
	}
	if phase != "open" || cooldown <= time.Now().UnixMilli() {
		t.Fatalf("health phase=%q cooldown=%d", phase, cooldown)
	}
	if response := fixture.chat(t); response.Code != http.StatusOK {
		t.Fatalf("cooldown request: status=%d body=%s", response.Code, response.Body.String())
	}
	if firstHits != 2 || secondHits != 3 {
		t.Fatalf("cooling site was not skipped: first=%d second=%d", firstHits, secondHits)
	}
}

func TestV3GatewaySkipsSitesWithoutEligibleKeysBeforeAttemptLimit(t *testing.T) {
	secondHits := 0
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondHits++
		successResponse(w, "provider-second")
	}))
	defer second.Close()

	fixture := newV3GatewayFixture(t,
		v3SiteSpec{name: "No Keys", baseURL: second.URL, keys: nil},
		v3SiteSpec{name: "Second", baseURL: second.URL, keys: []string{"second-good"}},
	)
	if _, err := fixture.store.DB.Exec(`UPDATE published_models SET max_attempts=1 WHERE public_name=?`, fixture.model); err != nil {
		t.Fatal(err)
	}

	response := fixture.chat(t)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if secondHits != 1 {
		t.Fatalf("healthy site hits = %d, want 1", secondHits)
	}
	var healthRows int
	if err := fixture.store.DB.QueryRow(`SELECT COUNT(*) FROM route_site_target_health WHERE target_id=?`, fixture.targetIDs[0]).Scan(&healthRows); err != nil {
		t.Fatal(err)
	}
	if healthRows != 0 {
		t.Fatalf("site without credentials unexpectedly acquired a health lease")
	}
}
