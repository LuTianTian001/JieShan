package vnextmigration

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestPreviewReaderRemainsReadOnlyAndIndependent(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate package directory")
	}
	directory := filepath.Dir(currentFile)
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	previewFiles := map[string]bool{
		"load.go": true, "materialize.go": true, "preview.go": true, "report.go": true,
	}
	for _, entry := range entries {
		if entry.IsDir() || !previewFiles[entry.Name()] {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("unquote import in %s: %v", entry.Name(), err)
			}
			importsRuntimeStore := strings.Contains(importPath, "/internal/store")
			importsVNextRuntime := strings.Contains(importPath, "/internal/vnext") && !strings.HasSuffix(importPath, "/internal/vnext/protocol")
			if importsRuntimeStore || importsVNextRuntime {
				t.Errorf("%s imports runtime package %q; migration preview must remain an independent legacy reader", entry.Name(), importPath)
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if ok && (selector.Sel.Name == "Exec" || selector.Sel.Name == "ExecContext") {
				t.Errorf("%s calls %s; migration preview production code must not execute SQL writes", entry.Name(), selector.Sel.Name)
			}
			return true
		})
	}
}
