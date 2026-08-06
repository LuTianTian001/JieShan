package upstream

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestParseModelPageVariants(t *testing.T) {
	tests := []struct {
		name string
		kind string
		body string
		want []string
	}{
		{name: "OpenAI data", kind: "openai", body: `{"data":[{"id":"gpt-b"},{"id":"gpt-a"}]}`, want: []string{"gpt-a", "gpt-b"}},
		{name: "root object array", kind: "compatible", body: `[{"model_name":"relay-b"},{"name":"relay-a"}]`, want: []string{"relay-a", "relay-b"}},
		{name: "root string array", kind: "compatible", body: `["model-b","model-a","model-a"]`, want: []string{"model-a", "model-b"}},
		{name: "models envelope", kind: "gemini", body: `{"models":[{"name":"models/gemini-pro"},{"name":"models/gemini-flash"}]}`, want: []string{"gemini-flash", "gemini-pro"}},
		{name: "nested result data", kind: "compatible", body: `{"result":{"data":{"items":[{"model":"nested-model"}]}}}`, want: []string{"nested-model"}},
		{name: "nested string models", kind: "compatible", body: `{"data":{"models":["nested-b","nested-a"]}}`, want: []string{"nested-a", "nested-b"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			page, err := parseModelPage(test.kind, []byte(test.body))
			if err != nil {
				t.Fatalf("parseModelPage() error = %v", err)
			}
			if !reflect.DeepEqual(page.Models, test.want) {
				t.Fatalf("models = %#v, want %#v", page.Models, test.want)
			}
		})
	}
}

func TestParseModelPageRejectsHTMLAndBusinessErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
		part string
	}{
		{name: "HTML login", body: `<!doctype html><html><title>Login</title></html>`, part: "HTML"},
		{name: "success false", body: `{"success":false,"message":"token expired","data":[]}`, part: "token expired"},
		{name: "error object", body: `{"error":{"message":"permission denied"}}`, part: "permission denied"},
		{name: "business code", body: `{"code":401,"message":"unauthorized"}`, part: "unauthorized"},
		{name: "unsupported JSON", body: `{"data":{"account":"user"}}`, part: "supported model list"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			page, err := parseModelPage("compatible", []byte(test.body))
			if err == nil || !strings.Contains(err.Error(), test.part) {
				t.Fatalf("parseModelPage() page=%+v error=%v, want %q", page, err, test.part)
			}
			if page.Models == nil {
				t.Fatal("error result has nil Models")
			}
		})
	}
}

func TestParseModelPageEmptyListIsNonNil(t *testing.T) {
	page, err := parseModelPage("openai", []byte(`{"data":[]}`))
	if err != nil {
		t.Fatalf("parseModelPage() error = %v", err)
	}
	if page.Models == nil || len(page.Models) != 0 {
		t.Fatalf("models = %#v, want non-nil empty slice", page.Models)
	}
}

func TestDiscoverModelsFollowsOpenAICursorPages(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if got := request.Header.Get("Authorization"); got != "Bearer discovery-secret" {
			t.Errorf("Authorization = %q", got)
		}
		if got := request.Header.Get("X-Tenant"); got != "tenant-a" {
			t.Errorf("X-Tenant = %q", got)
		}
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Query().Get("after") == "" {
			fmt.Fprint(writer, `{"data":[{"id":"model-b"}],"has_more":true,"last_id":"cursor-1"}`)
			return
		}
		if got := request.URL.Query().Get("after"); got != "cursor-1" {
			t.Errorf("after = %q", got)
		}
		fmt.Fprint(writer, `{"result":{"models":["model-a","model-b"]},"has_more":false}`)
	}))
	t.Cleanup(server.Close)

	client := &Client{http: server.Client()}
	result, err := client.discoverModelsFromURL(context.Background(), "openai", server.URL+"/v1/models", "discovery-secret", []byte(`{"X-Tenant":"tenant-a"}`))
	if err != nil {
		t.Fatalf("discoverModelsFromURL() error = %v", err)
	}
	if !result.Complete || result.PagesFetched != 2 || !reflect.DeepEqual(result.Models, []string{"model-a", "model-b"}) {
		t.Fatalf("result = %+v", result)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

func TestDiscoverModelsFollowsGeminiPageTokenAndPreservesKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if got := request.URL.Query().Get("key"); got != "gemini-secret" {
			t.Errorf("key = %q", got)
		}
		if request.URL.Query().Get("pageToken") == "" {
			fmt.Fprint(writer, `{"models":[{"name":"models/gemini-b"}],"nextPageToken":"page-2"}`)
			return
		}
		if got := request.URL.Query().Get("pageToken"); got != "page-2" {
			t.Errorf("pageToken = %q", got)
		}
		fmt.Fprint(writer, `{"models":[{"name":"models/gemini-a"}]}`)
	}))
	t.Cleanup(server.Close)

	requestURL, err := modelsURL("gemini", server.URL, "gemini-secret")
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{http: server.Client()}
	result, err := client.discoverModelsFromURL(context.Background(), "gemini", requestURL, "gemini-secret", nil)
	if err != nil {
		t.Fatalf("discoverModelsFromURL() error = %v", err)
	}
	if !reflect.DeepEqual(result.Models, []string{"gemini-a", "gemini-b"}) || result.PagesFetched != 2 || !result.Complete {
		t.Fatalf("result = %+v", result)
	}
}

func TestDiscoverModelsRejectsCrossOriginPagination(t *testing.T) {
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		destinationCalls.Add(1)
		fmt.Fprint(writer, `{"data":[{"id":"stolen"}]}`)
	}))
	t.Cleanup(destination.Close)
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(writer, `{"data":[{"id":"safe"}],"next":%q}`, destination.URL+"/v1/models")
	}))
	t.Cleanup(source.Close)

	client := &Client{http: source.Client()}
	result, err := client.discoverModelsFromURL(context.Background(), "openai", source.URL+"/v1/models", "secret", nil)
	if err == nil || !strings.Contains(err.Error(), "cannot change origin") {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if result.Complete || !reflect.DeepEqual(result.Models, []string{"safe"}) {
		t.Fatalf("partial result = %+v", result)
	}
	if destinationCalls.Load() != 0 {
		t.Fatalf("destination calls = %d, want 0", destinationCalls.Load())
	}
}

func TestDiscoverModelsEmptyResultPreservesSlice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"data":[]}`)
	}))
	t.Cleanup(server.Close)

	client := &Client{http: server.Client()}
	result, err := client.discoverModelsFromURL(context.Background(), "openai", server.URL+"/v1/models", "secret", nil)
	if !errors.Is(err, ErrEmptyModelList) {
		t.Fatalf("error = %v, want ErrEmptyModelList", err)
	}
	if result.Models == nil || len(result.Models) != 0 || !result.Complete || result.PagesFetched != 1 {
		t.Fatalf("result = %+v", result)
	}
}
