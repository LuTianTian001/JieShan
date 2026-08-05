package config

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr            string
	DataDir               string
	DatabasePath          string
	WebDir                string
	AdminPassword         string
	SecretKey             string
	LogLevel              string
	TrustProxy            bool
	AllowPrivateUpstreams bool
	SessionTTL            time.Duration
	UpstreamTimeout       time.Duration
}

func Load() (Config, error) {
	dataDir := env("JIESHAN_DATA_DIR", "data")
	cfg := Config{
		ListenAddr:            env("JIESHAN_LISTEN_ADDR", ":4000"),
		DataDir:               dataDir,
		DatabasePath:          env("JIESHAN_DB_PATH", filepath.Join(dataDir, "jieshan.db")),
		WebDir:                env("JIESHAN_WEB_DIR", filepath.Join("web", "dist")),
		AdminPassword:         strings.TrimSpace(os.Getenv("JIESHAN_ADMIN_PASSWORD")),
		SecretKey:             strings.TrimSpace(os.Getenv("JIESHAN_SECRET_KEY")),
		LogLevel:              strings.ToLower(env("JIESHAN_LOG_LEVEL", "info")),
		TrustProxy:            envBool("JIESHAN_TRUST_PROXY", false),
		AllowPrivateUpstreams: envBool("JIESHAN_ALLOW_PRIVATE_UPSTREAMS", false),
		SessionTTL:            envDuration("JIESHAN_SESSION_TTL", 24*time.Hour),
		UpstreamTimeout:       envDuration("JIESHAN_UPSTREAM_TIMEOUT", 60*time.Second),
	}
	if cfg.SecretKey != "" {
		decoded, err := hex.DecodeString(cfg.SecretKey)
		if err != nil || len(cfg.SecretKey) != 64 || len(decoded) != 32 {
			return Config{}, fmt.Errorf("JIESHAN_SECRET_KEY must be exactly 64 hexadecimal characters")
		}
	}
	if cfg.SessionTTL < time.Minute {
		return Config{}, fmt.Errorf("JIESHAN_SESSION_TTL must be at least one minute")
	}
	if cfg.UpstreamTimeout < time.Second {
		return Config{}, fmt.Errorf("JIESHAN_UPSTREAM_TIMEOUT must be at least one second")
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}
