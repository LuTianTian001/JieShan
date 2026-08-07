package inventoryapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/outbound"
	vnextprotocol "github.com/LuTianTian001/JieShan/internal/vnext/protocol"
	vnextopenai "github.com/LuTianTian001/JieShan/internal/vnext/protocol/openai"
	"github.com/LuTianTian001/JieShan/internal/vnext/secretbox"
	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const previewPath = "/api/vnext/inventory/model-discovery/preview"

func TestModelDiscoveryPreviewUsesSSRFClientAndLeavesNoPersistentTrace(t *testing.T) {
	const apiKey = "temporary-preview-secret"
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/relay/v1/models" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"model-z"},{"id":"model-a"},{"id":"model-z"}]}`))
	}))
	defer upstream.Close()

	handler, storage, client := newPreviewHandler(t, true)
	defer client.CloseIdleConnections()
	response := inventoryRequest(handler, http.MethodPost, previewPath, `{
		"baseUrl":"`+upstream.URL+`/relay/v1","wireProtocol":"openai",
		"authScheme":"bearer","apiKey":"`+apiKey+`"
	}`, "")
	requireStatus(t, response, http.StatusOK)
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("preview cache headers = %v", response.Header())
	}
	if calls.Load() != 1 || strings.Contains(response.Body.String(), apiKey) ||
		strings.Contains(response.Body.String(), upstream.URL) {
		t.Fatalf("preview response/calls = %d %s", calls.Load(), response.Body.String())
	}
	var envelope struct {
		Models   []string `json:"models"`
		Complete bool     `json:"complete"`
	}
	decodeRecorder(t, response, &envelope)
	if !envelope.Complete || !reflect.DeepEqual(envelope.Models, []string{"model-a", "model-z"}) {
		t.Fatalf("preview = %+v", envelope)
	}
	assertPreviewTablesEmpty(t, storage)
}

func TestModelDiscoveryPreviewSanitizesUpstreamCredentialFailure(t *testing.T) {
	const apiKey = "temporary-key-that-must-not-leak"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"rejected ` + apiKey + `"}}`))
	}))
	defer upstream.Close()

	handler, storage, client := newPreviewHandler(t, true)
	defer client.CloseIdleConnections()
	response := inventoryRequest(handler, http.MethodPost, previewPath, `{
		"baseUrl":"`+upstream.URL+`/v1","wireProtocol":"openai","surface":"openai.responses",
		"authScheme":"x-api-key","apiKey":"`+apiKey+`"
	}`, "")
	requireStatus(t, response, http.StatusUnauthorized)
	if !strings.Contains(response.Body.String(), `"code":"upstream_authentication_failed"`) ||
		strings.Contains(response.Body.String(), apiKey) {
		t.Fatalf("sanitized authentication response = %s", response.Body.String())
	}
	assertPreviewTablesEmpty(t, storage)
}

func TestModelDiscoveryPreviewBlocksPrivateTargetsWhenOutboundPolicyDoes(t *testing.T) {
	const apiKey = "private-target-secret"
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer upstream.Close()

	handler, storage, client := newPreviewHandler(t, false)
	defer client.CloseIdleConnections()
	response := inventoryRequest(handler, http.MethodPost, previewPath, `{
		"baseUrl":"`+upstream.URL+`/v1","wireProtocol":"openai",
		"authScheme":"bearer","apiKey":"`+apiKey+`"
	}`, "")
	requireStatus(t, response, http.StatusBadGateway)
	if calls.Load() != 0 || !strings.Contains(response.Body.String(), `"code":"discovery_failed"`) ||
		strings.Contains(response.Body.String(), apiKey) {
		t.Fatalf("SSRF response/calls = %d %s", calls.Load(), response.Body.String())
	}
	assertPreviewTablesEmpty(t, storage)
}

func TestModelDiscoveryPreviewValidatesEphemeralContract(t *testing.T) {
	handler, _, client := newPreviewHandler(t, true)
	defer client.CloseIdleConnections()
	tests := []struct {
		name string
		body string
	}{
		{name: "missing API key", body: `{"baseUrl":"https://example.com/v1","wireProtocol":"openai","authScheme":"bearer"}`},
		{name: "URL query", body: `{"baseUrl":"https://example.com/v1?token=must-not-echo","wireProtocol":"openai","authScheme":"bearer","apiKey":"key"}`},
		{name: "mismatched surface", body: `{"baseUrl":"https://example.com/v1","wireProtocol":"anthropic","surface":"openai.responses","authScheme":"x-api-key","apiKey":"key"}`},
		{name: "protocol auth mismatch", body: `{"baseUrl":"https://example.com/v1","wireProtocol":"gemini","authScheme":"bearer","apiKey":"key"}`},
		{name: "unknown field", body: `{"baseUrl":"https://example.com/v1","wireProtocol":"openai","authScheme":"bearer","apiKey":"key","secret":"forbidden"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := inventoryRequest(handler, http.MethodPost, previewPath, test.body, "")
			requireStatus(t, response, http.StatusBadRequest)
			if strings.Contains(response.Body.String(), "must-not-echo") || strings.Contains(response.Body.String(), "forbidden") {
				t.Fatalf("validation response leaked request material: %s", response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("validation cache headers = %v", response.Header())
			}
		})
	}
	response := inventoryRequest(handler, http.MethodGet, previewPath, "", "")
	requireStatus(t, response, http.StatusMethodNotAllowed)
}

func TestClassifyDiscoveryFailureUsesStableSafeCategories(t *testing.T) {
	tests := []struct {
		message string
		want    error
	}{
		{"credential_auth", ErrDiscoveryAuthFailed},
		{"credential_permission", ErrDiscoveryForbidden},
		{"credential_payment_required", ErrDiscoveryPayment},
		{"credential_rate_limited", ErrDiscoveryRateLimited},
		{"unclassified upstream failure", ErrDiscoveryFailed},
	}
	for _, test := range tests {
		if got := classifyDiscoveryFailure(context.Background(), errors.New(test.message)); !errors.Is(got, test.want) {
			t.Fatalf("classifyDiscoveryFailure(%q) = %v, want %v", test.message, got, test.want)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	if got := classifyDiscoveryFailure(ctx, errors.New("generic")); !errors.Is(got, ErrDiscoveryTimedOut) {
		t.Fatalf("timeout classification = %v", got)
	}
}

func newPreviewHandler(t *testing.T, allowPrivate bool) (*Handler, *vnextstore.Store, *outbound.Client) {
	t.Helper()
	storage, err := vnextstore.Open(context.Background(), filepath.Join(t.TempDir(), "vnext.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	box, err := secretbox.New(bytes.Repeat([]byte{0x71}, 32))
	if err != nil {
		t.Fatal(err)
	}
	client := outbound.New(outbound.Options{
		AllowPrivate: allowPrivate, DialTimeout: time.Second, ResponseHeaderTimeout: 2 * time.Second,
	})
	doer := interface {
		Do(*http.Request) (*http.Response, error)
	}(client)
	adapter, err := vnextopenai.NewChatCompletionsAdapter(doer)
	if err != nil {
		t.Fatal(err)
	}
	responses, err := vnextopenai.NewResponsesAdapter(doer)
	if err != nil {
		t.Fatal(err)
	}
	registry := vnextprotocol.NewRegistry()
	if _, err := registry.Register(vnextprotocol.OpenAI, vnextprotocol.OpenAIChatCompletions, adapter); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Register(vnextprotocol.OpenAI, vnextprotocol.OpenAIResponses, responses); err != nil {
		t.Fatal(err)
	}
	handler, err := NewStoreHandler(storage, box, registry)
	if err != nil {
		t.Fatal(err)
	}
	return handler, storage, client
}

func assertPreviewTablesEmpty(t *testing.T, storage *vnextstore.Store) {
	t.Helper()
	for _, table := range []string{"sites", "site_endpoints", "site_credentials", "provider_model_targets", "request_logs"} {
		var count int
		if err := storage.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("preview persisted %d rows in %s", count, table)
		}
	}
}
