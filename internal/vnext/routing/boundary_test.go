package routing

import (
	"go/parser"
	"go/token"
	"io/fs"
	"strconv"
	"strings"
	"testing"
)

func TestVNextRoutingCoreHasNoLegacyInternalDependencies(t *testing.T) {
	packages, err := parser.ParseDir(token.NewFileSet(), ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, parsedPackage := range packages {
		for filename, file := range parsedPackage.Files {
			for _, imported := range file.Imports {
				path, unquoteErr := strconv.Unquote(imported.Path.Value)
				if unquoteErr != nil {
					t.Fatal(unquoteErr)
				}
				if strings.Contains(path, "github.com/LuTianTian001/JieShan/internal/") {
					t.Fatalf("%s imports legacy internal package %q", filename, path)
				}
			}
		}
	}
}
