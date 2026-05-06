package gatewayapp

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestGatewayAppDoesNotImportSurfacePackages(t *testing.T) {
	forbiddenPrefixes := map[string]string{
		"github.com/OnslaughtSnail/caelis/acpbridge/": "ACP surface adapter",
		"github.com/OnslaughtSnail/caelis/headless":   "headless surface adapter",
		"github.com/OnslaughtSnail/caelis/tui/":       "TUI surface package",
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
			for prefix, reason := range forbiddenPrefixes {
				if strings.HasPrefix(importPath, prefix) {
					t.Fatalf("%s imports %s %q; app/gatewayapp should build the local stack without owning surface adapters", name, reason, importPath)
				}
			}
		}
	}
}
