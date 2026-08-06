package upstream

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestValidateURLSyntax(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "https", raw: "https://api.example.com/v1"},
		{name: "http", raw: "http://api.example.com/v1"},
		{name: "relative", raw: "/v1", wantErr: true},
		{name: "missing host", raw: "https:///v1", wantErr: true},
		{name: "ftp", raw: "ftp://api.example.com/v1", wantErr: true},
		{name: "websocket", raw: "ws://api.example.com/v1", wantErr: true},
		{name: "username", raw: "https://user@api.example.com/v1", wantErr: true},
		{name: "password", raw: "https://user:password@api.example.com/v1", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target, parseErr := url.Parse(test.raw)
			if parseErr != nil {
				if !test.wantErr {
					t.Fatalf("url.Parse() error = %v", parseErr)
				}
				return
			}
			err := validateURLSyntax(target)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateURLSyntax(%q) error = %v, wantErr %v", test.raw, err, test.wantErr)
			}
		})
	}
}

func TestOutboundPolicyBlocksNonPublicAddresses(t *testing.T) {
	policy := newOutboundPolicy(false)
	tests := []string{
		"http://127.0.0.1",
		"http://[::1]",
		"http://10.0.0.1",
		"http://172.16.0.1",
		"http://192.168.0.1",
		"http://100.64.0.1",
		"http://169.254.1.1",
		"http://224.0.0.1",
		"http://[ff02::1]",
		"http://169.254.169.254",
		"http://100.100.100.200",
		"http://metadata.google.internal",
	}

	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if err := policy.validateURL(mustParseURL(t, raw)); err == nil {
				t.Fatalf("validateURL(%q) error = nil, want blocked address", raw)
			}
		})
	}
}

func TestPrivateOptionAllowsPrivateButNotMetadata(t *testing.T) {
	policy := newOutboundPolicy(true)
	for _, raw := range []string{"http://127.0.0.1", "http://10.0.0.1", "http://[::1]"} {
		if err := policy.validateURL(mustParseURL(t, raw)); err != nil {
			t.Fatalf("validateURL(%q) error = %v, want allowed private address", raw, err)
		}
	}
	for _, raw := range []string{"http://169.254.169.254", "http://100.100.100.200", "http://metadata.google.internal"} {
		if err := policy.validateURL(mustParseURL(t, raw)); err == nil {
			t.Fatalf("validateURL(%q) error = nil, want metadata endpoint blocked", raw)
		}
	}
}

func TestClientBlocksLoopbackByDefault(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	client := NewClient(nil, nil, 2*time.Second)
	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(req); err == nil {
		t.Fatal("Do() error = nil, want loopback address blocked")
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("server requests = %d, want 0", got)
	}
}

func TestClientAllowsLoopbackWithExplicitOption(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	client := NewClient(nil, nil, 2*time.Second, ClientOptions{AllowPrivateUpstreams: true})
	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
}

func TestCrossOriginRedirectStripsCredentials(t *testing.T) {
	type capturedRequest struct {
		header http.Header
		query  url.Values
	}
	captured := make(chan capturedRequest, 1)
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		captured <- capturedRequest{header: req.Header.Clone(), query: req.URL.Query()}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(destination.Close)

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, destination.URL+"/next?key=redirect-secret&keep=yes", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(source.Close)

	client := NewClient(nil, nil, 2*time.Second, ClientOptions{AllowPrivateUpstreams: true})
	req, err := http.NewRequest(http.MethodGet, source.URL+"/start?key=initial-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Cookie", "session=secret")
	req.Header.Set("X-API-Key", "secret")
	req.Header.Set("X-Safe", "preserved")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer resp.Body.Close()

	got := <-captured
	for _, key := range []string{"Authorization", "Cookie", "X-API-Key", "Referer"} {
		if value := got.header.Get(key); value != "" {
			t.Errorf("redirected %s = %q, want empty", key, value)
		}
	}
	if value := got.header.Get("X-Safe"); value != "preserved" {
		t.Errorf("redirected X-Safe = %q, want preserved", value)
	}
	if value := got.query.Get("key"); value != "" {
		t.Errorf("redirected key query = %q, want empty", value)
	}
	if value := got.query.Get("keep"); value != "yes" {
		t.Errorf("redirected keep query = %q, want yes", value)
	}
}

func TestCrossOriginRedirectRejectsReplayedBody(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var destinationRequests atomic.Int32
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				destinationRequests.Add(1)
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(destination.Close)

			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				http.Redirect(w, req, destination.URL+"/capture", status)
			}))
			t.Cleanup(source.Close)

			client := NewClient(nil, nil, 2*time.Second, ClientOptions{AllowPrivateUpstreams: true})
			req, err := http.NewRequest(http.MethodPost, source.URL+"/refresh", strings.NewReader(`{"refresh_token":"secret"}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")

			resp, err := client.Do(req)
			if resp != nil && resp.Body != nil {
				resp.Body.Close()
			}
			if err == nil {
				t.Fatal("Do() error = nil, want cross-origin body redirect rejected")
			}
			if !strings.Contains(err.Error(), "cross-origin redirect with request body") {
				t.Fatalf("Do() error = %v, want cross-origin body rejection", err)
			}
			if got := destinationRequests.Load(); got != 0 {
				t.Fatalf("destination requests = %d, want 0", got)
			}
		})
	}
}

func TestSameOriginRedirectKeepsCredentials(t *testing.T) {
	type capturedRequest struct {
		header http.Header
		query  url.Values
	}
	captured := make(chan capturedRequest, 1)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/start" {
			http.Redirect(w, req, server.URL+"/next?key=same-origin", http.StatusTemporaryRedirect)
			return
		}
		captured <- capturedRequest{header: req.Header.Clone(), query: req.URL.Query()}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	client := NewClient(nil, nil, 2*time.Second, ClientOptions{AllowPrivateUpstreams: true})
	req, err := http.NewRequest(http.MethodGet, server.URL+"/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Cookie", "session=secret")
	req.Header.Set("X-API-Key", "secret")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer resp.Body.Close()

	got := <-captured
	for key, want := range map[string]string{
		"Authorization": "Bearer secret",
		"Cookie":        "session=secret",
		"X-API-Key":     "secret",
	} {
		if value := got.header.Get(key); value != want {
			t.Errorf("redirected %s = %q, want %q", key, value, want)
		}
	}
	if value := got.query.Get("key"); value != "same-origin" {
		t.Errorf("redirected key query = %q, want same-origin", value)
	}
}

func TestRedirectRevalidatesTargetURL(t *testing.T) {
	policy := newOutboundPolicy(false)
	original, err := http.NewRequest(http.MethodGet, "https://api.example.com/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	redirected, err := http.NewRequest(http.MethodGet, "http://127.0.0.1/latest/meta-data", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.checkRedirect(redirected, []*http.Request{original}); err == nil {
		t.Fatal("checkRedirect() error = nil, want private redirect rejected")
	}
}

func TestEndpointBuildersRejectUnsafeBaseURL(t *testing.T) {
	for _, raw := range []string{"ftp://api.example.com", "https://user:secret@api.example.com"} {
		if _, err := modelsURL("openai", raw, "secret"); err == nil {
			t.Errorf("modelsURL(%q) error = nil, want unsafe URL rejected", raw)
		}
		if _, err := chatURL(raw); err == nil {
			t.Errorf("chatURL(%q) error = nil, want unsafe URL rejected", raw)
		}
	}
}

func TestClientErrorRedactsURLCredentials(t *testing.T) {
	client := &Client{http: &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("request failed for " + req.URL.String())
	})}}
	req, err := http.NewRequest(http.MethodGet, "https://api.example.com/v1/models?key=gemini-secret&access_token=token-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(req)
	if err == nil {
		t.Fatal("Do() error = nil, want transport error")
	}
	if message := err.Error(); strings.Contains(message, "gemini-secret") || strings.Contains(message, "token-secret") {
		t.Fatalf("Do() error leaked URL credentials: %q", message)
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	target, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q) error = %v", raw, err)
	}
	return target
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
