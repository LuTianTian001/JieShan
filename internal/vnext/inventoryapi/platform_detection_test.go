package inventoryapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/LuTianTian001/JieShan/internal/vnext/platformdetect"
	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	"github.com/LuTianTian001/JieShan/internal/vnext/secretbox"
	"github.com/LuTianTian001/JieShan/internal/vnext/siteadmin"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

func TestStoreHandlerExposesPlatformDetectionWithoutCredentialEcho(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/settings/public" {
			_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"version":"1.2.3","site_name":"Relay","server_timezone":"UTC","server_utc_offset":"+00:00","table_page_size_options":[20,50]}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	storage, err := vnextstore.Open(context.Background(), filepath.Join(t.TempDir(), "vnext.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	box, err := secretbox.New(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	managementRegistry := siteadmin.NewRegistry()
	sub2API, err := siteadmin.NewSub2APIAdapter(upstream.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := managementRegistry.Register(sub2API); err != nil {
		t.Fatal(err)
	}
	detector, err := platformdetect.New(upstream.Client(), managementRegistry)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewStoreHandlerWithPlatformDetector(storage, box, vnextprotocol.NewRegistry(), detector)
	if err != nil {
		t.Fatal(err)
	}

	created := inventoryRequest(handler, http.MethodPost, "/api/vnext/inventory/sites", `{
		"name":"Sub2 Relay","dashboardUrl":"`+upstream.URL+`","enabled":true
	}`, "")
	requireStatus(t, created, http.StatusCreated)
	var createdEnvelope struct {
		Item siteResponse `json:"item"`
	}
	decodeRecorder(t, created, &createdEnvelope)

	response := inventoryRequest(handler, http.MethodGet,
		"/api/vnext/inventory/sites/"+strconv.FormatInt(createdEnvelope.Item.ID, 10)+"/platform-detection", "", "")
	requireStatus(t, response, http.StatusOK)
	var result platformdetect.Result
	decodeRecorder(t, response, &result)
	if result.Verdict != "trusted" || result.SelectedPlatform != "sub2api" || !result.Capabilities.SessionRefresh {
		t.Fatalf("platform detection = %#v", result)
	}
	if strings.Contains(response.Body.String(), "Authorization") || strings.Contains(response.Body.String(), "access_token") {
		t.Fatalf("platform detection leaked credentials: %s", response.Body.String())
	}

	if _, err := storage.CreateSealedSiteAccountConnection(t.Context(), createdEnvelope.Item.ID,
		vnextstore.SealedSiteAccountConnectionInput{
			AdapterKind: "one_api", Origin: upstream.URL, CipherVersion: 1, Enabled: true,
		}, func(_, _ int64) ([]byte, error) { return []byte{0x01}, nil }); err != nil {
		t.Fatal(err)
	}
	lockedResponse := inventoryRequest(handler, http.MethodGet,
		"/api/vnext/inventory/sites/"+strconv.FormatInt(createdEnvelope.Item.ID, 10)+"/platform-detection", "", "")
	requireStatus(t, lockedResponse, http.StatusOK)
	var locked platformdetect.Result
	decodeRecorder(t, lockedResponse, &locked)
	if locked.State != "manual" || locked.Verdict != "trusted" || !locked.SelectionLocked ||
		locked.SelectedPlatform != "one_api" || locked.Score != 100 {
		t.Fatalf("locked platform detection = %#v", locked)
	}
}
