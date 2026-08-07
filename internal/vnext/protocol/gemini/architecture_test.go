package gemini

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestGeminiAdapterDoesNotDependOnRuntimeLayers(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(".", entry.Name())
		set := token.NewFileSet()
		file, err := parser.ParseFile(set, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			prefix := "github.com/LuTianTian001/JieShan/internal/vnext/"
			if strings.HasPrefix(importPath, prefix) && importPath != prefix+"protocol" {
				position := set.Position(spec.Pos())
				t.Fatalf("%s imports forbidden runtime layer %q", position, importPath)
			}
		}
	}
}
