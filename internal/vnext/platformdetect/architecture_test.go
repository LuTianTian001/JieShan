package platformdetect

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPlatformDetectorOwnsNoPersistenceOrHTTPRouteBoundary(t *testing.T) {
	blocked := []string{
		"github.com/LuTianTian001/JieShan/internal/vnext/store",
		"github.com/LuTianTian001/JieShan/internal/vnext/inventoryapi",
		"github.com/LuTianTian001/JieShan/internal/vnext/siteadminapi",
		"github.com/LuTianTian001/JieShan/internal/vnext/runtime",
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
			t.Fatal(err)
		}
		for _, spec := range parsed.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range blocked {
				if path == forbidden || strings.HasPrefix(path, forbidden+"/") {
					t.Fatalf("%s imports forbidden boundary %s", entry.Name(), path)
				}
			}
		}
	}
}
