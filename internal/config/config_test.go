package config

import (
	"strings"
	"testing"
)

func TestLoadAllowPrivateUpstreams(t *testing.T) {
	t.Setenv("JIESHAN_ALLOW_PRIVATE_UPSTREAMS", "true")
	t.Setenv("JIESHAN_SECRET_KEY", strings.Repeat("ab", 32))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.AllowPrivateUpstreams {
		t.Fatal("AllowPrivateUpstreams = false, want true")
	}
}

func TestLoadPrivateUpstreamsDisabledByDefault(t *testing.T) {
	t.Setenv("JIESHAN_ALLOW_PRIVATE_UPSTREAMS", "")
	t.Setenv("JIESHAN_SECRET_KEY", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.AllowPrivateUpstreams {
		t.Fatal("AllowPrivateUpstreams = true, want false")
	}
}

func TestLoadRejectsInvalidSecretKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{name: "wrong length", key: strings.Repeat("ab", 31)},
		{name: "non hexadecimal", key: strings.Repeat("z", 64)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("JIESHAN_SECRET_KEY", test.key)
			_, err := Load()
			if err == nil {
				t.Fatal("Load() error = nil, want invalid secret key error")
			}
			if !strings.Contains(err.Error(), "exactly 64 hexadecimal characters") {
				t.Fatalf("Load() error = %q, want secret key format error", err)
			}
		})
	}
}
