package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultDatabaseName = "jieshan.sqlite"
	defaultWebDir       = "web/dist"
)

type Config struct {
	ListenAddr            string
	DataDir               string
	DatabasePath          string
	WebDir                string
	AdminPassword         string
	MasterKeyHex          string
	LogLevel              string
	AllowedOrigins        []string
	TrustProxy            bool
	SecureCookies         *bool
	SessionTTL            time.Duration
	AllowPrivateUpstreams bool

	UpstreamDialTimeout           time.Duration
	UpstreamResponseHeaderTimeout time.Duration
	UpstreamMaxConnsPerHost       int
	DatabaseMaxOpenConns          int
	DatabaseMaxIdleConns          int

	FailureThreshold   int
	FailureWindow      time.Duration
	Cooldown           time.Duration
	HalfOpenLease      time.Duration
	CredentialCooldown time.Duration
	FirstOutputTimeout time.Duration
	StreamIdleTimeout  time.Duration
	RequestTimeout     time.Duration
	MaxAttempts        int

	ProbePollInterval         time.Duration
	ProbeLeaseDuration        time.Duration
	ProbeTimeout              time.Duration
	ProbeMaxConcurrentModels  int
	ProbeMaxConcurrentTargets int
}

func Load() (Config, error) {
	dataDir := env("JIESHAN_DATA_DIR", "data")
	trustProxy, err := envBool("JIESHAN_TRUST_PROXY", false)
	if err != nil {
		return Config{}, err
	}
	allowPrivate, err := envBool("JIESHAN_ALLOW_PRIVATE_UPSTREAMS", false)
	if err != nil {
		return Config{}, err
	}
	secureCookies, err := envOptionalBool("JIESHAN_SECURE_COOKIES")
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		ListenAddr:            env("JIESHAN_LISTEN_ADDR", ":4000"),
		DataDir:               dataDir,
		DatabasePath:          env("JIESHAN_DB_PATH", filepath.Join(dataDir, defaultDatabaseName)),
		WebDir:                env("JIESHAN_WEB_DIR", defaultWebDir),
		AdminPassword:         strings.TrimSpace(os.Getenv("JIESHAN_ADMIN_PASSWORD")),
		MasterKeyHex:          strings.TrimSpace(os.Getenv("JIESHAN_SECRET_KEY")),
		LogLevel:              strings.ToLower(env("JIESHAN_LOG_LEVEL", "info")),
		AllowedOrigins:        envList("JIESHAN_ALLOWED_ORIGINS"),
		TrustProxy:            trustProxy,
		SecureCookies:         secureCookies,
		SessionTTL:            envDuration("JIESHAN_SESSION_TTL", 24*time.Hour),
		AllowPrivateUpstreams: allowPrivate,

		UpstreamDialTimeout:           envDuration("JIESHAN_UPSTREAM_DIAL_TIMEOUT", 10*time.Second),
		UpstreamResponseHeaderTimeout: envDuration("JIESHAN_UPSTREAM_TIMEOUT", 60*time.Second),
		UpstreamMaxConnsPerHost:       envInt("JIESHAN_UPSTREAM_MAX_CONNS_PER_HOST", 12),
		DatabaseMaxOpenConns:          envInt("JIESHAN_DB_MAX_OPEN_CONNS", 4),
		DatabaseMaxIdleConns:          envInt("JIESHAN_DB_MAX_IDLE_CONNS", 2),

		FailureThreshold:   envInt("JIESHAN_FAILURE_THRESHOLD", 2),
		FailureWindow:      envDuration("JIESHAN_FAILURE_WINDOW", 5*time.Minute),
		Cooldown:           envDuration("JIESHAN_COOLDOWN", 5*time.Minute),
		HalfOpenLease:      envDuration("JIESHAN_HALF_OPEN_LEASE", 30*time.Second),
		CredentialCooldown: envDuration("JIESHAN_CREDENTIAL_COOLDOWN", 5*time.Minute),
		FirstOutputTimeout: envDuration("JIESHAN_FIRST_OUTPUT_TIMEOUT", 15*time.Second),
		StreamIdleTimeout:  envDuration("JIESHAN_STREAM_IDLE_TIMEOUT", 60*time.Second),
		RequestTimeout:     envDuration("JIESHAN_REQUEST_TIMEOUT", 5*time.Minute),
		MaxAttempts:        envInt("JIESHAN_MAX_ATTEMPTS", 4),

		ProbePollInterval:         envDuration("JIESHAN_PROBE_POLL_INTERVAL", 5*time.Second),
		ProbeLeaseDuration:        envDuration("JIESHAN_PROBE_LEASE_DURATION", 5*time.Minute),
		ProbeTimeout:              envDuration("JIESHAN_PROBE_TIMEOUT", 30*time.Second),
		ProbeMaxConcurrentModels:  envInt("JIESHAN_PROBE_MAX_CONCURRENT_MODELS", 1),
		ProbeMaxConcurrentTargets: envInt("JIESHAN_PROBE_MAX_CONCURRENT_TARGETS", 2),
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (cfg Config) validate() error {
	if strings.TrimSpace(cfg.ListenAddr) == "" || strings.TrimSpace(cfg.DataDir) == "" ||
		strings.TrimSpace(cfg.DatabasePath) == "" || strings.TrimSpace(cfg.WebDir) == "" {
		return errors.New("JieShan listen, data, database, and web paths are required")
	}
	if cfg.MasterKeyHex != "" {
		decoded, err := hex.DecodeString(cfg.MasterKeyHex)
		if err != nil || len(cfg.MasterKeyHex) != 64 || len(decoded) != 32 {
			return errors.New("JIESHAN_SECRET_KEY must be exactly 64 hexadecimal characters")
		}
	}
	if cfg.AdminPassword != "" && len(cfg.AdminPassword) < 12 {
		return errors.New("JIESHAN_ADMIN_PASSWORD must contain at least 12 characters")
	}
	if cfg.SessionTTL < 5*time.Minute || cfg.SessionTTL > 30*24*time.Hour {
		return errors.New("JIESHAN_SESSION_TTL must be between 5 minutes and 30 days")
	}
	if cfg.UpstreamDialTimeout < time.Second || cfg.UpstreamResponseHeaderTimeout < time.Second {
		return errors.New("JieShan upstream timeouts must be at least one second")
	}
	if cfg.UpstreamMaxConnsPerHost < 1 || cfg.UpstreamMaxConnsPerHost > 256 {
		return errors.New("JIESHAN_UPSTREAM_MAX_CONNS_PER_HOST must be between 1 and 256")
	}
	if cfg.DatabaseMaxOpenConns < 1 || cfg.DatabaseMaxOpenConns > 32 {
		return errors.New("JIESHAN_DB_MAX_OPEN_CONNS must be between 1 and 32")
	}
	if cfg.DatabaseMaxIdleConns < 0 || cfg.DatabaseMaxIdleConns > cfg.DatabaseMaxOpenConns {
		return errors.New("JIESHAN_DB_MAX_IDLE_CONNS must be between 0 and JIESHAN_DB_MAX_OPEN_CONNS")
	}
	if cfg.FailureThreshold < 2 || cfg.FailureThreshold > 20 {
		return errors.New("JIESHAN_FAILURE_THRESHOLD must be between 2 and 20")
	}
	for name, value := range map[string]time.Duration{
		"JIESHAN_FAILURE_WINDOW":       cfg.FailureWindow,
		"JIESHAN_COOLDOWN":             cfg.Cooldown,
		"JIESHAN_HALF_OPEN_LEASE":      cfg.HalfOpenLease,
		"JIESHAN_CREDENTIAL_COOLDOWN":  cfg.CredentialCooldown,
		"JIESHAN_FIRST_OUTPUT_TIMEOUT": cfg.FirstOutputTimeout,
		"JIESHAN_STREAM_IDLE_TIMEOUT":  cfg.StreamIdleTimeout,
		"JIESHAN_REQUEST_TIMEOUT":      cfg.RequestTimeout,
		"JIESHAN_PROBE_POLL_INTERVAL":  cfg.ProbePollInterval,
		"JIESHAN_PROBE_LEASE_DURATION": cfg.ProbeLeaseDuration,
		"JIESHAN_PROBE_TIMEOUT":        cfg.ProbeTimeout,
	} {
		if value < time.Second {
			return fmt.Errorf("%s must be at least one second", name)
		}
	}
	if cfg.RequestTimeout < cfg.FirstOutputTimeout || cfg.RequestTimeout < cfg.StreamIdleTimeout {
		return errors.New("JIESHAN_REQUEST_TIMEOUT must not be shorter than first-output or stream-idle timeout")
	}
	if cfg.MaxAttempts < 1 || cfg.MaxAttempts > 20 {
		return errors.New("JIESHAN_MAX_ATTEMPTS must be between 1 and 20")
	}
	if cfg.ProbeMaxConcurrentModels < 1 || cfg.ProbeMaxConcurrentModels > 32 ||
		cfg.ProbeMaxConcurrentTargets < 1 || cfg.ProbeMaxConcurrentTargets > 32 {
		return errors.New("JieShan probe concurrency must be between 1 and 32")
	}
	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("JIESHAN_LOG_LEVEL must be debug, info, warn, or error")
	}
	return nil
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envList(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	seen := make(map[string]struct{})
	values := make([]string, 0)
	for _, part := range strings.Split(raw, ",") {
		value := strings.TrimSpace(part)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	return values
}

func envBool(key string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return parsed, nil
}

func envOptionalBool(key string) (*bool, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return nil, fmt.Errorf("%s must be true or false", key)
	}
	return &parsed, nil
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return -1
	}
	return parsed
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return -1
	}
	return parsed
}
