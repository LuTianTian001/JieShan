package store

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestVNextStoreDoesNotImportLegacyRuntime(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{
		"github.com/LuTianTian001/JieShan/internal/store",
		"github.com/LuTianTian001/JieShan/internal/gateway",
		"github.com/LuTianTian001/JieShan/internal/monitor",
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
			for _, blocked := range forbidden {
				if path == blocked || strings.HasPrefix(path, blocked+"/") {
					t.Fatalf("%s imports forbidden legacy boundary %s", entry.Name(), path)
				}
			}
		}
	}
}
