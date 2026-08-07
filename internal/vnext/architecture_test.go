package vnext_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/LuTianTian001/JieShan"

// VNext is the production runtime. Legacy internal packages were removed after
// cutover and must not be reintroduced as dependencies under internal/vnext.
func TestVNextDoesNotImportLegacyInternalPackages(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}
		for _, spec := range file.Imports {
			importPath, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
			}
			legacyPrefix := modulePath + "/internal/"
			vnextPrefix := modulePath + "/internal/vnext/"
			if strings.HasPrefix(importPath, legacyPrefix) && !strings.HasPrefix(importPath, vnextPrefix) {
				t.Errorf("%s imports legacy internal package %q", relativePath(root, path), importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func relativePath(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(relative)
}
