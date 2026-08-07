package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/config"
	vnextStore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

func TestNewBuildsOnlyTheJieShanRuntime(t *testing.T) {
	dataDir := t.TempDir()
	webDir := prepareTestWeb(t)
	databasePath := filepath.Join(dataDir, "jieshan.sqlite")
	application, err := New(context.Background(), testConfig(dataDir, databasePath, webDir), discardLogger())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = application.Close() })

	request := httptest.NewRequest(http.MethodGet, "http://jieshan.test/healthz", nil)
	response := httptest.NewRecorder()
	application.runtime.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /healthz status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"status":"ok"`) {
		t.Fatalf("GET /healthz body = %s", response.Body.String())
	}
	if _, err := os.Stat(databasePath); err != nil {
		t.Fatalf("new JieShan database was not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "jieshan.db")); !os.IsNotExist(err) {
		t.Fatalf("legacy database path should remain untouched, stat error = %v", err)
	}
}

func TestGeneratedAdministratorPasswordSurvivesRuntimeOpenFailureAndRetry(t *testing.T) {
	dataDir := t.TempDir()
	cfg := testConfig(dataDir, filepath.Join(dataDir, "jieshan.sqlite"), filepath.Join(t.TempDir(), "missing-web"))
	cfg.AdminPassword = ""
	if application, err := New(context.Background(), cfg, discardLogger()); err == nil {
		_ = application.Close()
		t.Fatal("New() succeeded with a missing web directory")
	}

	path := filepath.Join(dataDir, bootstrapPasswordFile)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read password after failed runtime open: %v", err)
	}
	password := strings.TrimSpace(string(content))
	if len(password) < 20 {
		t.Fatalf("persisted administrator password is unexpectedly short: %d", len(password))
	}
	assertPrivateFile(t, path)

	cfg.WebDir = prepareTestWeb(t)
	application, err := New(context.Background(), cfg, discardLogger())
	if err != nil {
		t.Fatalf("retry New() error = %v", err)
	}
	t.Cleanup(func() { _ = application.Close() })
	retriedContent, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(retriedContent)) != password {
		t.Fatal("retry replaced the persisted administrator password")
	}
	if generated := application.runtime.TakeBootstrapPassword(); generated != "" {
		t.Fatal("retry treated the persisted password as a newly generated secret")
	}
	assertAdminLogin(t, application, password)
}

func TestExistingBootstrapPasswordFileIsReusedAndSecured(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataDir, bootstrapPasswordFile)
	password := "persisted-bootstrap-password-2026"
	if err := os.WriteFile(path, []byte(password+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(dataDir, filepath.Join(dataDir, "jieshan.sqlite"), prepareTestWeb(t))
	cfg.AdminPassword = ""
	application, err := New(context.Background(), cfg, discardLogger())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = application.Close() })
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(content)) != password {
		t.Fatal("existing bootstrap password file was overwritten")
	}
	assertPrivateFile(t, path)
	assertAdminLogin(t, application, password)
}

func TestExistingAdministratorDoesNotCreateMisleadingPasswordFileOrChangePassword(t *testing.T) {
	dataDir := t.TempDir()
	databasePath := filepath.Join(dataDir, "jieshan.sqlite")
	webDir := prepareTestWeb(t)
	originalPassword := "existing-administrator-password"
	cfg := testConfig(dataDir, databasePath, webDir)
	cfg.AdminPassword = originalPassword
	first, err := New(context.Background(), cfg, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	passwordPath := filepath.Join(dataDir, bootstrapPasswordFile)
	if _, err := os.Stat(passwordPath); !os.IsNotExist(err) {
		t.Fatalf("explicit bootstrap unexpectedly created password file: %v", err)
	}
	cfg.AdminPassword = ""
	second, err := New(context.Background(), cfg, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if _, err := os.Stat(passwordPath); !os.IsNotExist(err) {
		t.Fatalf("existing administrator caused a misleading password file: %v", err)
	}
	assertAdminLogin(t, second, originalPassword)
}

func TestRunServesHealthStopsCleanlyAndLeavesReusableSQLite(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	databasePath := filepath.Join(dataDir, "jieshan.sqlite")
	cfg := testConfig(dataDir, databasePath, prepareTestWeb(t))
	cfg.ListenAddr = address
	application, err := New(context.Background(), cfg, discardLogger())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()

	client := &http.Client{Timeout: 250 * time.Millisecond}
	deadline := time.Now().Add(3 * time.Second)
	for {
		response, requestErr := client.Get("http://" + address + "/healthz")
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("health endpoint did not become ready: %v", requestErr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatalf("Run() shutdown error = %v", runErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run() did not stop after cancellation")
	}

	reopened, err := vnextStore.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatalf("reopen SQLite after shutdown: %v", err)
	}
	defer reopened.Close()
	var integrity string
	if err := reopened.DB.QueryRowContext(context.Background(), `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		t.Fatal(err)
	}
	if integrity != "ok" {
		t.Fatalf("SQLite integrity after shutdown = %q", integrity)
	}
}

func assertPrivateFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("%s is not a regular file", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("administrator password permissions = %o, want 600", info.Mode().Perm())
	}
}

func assertAdminLogin(t *testing.T, application *App, password string) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"username": "admin", "password": password})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://jieshan.test/api/vnext/auth/login", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://jieshan.test")
	response := httptest.NewRecorder()
	application.runtime.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("administrator login status = %d, body = %s", response.Code, response.Body.String())
	}
}

func testConfig(dataDir, databasePath, webDir string) config.Config {
	return config.Config{
		ListenAddr:                    "127.0.0.1:0",
		DataDir:                       dataDir,
		DatabasePath:                  databasePath,
		WebDir:                        webDir,
		AdminPassword:                 "a-long-test-administrator-password",
		LogLevel:                      "error",
		SessionTTL:                    time.Hour,
		UpstreamDialTimeout:           time.Second,
		UpstreamResponseHeaderTimeout: time.Second,
		UpstreamMaxConnsPerHost:       2,
		FailureThreshold:              2,
		FailureWindow:                 time.Minute,
		Cooldown:                      time.Minute,
		HalfOpenLease:                 10 * time.Second,
		CredentialCooldown:            time.Minute,
		ProbePollInterval:             time.Second,
		ProbeLeaseDuration:            time.Minute,
		ProbeTimeout:                  time.Second,
		ProbeMaxConcurrentModels:      1,
		ProbeMaxConcurrentTargets:     1,
	}
}

func prepareTestWeb(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte("<!doctype html><title>JieShan</title>"), 0o600); err != nil {
		t.Fatal(err)
	}
	return directory
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
