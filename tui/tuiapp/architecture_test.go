package tuiapp

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestTUIAppDoesNotImportGatewayTUIRuntimeAdapter(t *testing.T) {
	const forbidden = "github.com/OnslaughtSnail/caelis/tui/gatewaydriver"
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir(.) error = %v", err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
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
			if importPath == forbidden {
				t.Fatalf("%s imports gateway driver package %q; depend on tui/driver instead", name, forbidden)
			}
		}
	}
}
