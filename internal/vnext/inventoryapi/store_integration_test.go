package inventoryapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	vnextopenai "github.com/LuTianTian001/JieShan/internal/vnext/protocol/openai"
	"github.com/LuTianTian001/JieShan/internal/vnext/secretbox"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

func TestStoreHandlerSupportsInventoryDiscoveryAndImportWithoutSecretEcho(t *testing.T) {
	const upstreamSecret = "upstream-secret-value"
	var discoveryRound atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Method != http.MethodGet {
			t.Fatalf("unexpected discovery request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+upstreamSecret {
			t.Fatalf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		if discoveryRound.Load() == 0 {
			_, _ = w.Write([]byte(`{"data":[{"id":"model-b"},{"id":"model-a"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"model-a"}]}`))
	}))
	defer upstream.Close()

	storage, err := vnextstore.Open(context.Background(), filepath.Join(t.TempDir(), "vnext.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	box, err := secretbox.New(bytes.Repeat([]byte{0x31}, 32))
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := vnextopenai.NewChatCompletionsAdapter(upstream.Client())
	if err != nil {
		t.Fatal(err)
	}
	registry := vnextprotocol.NewRegistry()
	if _, err := registry.Register(vnextprotocol.OpenAI, vnextprotocol.OpenAIChatCompletions, adapter); err != nil {
		t.Fatal(err)
	}
	handler, err := NewStoreHandler(storage, box, registry)
	if err != nil {
		t.Fatal(err)
	}

	siteResponseRecorder := inventoryRequest(handler, http.MethodPost, "/api/vnext/inventory/sites", `{
		"name":"Relay","dashboardUrl":"https://relay.example","enabled":true
	}`, "")
	requireStatus(t, siteResponseRecorder, http.StatusCreated)
	var siteEnvelope struct {
		Item siteResponse `json:"item"`
	}
	decodeRecorder(t, siteResponseRecorder, &siteEnvelope)
	siteID := siteEnvelope.Item.ID

	endpointRecorder := inventoryRequest(handler, http.MethodPost,
		"/api/vnext/inventory/sites/"+strconv.FormatInt(siteID, 10)+"/endpoints", `{
			"name":"OpenAI","baseUrl":"`+upstream.URL+`/v1","wireProtocol":"openai",
			"surface":"openai.chat_completions","enabled":true
		}`, "")
	requireStatus(t, endpointRecorder, http.StatusCreated)
	var endpointEnvelope struct {
		Item endpointResponse `json:"item"`
	}
	decodeRecorder(t, endpointRecorder, &endpointEnvelope)
	endpointID := endpointEnvelope.Item.ID
	if endpointEnvelope.Item.AuthScheme != "bearer" {
		t.Fatalf("default auth scheme = %q", endpointEnvelope.Item.AuthScheme)
	}

	credentialRecorder := inventoryRequest(handler, http.MethodPost,
		"/api/vnext/inventory/sites/"+strconv.FormatInt(siteID, 10)+"/credentials", `{
			"name":"primary","secret":"`+upstreamSecret+`","enabled":true
		}`, "")
	requireStatus(t, credentialRecorder, http.StatusCreated)
	if strings.Contains(credentialRecorder.Body.String(), upstreamSecret) || strings.Contains(credentialRecorder.Body.String(), "secret_cipher") {
		t.Fatalf("credential response leaked secret material: %s", credentialRecorder.Body.String())
	}
	var credentialEnvelope struct {
		Item credentialResponse `json:"item"`
	}
	decodeRecorder(t, credentialRecorder, &credentialEnvelope)
	credentialID := credentialEnvelope.Item.ID
	if !credentialEnvelope.Item.SecretConfigured || credentialEnvelope.Item.RuntimeState != "active" {
		t.Fatalf("credential response = %#v", credentialEnvelope.Item)
	}

	bindingsRecorder := inventoryRequest(handler, http.MethodPut,
		"/api/vnext/inventory/sites/"+strconv.FormatInt(siteID, 10)+"/endpoints/"+strconv.FormatInt(endpointID, 10)+"/credentials",
		`{"credentialIds":[`+strconv.FormatInt(credentialID, 10)+`]}`, endpointRecorder.Header().Get("ETag"))
	requireStatus(t, bindingsRecorder, http.StatusOK)
	if bindingsRecorder.Header().Get("ETag") != `"2"` {
		t.Fatalf("binding ETag = %q", bindingsRecorder.Header().Get("ETag"))
	}

	discoveryRecorder := inventoryRequest(handler, http.MethodPost,
		"/api/vnext/inventory/sites/"+strconv.FormatInt(siteID, 10)+"/endpoints/"+strconv.FormatInt(endpointID, 10)+"/models/discover",
		`{"credentialId":`+strconv.FormatInt(credentialID, 10)+`}`, "")
	requireStatus(t, discoveryRecorder, http.StatusOK)
	if strings.Contains(discoveryRecorder.Body.String(), upstreamSecret) {
		t.Fatalf("discovery response leaked secret: %s", discoveryRecorder.Body.String())
	}
	var discoveryEnvelope struct {
		Complete bool `json:"complete"`
		Items    []struct {
			SourceModel string `json:"sourceModel"`
			Imported    bool   `json:"imported"`
		} `json:"items"`
	}
	decodeRecorder(t, discoveryRecorder, &discoveryEnvelope)
	if !discoveryEnvelope.Complete || !reflect.DeepEqual(discoveryEnvelope.Items, []struct {
		SourceModel string `json:"sourceModel"`
		Imported    bool   `json:"imported"`
	}{{SourceModel: "model-a"}, {SourceModel: "model-b"}}) {
		t.Fatalf("discovered models = %#v", discoveryEnvelope.Items)
	}

	importRecorder := inventoryRequest(handler, http.MethodPost,
		"/api/vnext/inventory/sites/"+strconv.FormatInt(siteID, 10)+"/endpoints/"+strconv.FormatInt(endpointID, 10)+"/models/import",
		`{"credentialId":`+strconv.FormatInt(credentialID, 10)+`,"models":["model-a","model-b"]}`, "")
	requireStatus(t, importRecorder, http.StatusOK)
	var importEnvelope struct {
		Items []providerModelResponse `json:"items"`
	}
	decodeRecorder(t, importRecorder, &importEnvelope)
	if len(importEnvelope.Items) != 2 {
		t.Fatalf("imported targets = %#v", importEnvelope.Items)
	}

	catalogRecorder := inventoryRequest(handler, http.MethodGet, "/api/vnext/inventory/model-targets?q=model&protocol=openai", "", "")
	requireStatus(t, catalogRecorder, http.StatusOK)
	var catalogEnvelope struct {
		Items []modelTargetCatalogResponse `json:"items"`
	}
	decodeRecorder(t, catalogRecorder, &catalogEnvelope)
	if len(catalogEnvelope.Items) != 2 || !catalogEnvelope.Items[0].Routable || catalogEnvelope.Items[0].BoundCredentialCount != 1 || catalogEnvelope.Items[0].UnknownCredentialCount != 0 {
		t.Fatalf("model target catalog = %#v", catalogEnvelope.Items)
	}

	discoveryRound.Store(1)
	secondDiscovery := inventoryRequest(handler, http.MethodPost,
		"/api/vnext/inventory/sites/"+strconv.FormatInt(siteID, 10)+"/endpoints/"+strconv.FormatInt(endpointID, 10)+"/models/discover",
		`{"credentialId":`+strconv.FormatInt(credentialID, 10)+`}`, "")
	requireStatus(t, secondDiscovery, http.StatusOK)
	var modelBAvailability string
	if err := storage.DB.QueryRow(`SELECT a.availability
FROM credential_target_access a
JOIN provider_model_targets p ON p.id=a.provider_model_target_id
WHERE a.credential_id=? AND p.endpoint_id=? AND p.source_model='model-b'`, credentialID, endpointID).Scan(&modelBAvailability); err != nil {
		t.Fatal(err)
	}
	if modelBAvailability != "unsupported" {
		t.Fatalf("model-b availability = %q", modelBAvailability)
	}

	legacyRouteRecorder := inventoryRequest(handler, http.MethodPost,
		"/api/vnext/inventory/downstream-keys/1/routes", `{"publicModel":"public-model","targetIds":[1]}`, "")
	requireStatus(t, legacyRouteRecorder, http.StatusNotFound)

	var ciphertext []byte
	if err := storage.DB.QueryRow(`SELECT secret_cipher FROM site_credentials WHERE id=?`, credentialID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte(upstreamSecret)) || bytes.Equal(ciphertext, []byte(upstreamSecret)) {
		t.Fatalf("stored credential is not encrypted: %q", ciphertext)
	}
	plaintext, err := box.Open(secretbox.PurposeSiteCredential, secretbox.Identity{RecordID: credentialID, OwnerID: siteID}, ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(plaintext)
	if string(plaintext) != upstreamSecret {
		t.Fatalf("decrypted credential = %q", plaintext)
	}
}

func inventoryRequest(handler http.Handler, method, path, body, ifMatch string) *httptest.ResponseRecorder {
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

func requireStatus(t *testing.T, response *httptest.ResponseRecorder, want int) {
	t.Helper()
	if response.Code != want {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, want, response.Body.String())
	}
}

func decodeRecorder(t *testing.T, response *httptest.ResponseRecorder, destination any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), destination); err != nil {
		t.Fatalf("decode response %q: %v", response.Body.String(), err)
	}
}
