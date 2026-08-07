package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadUsesSafeV3Defaults(t *testing.T) {
	clearConfigurationEnvironment(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DatabasePath != filepath.Join("data", "jieshan.sqlite") {
		t.Fatalf("DatabasePath = %q, want isolated JieShan database", cfg.DatabasePath)
	}
	if cfg.AdminPassword != "" || cfg.MasterKeyHex != "" {
		t.Fatal("first-run administrator password and master key should be generated when omitted")
	}
	if cfg.AllowPrivateUpstreams || cfg.TrustProxy || cfg.SecureCookies != nil {
		t.Fatal("security-sensitive booleans should not be enabled implicitly")
	}
	if cfg.FailureThreshold != 2 || cfg.FailureWindow != 5*time.Minute || cfg.Cooldown != 15*time.Minute {
		t.Fatalf("routing defaults = threshold %d, window %s, cooldown %s", cfg.FailureThreshold, cfg.FailureWindow, cfg.Cooldown)
	}
	if cfg.FirstOutputTimeout != 15*time.Second || cfg.StreamIdleTimeout != time.Minute ||
		cfg.RequestTimeout != 5*time.Minute || cfg.MaxAttempts != 4 {
		t.Fatalf("request boundaries = %s/%s/%s attempts=%d", cfg.FirstOutputTimeout, cfg.StreamIdleTimeout, cfg.RequestTimeout, cfg.MaxAttempts)
	}
	if cfg.ProbeInterval != 15*time.Minute {
		t.Fatalf("scheduled probe interval = %s, want 15m", cfg.ProbeInterval)
	}
	if cfg.ProbeMaxConcurrentModels != 1 || cfg.ProbeMaxConcurrentTargets != 2 {
		t.Fatalf("probe concurrency = %d/%d, want 1/2", cfg.ProbeMaxConcurrentModels, cfg.ProbeMaxConcurrentTargets)
	}
	if cfg.UpstreamMaxConnsPerHost != 12 || cfg.DatabaseMaxOpenConns != 4 || cfg.DatabaseMaxIdleConns != 2 {
		t.Fatalf("2C2G pools = upstream %d database %d/%d, want 12 and 4/2",
			cfg.UpstreamMaxConnsPerHost, cfg.DatabaseMaxOpenConns, cfg.DatabaseMaxIdleConns)
	}
}

func TestLoadParsesSecurityAndOriginConfiguration(t *testing.T) {
	clearConfigurationEnvironment(t)
	t.Setenv("JIESHAN_ALLOW_PRIVATE_UPSTREAMS", "true")
	t.Setenv("JIESHAN_TRUST_PROXY", "true")
	t.Setenv("JIESHAN_SECURE_COOKIES", "false")
	t.Setenv("JIESHAN_ALLOWED_ORIGINS", "https://one.example, https://two.example,https://one.example")
	t.Setenv("JIESHAN_SECRET_KEY", strings.Repeat("ab", 32))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.AllowPrivateUpstreams || !cfg.TrustProxy || cfg.SecureCookies == nil || *cfg.SecureCookies {
		t.Fatalf("security configuration was not preserved: %+v", cfg)
	}
	wantOrigins := []string{"https://one.example", "https://two.example"}
	if !reflect.DeepEqual(cfg.AllowedOrigins, wantOrigins) {
		t.Fatalf("AllowedOrigins = %#v, want %#v", cfg.AllowedOrigins, wantOrigins)
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
		want  string
	}{
		{name: "secret length", key: "JIESHAN_SECRET_KEY", value: strings.Repeat("ab", 31), want: "64 hexadecimal"},
		{name: "secret encoding", key: "JIESHAN_SECRET_KEY", value: strings.Repeat("z", 64), want: "64 hexadecimal"},
		{name: "short password", key: "JIESHAN_ADMIN_PASSWORD", value: "too-short", want: "at least 12"},
		{name: "boolean", key: "JIESHAN_TRUST_PROXY", value: "sometimes", want: "true or false"},
		{name: "duration", key: "JIESHAN_COOLDOWN", value: "soon", want: "at least one second"},
		{name: "request timeout ordering", key: "JIESHAN_REQUEST_TIMEOUT", value: "10s", want: "must not be shorter"},
		{name: "failure threshold", key: "JIESHAN_FAILURE_THRESHOLD", value: "1", want: "between 2 and 20"},
		{name: "max attempts", key: "JIESHAN_MAX_ATTEMPTS", value: "21", want: "between 1 and 20"},
		{name: "probe concurrency", key: "JIESHAN_PROBE_MAX_CONCURRENT_TARGETS", value: "33", want: "between 1 and 32"},
		{name: "database open connections", key: "JIESHAN_DB_MAX_OPEN_CONNS", value: "33", want: "between 1 and 32"},
		{name: "database idle connections", key: "JIESHAN_DB_MAX_IDLE_CONNS", value: "5", want: "between 0"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearConfigurationEnvironment(t)
			t.Setenv(test.key, test.value)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want text %q", err, test.want)
			}
		})
	}
}

func clearConfigurationEnvironment(t *testing.T) {
	t.Helper()
	keys := []string{
		"JIESHAN_LISTEN_ADDR", "JIESHAN_DATA_DIR", "JIESHAN_DB_PATH", "JIESHAN_WEB_DIR",
		"JIESHAN_ADMIN_PASSWORD", "JIESHAN_SECRET_KEY", "JIESHAN_LOG_LEVEL", "JIESHAN_ALLOWED_ORIGINS",
		"JIESHAN_TRUST_PROXY", "JIESHAN_SECURE_COOKIES", "JIESHAN_SESSION_TTL",
		"JIESHAN_ALLOW_PRIVATE_UPSTREAMS", "JIESHAN_UPSTREAM_DIAL_TIMEOUT", "JIESHAN_UPSTREAM_TIMEOUT",
		"JIESHAN_UPSTREAM_MAX_CONNS_PER_HOST", "JIESHAN_DB_MAX_OPEN_CONNS", "JIESHAN_DB_MAX_IDLE_CONNS",
		"JIESHAN_FAILURE_THRESHOLD", "JIESHAN_FAILURE_WINDOW",
		"JIESHAN_COOLDOWN", "JIESHAN_HALF_OPEN_LEASE", "JIESHAN_CREDENTIAL_COOLDOWN",
		"JIESHAN_FIRST_OUTPUT_TIMEOUT", "JIESHAN_STREAM_IDLE_TIMEOUT", "JIESHAN_REQUEST_TIMEOUT", "JIESHAN_MAX_ATTEMPTS",
		"JIESHAN_PROBE_INTERVAL",
		"JIESHAN_PROBE_POLL_INTERVAL", "JIESHAN_PROBE_LEASE_DURATION", "JIESHAN_PROBE_TIMEOUT",
		"JIESHAN_PROBE_MAX_CONCURRENT_MODELS", "JIESHAN_PROBE_MAX_CONCURRENT_TARGETS",
	}
	for _, key := range keys {
		t.Setenv(key, "")
	}
}
