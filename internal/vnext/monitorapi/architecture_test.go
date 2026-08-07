package monitorapi

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestMonitorAPIRemainsAnHTTPAdapter(t *testing.T) {
	blocked := []string{
		"github.com/LuTianTian001/JieShan/internal/vnext/runtime",
		"github.com/LuTianTian001/JieShan/internal/vnext/gateway",
		"github.com/LuTianTian001/JieShan/internal/vnext/protocol",
		"github.com/LuTianTian001/JieShan/internal/vnext/outbound",
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, spec := range parsed.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("unquote import in %s: %v", entry.Name(), err)
			}
			for _, forbidden := range blocked {
				if path == forbidden || strings.HasPrefix(path, forbidden+"/") {
					t.Fatalf("%s imports forbidden owner %s", entry.Name(), path)
				}
			}
		}
	}
}
