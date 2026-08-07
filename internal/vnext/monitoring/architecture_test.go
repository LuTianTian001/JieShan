package monitoring

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Monitoring owns scheduling and durable observations, not gateway request
// execution or protocol adaptation. A concrete ProbeExecutor is injected from
// composition code so this package cannot grow a second proxy stack.
func TestMonitoringDoesNotOwnGatewayOrProtocolExecution(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{
		"github.com/LuTianTian001/JieShan/internal/vnext/gateway",
		"github.com/LuTianTian001/JieShan/internal/vnext/dataplane",
		"github.com/LuTianTian001/JieShan/internal/vnext/protocol",
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
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			for _, blocked := range forbidden {
				if importPath == blocked || strings.HasPrefix(importPath, blocked+"/") {
					t.Fatalf("%s imports forbidden execution boundary %s", entry.Name(), importPath)
				}
			}
		}
	}
}
