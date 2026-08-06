package monitor

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/gateway"
	"github.com/LuTianTian001/JieShan/internal/secrets"
	"github.com/LuTianTian001/JieShan/internal/store"
	"github.com/LuTianTian001/JieShan/internal/upstream"
)

func TestDueProbeJobsSchedulesEachTargetIndependently(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	recent := now.Add(-time.Minute).UnixMilli()
	overdue := now.Add(-10 * time.Minute).UnixMilli()
	routes := []store.Route{
		{
			ID: 1, Enabled: true, MonitorEnabled: true, MonitorIntervalSeconds: 300,
			Targets: []store.RouteTarget{
				{ID: 11, Enabled: true, CredentialState: "active", LastProbeAt: &recent},
				{ID: 12, Enabled: true, CredentialState: "active"},
				{ID: 13, Enabled: true, CredentialState: "active", LastProbeAt: &overdue},
				{ID: 14, Enabled: true, CredentialState: "active", CapabilityState: "unsupported", LastProbeAt: &overdue},
				{ID: 15, Enabled: true, CredentialState: "active", CapabilityState: "unsupported"},
				{ID: 16, Enabled: true, CredentialState: "invalid"},
			},
		},
		{ID: 2, Enabled: true, MonitorEnabled: false, Targets: []store.RouteTarget{{ID: 21, Enabled: true, CredentialState: "active"}}},
	}

	jobs := dueProbeJobs(routes, now)
	ids := make([]int64, 0, len(jobs))
	for _, job := range jobs {
		ids = append(ids, job.targetID)
	}
	if want := []int64{12, 13, 15}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("due target ids = %v, want %v", ids, want)
	}
}

func TestUnsupportedTargetsUseSlowReprobeInterval(t *testing.T) {
	route := store.Route{MonitorIntervalSeconds: 300}
	regular := targetProbeInterval(route, store.RouteTarget{})
	unsupported := targetProbeInterval(route, store.RouteTarget{CapabilityState: "unsupported"})
	if regular != 5*time.Minute || unsupported != time.Hour {
		t.Fatalf("regular=%s unsupported=%s", regular, unsupported)
	}
}

func TestV3SchedulerProbesDueSelectedModelOnlyOncePerInterval(t *testing.T) {
	probeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"probe","object":"chat.completion","model":"source","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`)
	}))
	defer probeServer.Close()

	ctx := context.Background()
	root := t.TempDir()
	database, err := store.Open(ctx, filepath.Join(root, "scheduler.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	cipher, err := secrets.LoadOrCreate(filepath.Join(root, "secrets"), "")
	if err != nil {
		t.Fatal(err)
	}
	siteID, err := database.CreateSite(ctx, store.SiteWrite{Name: "Scheduled", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	site, _ := database.GetSite(ctx, siteID)
	endpointID, err := database.CreateInferenceEndpoint(ctx, siteID, site.Revision, store.InferenceEndpointWrite{
		Name: "Primary", BaseURL: probeServer.URL, WireProtocol: "openai", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Encrypt("scheduled-key")
	if err != nil {
		t.Fatal(err)
	}
	site, _ = database.GetSite(ctx, siteID)
	if _, err := database.CreateInferenceCredential(ctx, siteID, site.Revision, store.InferenceCredentialWrite{
		Name: "Key", SecretCipher: encrypted, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	siteModelID, err := database.CreateSiteModel(ctx, store.SiteModelWrite{
		SiteID: siteID, EndpointID: endpointID, ModelName: "source", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishedID, err := database.CreatePublishedModel(ctx, store.PublishedModelWrite{
		PublicName: "selected", Enabled: true, MonitorEnabled: true, MonitorIntervalSeconds: 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	published, _ := database.GetPublishedModel(ctx, publishedID)
	if _, err := database.CreateRouteSiteTarget(ctx, publishedID, published.Revision, store.RouteSiteTargetWrite{
		SiteID: siteID, EndpointID: endpointID, SiteModelID: siteModelID, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	client := upstream.NewClient(database, cipher, 2*time.Second, upstream.ClientOptions{AllowPrivateUpstreams: true})
	scheduler := New(database, gateway.New(database, client), slog.New(slog.NewTextHandler(io.Discard, nil)))
	scheduler.runV3Due(ctx)
	runs, err := database.ListProbeRuns(ctx, publishedID, 10)
	if err != nil || len(runs) != 1 || runs[0].TriggerKind != "scheduled" || runs[0].Status != "success" {
		t.Fatalf("scheduled probe runs = %+v, %v", runs, err)
	}
	scheduler.runV3Due(ctx)
	runs, err = database.ListProbeRuns(ctx, publishedID, 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("model was probed twice before its interval elapsed: %+v, %v", runs, err)
	}
}
