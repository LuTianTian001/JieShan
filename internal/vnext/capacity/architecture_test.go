package capacity

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCapacityCoreStaysIndependentFromGatewayRoutingAndPersistence(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{
		"database/sql",
		"github.com/LuTianTian001/JieShan/internal/vnext/gateway",
		"github.com/LuTianTian001/JieShan/internal/vnext/protocol",
		"github.com/LuTianTian001/JieShan/internal/vnext/resolver",
		"github.com/LuTianTian001/JieShan/internal/vnext/routing",
		"github.com/LuTianTian001/JieShan/internal/vnext/store",
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
					t.Fatalf("%s imports forbidden capacity boundary %s", entry.Name(), path)
				}
			}
		}
	}
}
