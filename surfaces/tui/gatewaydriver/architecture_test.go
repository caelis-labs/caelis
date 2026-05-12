package gatewaydriver

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestGatewayDriverDoesNotImportTUIApp(t *testing.T) {
	forbidden := map[string]string{
		"github.com/OnslaughtSnail/caelis/app/gatewayapp":   "local app composition root",
		"github.com/OnslaughtSnail/caelis/surfaces/tui/app": "Bubble Tea implementation",
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir(.) error = %v", err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(".", name)
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("ParseFile(%s) error = %v", path, err)
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("Unquote(%s) error = %v", spec.Path.Value, err)
			}
			if reason, ok := forbidden[importPath]; ok {
				t.Fatalf("%s imports %s %q; surfaces/tui/gatewaydriver should implement surfaces/tui/driver without owning the Bubble Tea app", name, reason, importPath)
			}
		}
	}
}
