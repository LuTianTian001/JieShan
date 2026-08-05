package billing

import (
	"errors"
	"strings"
	"testing"
)

func TestBuiltinCatalogLoadsAndCoversProviders(t *testing.T) {
	catalog, err := BuiltinCatalog()
	if err != nil {
		t.Fatalf("BuiltinCatalog() error = %v", err)
	}
	if catalog.Version != "2026-08-05.lkg.1" {
		t.Fatalf("catalog version = %q", catalog.Version)
	}
	if len(catalog.DigestSHA256()) != 64 {
		t.Fatalf("catalog digest = %q", catalog.DigestSHA256())
	}

	providers := []struct {
		model    string
		provider string
		priced   bool
	}{
		{"gpt-5.6", "openai", true},
		{"claude-sonnet-5-20260203", "anthropic", true},
		{"gemini-2.5-pro", "google", true},
		{"deepseek-chat", "deepseek", true},
		{"qwen3.8-max", "alibaba", true},
		{"glm-5", "zhipu", false},
		{"kimi-k2.6", "moonshot", true},
	}
	for _, item := range providers {
		price, ok := catalog.Lookup(item.model)
		if !ok {
			t.Errorf("Lookup(%q) did not find a catalog entry", item.model)
			continue
		}
		if price.Provider != item.provider || price.Priced != item.priced {
			t.Errorf("Lookup(%q) = provider %q priced %v", item.model, price.Provider, price.Priced)
		}
	}
}

func TestParseCatalogRejectsTrailingDataAndDuplicateAliases(t *testing.T) {
	raw := string(BuiltinCatalogJSON())
	if _, err := ParseCatalog([]byte(raw + `{}`)); !errors.Is(err, ErrInvalidCatalog) {
		t.Fatalf("trailing JSON error = %v", err)
	}

	duplicate := strings.Replace(raw, `"aliases": ["gpt-5.6-mini"]`, `"aliases": ["gpt-5.6-mini", "gpt-5.6"]`, 1)
	if _, err := ParseCatalog([]byte(duplicate)); !errors.Is(err, ErrInvalidCatalog) {
		t.Fatalf("duplicate alias error = %v", err)
	}
}

func TestParseCatalogRejectsMissingFX(t *testing.T) {
	raw := strings.Replace(string(BuiltinCatalogJSON()), `"CNY": {`, `"REMOVED": {`, 1)
	if _, err := ParseCatalog([]byte(raw)); !errors.Is(err, ErrInvalidCatalog) {
		t.Fatalf("missing FX error = %v", err)
	}
}
