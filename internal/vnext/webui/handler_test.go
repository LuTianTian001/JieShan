package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testIndex = "<!doctype html><html><body>JieShan app</body></html>"

func TestNewValidatesDistribution(t *testing.T) {
	t.Parallel()

	filePath := filepath.Join(t.TempDir(), "dist-file")
	if err := os.WriteFile(filePath, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	missingIndex := t.TempDir()
	indexDirectory := t.TempDir()
	if err := os.Mkdir(filepath.Join(indexDirectory, "index.html"), 0o700); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		distDir string
	}{
		{name: "empty", distDir: ""},
		{name: "missing", distDir: filepath.Join(t.TempDir(), "missing")},
		{name: "file", distDir: filePath},
		{name: "missing index", distDir: missingIndex},
		{name: "index directory", distDir: indexDirectory},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(Options{DistDir: test.distDir}); err == nil {
				t.Fatal("New returned no error")
			}
		})
	}
}

func TestHandlerServesIndexAndSPARoutesWithoutCaching(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t, Options{})

	for _, target := range []string{"/", "/index.html", "/models/claude-sonnet", "/settings/"} {
		t.Run(target, func(t *testing.T) {
			response := request(t, handler, http.MethodGet, target)
			assertStatus(t, response, http.StatusOK)
			assertBody(t, response, testIndex)
			if got := response.Header.Get("Cache-Control"); got != IndexCacheControl {
				t.Fatalf("Cache-Control = %q", got)
			}
			if got := response.Header.Get("Content-Type"); got != "text/html; charset=utf-8" {
				t.Fatalf("Content-Type = %q", got)
			}
		})
	}
}

func TestHandlerServesAssetsWithTieredCachingAndHEAD(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t, Options{})

	asset := request(t, handler, http.MethodGet, "/assets/app-abc12345.js?version=ignored")
	assertStatus(t, asset, http.StatusOK)
	assertBody(t, asset, "console.log('JieShan')")
	if got := asset.Header.Get("Cache-Control"); got != ImmutableAssetCacheControl {
		t.Fatalf("asset Cache-Control = %q", got)
	}
	if !strings.Contains(asset.Header.Get("Content-Type"), "javascript") {
		t.Fatalf("asset Content-Type = %q", asset.Header.Get("Content-Type"))
	}
	if asset.Header.Get("Last-Modified") == "" {
		t.Fatal("asset has no Last-Modified header")
	}

	public := request(t, handler, http.MethodGet, "/jieshan-brand.jpg")
	assertStatus(t, public, http.StatusOK)
	assertBody(t, public, "brand")
	if got := public.Header.Get("Cache-Control"); got != PublicAssetCacheControl {
		t.Fatalf("public Cache-Control = %q", got)
	}

	head := request(t, handler, http.MethodHead, "/assets/app-abc12345.js")
	assertStatus(t, head, http.StatusOK)
	assertBody(t, head, "")
	if got := head.Header.Get("Content-Length"); got != "22" {
		t.Fatalf("Content-Length = %q", got)
	}
	if got := head.Header.Get("Cache-Control"); got != ImmutableAssetCacheControl {
		t.Fatalf("HEAD Cache-Control = %q", got)
	}
}

func TestHandlerDoesNotTurnMissingAssetsIntoTheSPA(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t, Options{})

	for _, target := range []string{
		"/missing.js",
		"/favicon.ico",
		"/assets/missing-abc12345.js",
		"/assets/missing-without-extension",
		"/assets/",
	} {
		t.Run(target, func(t *testing.T) {
			response := request(t, handler, http.MethodGet, target)
			assertStatus(t, response, http.StatusNotFound)
			if strings.Contains(readBody(t, response), "JieShan app") {
				t.Fatal("missing asset returned the SPA index")
			}
		})
	}
}

func TestHandlerRejectsUnsafeAndHiddenPaths(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t, Options{})

	tests := []struct {
		name string
		path string
	}{
		{name: "parent", path: "/../outside.txt"},
		{name: "encoded parent", path: "/%2e%2e/outside.txt"},
		{name: "backslash", path: `/assets\app-abc12345.js`},
		{name: "alternate stream", path: "/index.html:secret"},
		{name: "hidden root", path: "/.env"},
		{name: "hidden nested", path: "/assets/.source.js"},
		{name: "double slash", path: "/assets//app-abc12345.js"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := request(t, handler, http.MethodGet, test.path)
			assertStatus(t, response, http.StatusNotFound)
		})
	}
}

func TestHandlerDoesNotFollowSymlinksOutsideDistribution(t *testing.T) {
	t.Parallel()
	distDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(distDir, "index.html"), []byte(testIndex), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("sensitive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(distDir, "leak.txt")); err != nil {
		t.Skipf("filesystem does not permit test symlinks: %v", err)
	}
	handler, err := New(Options{DistDir: distDir})
	if err != nil {
		t.Fatal(err)
	}

	response := request(t, handler, http.MethodGet, "/leak.txt")
	assertStatus(t, response, http.StatusNotFound)
	if strings.Contains(readBody(t, response), "sensitive") {
		t.Fatal("handler served a file outside the distribution")
	}
}

func TestHandlerNeverListsDirectories(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t, Options{})

	response := request(t, handler, http.MethodGet, "/assets")
	assertStatus(t, response, http.StatusNotFound)
	if strings.Contains(readBody(t, response), "app-abc12345.js") {
		t.Fatal("directory listing leaked an asset name")
	}
}

func TestHandlerPassesReservedSurfacesToConfiguredHandler(t *testing.T) {
	t.Parallel()
	var paths []string
	reserved := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.Method+" "+request.URL.Path)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(writer, `{"surface":"reserved"}`)
	})
	handler := newTestHandler(t, Options{
		ReservedHandler: reserved,
		AdditionalReservedPath: func(path string) bool {
			return path == "/metrics"
		},
	})

	tests := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/vnext/inventory/sites"},
		{method: http.MethodPost, path: "/v1/chat/completions"},
		{method: http.MethodPost, path: "/v1beta/models/gemini:generateContent"},
		{method: http.MethodHead, path: "/healthz"},
		{method: http.MethodGet, path: "/metrics"},
	}
	for _, test := range tests {
		response := request(t, handler, test.method, test.path)
		assertStatus(t, response, http.StatusTeapot)
	}
	if len(paths) != len(tests) {
		t.Fatalf("reserved handler calls = %d, want %d", len(paths), len(tests))
	}
	for index, test := range tests {
		want := test.method + " " + test.path
		if paths[index] != want {
			t.Fatalf("reserved call %d = %q, want %q", index, paths[index], want)
		}
	}

	frontend := request(t, handler, http.MethodGet, "/v10/dashboard")
	assertStatus(t, frontend, http.StatusOK)
	assertBody(t, frontend, testIndex)
}

func TestHandlerExplicitlyRejectsReservedSurfacesWithoutHandler(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t, Options{})

	response := request(t, handler, http.MethodGet, "/v1/models")
	assertStatus(t, response, http.StatusNotFound)
	if got := response.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestHandlerRejectsUnsupportedMethodsOnlyOnSPASurface(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t, Options{})

	response := request(t, handler, http.MethodPost, "/settings")
	assertStatus(t, response, http.StatusMethodNotAllowed)
	if got := response.Header.Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("Allow = %q", got)
	}
	assertBody(t, response, "method not allowed\n")

	head := request(t, handler, http.MethodHead, "/missing.js")
	assertStatus(t, head, http.StatusNotFound)
	assertBody(t, head, "")
}

func newTestHandler(t *testing.T, options Options) *Handler {
	t.Helper()
	distDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(distDir, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"index.html":                    testIndex,
		"assets/app-abc12345.js":        "console.log('JieShan')",
		"assets/app-abc12345.css":       "body { color: black; }",
		"jieshan-brand.jpg":             "brand",
		"assets/nested/placeholder.txt": "nested",
	}
	for name, contents := range files {
		fullPath := filepath.Join(distDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	options.DistDir = distDir
	handler, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func request(t *testing.T, handler http.Handler, method, target string) *http.Response {
	t.Helper()
	request := httptest.NewRequest(method, target, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder.Result()
}

func assertStatus(t *testing.T, response *http.Response, want int) {
	t.Helper()
	if response.StatusCode != want {
		t.Fatalf("status = %d, want %d; body = %q", response.StatusCode, want, readBody(t, response))
	}
}

func assertBody(t *testing.T, response *http.Response, want string) {
	t.Helper()
	if got := readBody(t, response); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func readBody(t *testing.T, response *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
