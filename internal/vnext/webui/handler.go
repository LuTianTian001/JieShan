// Package webui serves the built JieShan Vite application without depending
// on the legacy HTTP stack.
package webui

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	// IndexCacheControl keeps deployments and authentication redirects from
	// being pinned by a browser cache.
	IndexCacheControl = "no-store"
	// ImmutableAssetCacheControl is used for Vite's content-hashed assets.
	ImmutableAssetCacheControl = "public, max-age=31536000, immutable"
	// PublicAssetCacheControl is used for public files whose names are stable.
	PublicAssetCacheControl = "public, max-age=3600, must-revalidate"
)

var errNotRegular = errors.New("web UI path is not a regular file")

// PathPredicate identifies an HTTP path that belongs to a non-SPA surface.
// The predicate receives the decoded URL path, including its leading slash.
type PathPredicate func(string) bool

// Options configures the Vite distribution handler.
type Options struct {
	// DistDir is the explicit filesystem directory produced by `vite build`.
	// New validates that it contains a regular index.html file.
	DistDir string

	// ReservedHandler receives API or data-plane paths. When nil, those paths
	// receive a plain 404 response. This lets the composition root pass /v1 and
	// /v1beta requests to the data plane without letting the SPA swallow them.
	ReservedHandler http.Handler

	// AdditionalReservedPath can reserve application-specific non-SPA paths in
	// addition to DefaultReservedPath. It cannot disable the safe defaults.
	AdditionalReservedPath PathPredicate
}

// Handler serves one validated Vite distribution directory.
type Handler struct {
	distDir                string
	reservedHandler        http.Handler
	additionalReservedPath PathPredicate
}

// New validates options and constructs a filesystem-backed SPA handler.
func New(options Options) (*Handler, error) {
	distDir := strings.TrimSpace(options.DistDir)
	if distDir == "" {
		return nil, errors.New("web UI distribution directory is required")
	}

	absolute, err := filepath.Abs(distDir)
	if err != nil {
		return nil, fmt.Errorf("resolve web UI distribution directory: %w", err)
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, fmt.Errorf("inspect web UI distribution directory: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("web UI distribution path is not a directory")
	}

	index, _, err := openRegular(absolute, "index.html")
	if err != nil {
		return nil, fmt.Errorf("validate web UI index: %w", err)
	}
	if err := index.Close(); err != nil {
		return nil, fmt.Errorf("close validated web UI index: %w", err)
	}

	reserved := options.ReservedHandler
	if reserved == nil {
		reserved = http.HandlerFunc(writeNotFound)
	}
	return &Handler{
		distDir:                absolute,
		reservedHandler:        reserved,
		additionalReservedPath: options.AdditionalReservedPath,
	}, nil
}

// DefaultReservedPath reports whether path belongs to a VNext API,
// data-plane, or health surface rather than the browser application.
func DefaultReservedPath(requestPath string) bool {
	for _, prefix := range []string{"/api", "/v1", "/v1beta", "/healthz"} {
		if requestPath == prefix || strings.HasPrefix(requestPath, prefix+"/") {
			return true
		}
	}
	return false
}

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if DefaultReservedPath(request.URL.Path) ||
		(handler.additionalReservedPath != nil && handler.additionalReservedPath(request.URL.Path)) {
		handler.reservedHandler.ServeHTTP(writer, request)
		return
	}
	name, valid := distributionName(request.URL.Path)
	if !valid {
		writeNotFound(writer, request)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		writeStatus(writer, request, http.StatusMethodNotAllowed, "method not allowed\n")
		return
	}

	if name == "" {
		handler.serveIndex(writer, request)
		return
	}
	file, info, err := openRegular(handler.distDir, name)
	if err == nil {
		defer file.Close()
		handler.serveFile(writer, request, name, file, info)
		return
	}
	if !errors.Is(err, fs.ErrNotExist) || isAssetRequest(name) {
		writeNotFound(writer, request)
		return
	}
	if path.Ext(name) != "" {
		writeNotFound(writer, request)
		return
	}
	handler.serveIndex(writer, request)
}

func (handler *Handler) serveIndex(writer http.ResponseWriter, request *http.Request) {
	file, info, err := openRegular(handler.distDir, "index.html")
	if err != nil {
		writeNotFound(writer, request)
		return
	}
	defer file.Close()
	writer.Header().Set("Cache-Control", IndexCacheControl)
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(writer, request, "index.html", info.ModTime(), file)
}

func (handler *Handler) serveFile(
	writer http.ResponseWriter,
	request *http.Request,
	name string,
	file *os.File,
	info fs.FileInfo,
) {
	if name == "index.html" {
		writer.Header().Set("Cache-Control", IndexCacheControl)
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	} else if strings.HasPrefix(name, "assets/") {
		writer.Header().Set("Cache-Control", ImmutableAssetCacheControl)
	} else {
		writer.Header().Set("Cache-Control", PublicAssetCacheControl)
	}
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(writer, request, path.Base(name), info.ModTime(), file)
}

func openRegular(distDir, name string) (*os.File, fs.FileInfo, error) {
	file, err := os.OpenInRoot(distDir, filepath.FromSlash(name))
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, errNotRegular
	}
	return file, info, nil
}

func distributionName(requestPath string) (string, bool) {
	if requestPath == "" || !strings.HasPrefix(requestPath, "/") {
		return "", false
	}
	name := strings.TrimPrefix(requestPath, "/")
	if name == "" {
		return "", true
	}
	if strings.HasSuffix(name, "/") {
		name = strings.TrimSuffix(name, "/")
		if name == "" {
			return "", false
		}
	}
	if strings.ContainsAny(name, "\\:\x00") || !fs.ValidPath(name) {
		return "", false
	}
	for _, segment := range strings.Split(name, "/") {
		if strings.HasPrefix(segment, ".") {
			return "", false
		}
	}
	return name, true
}

func isAssetRequest(name string) bool {
	return name == "assets" || strings.HasPrefix(name, "assets/")
}

func writeNotFound(writer http.ResponseWriter, request *http.Request) {
	writeStatus(writer, request, http.StatusNotFound, "not found\n")
}

func writeStatus(writer http.ResponseWriter, request *http.Request, status int, body string) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(status)
	if request.Method != http.MethodHead {
		_, _ = writer.Write([]byte(body))
	}
}
