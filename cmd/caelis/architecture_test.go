package main

import (
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

func TestCmdCaelisImportsOnlyCLIAndBootstrap(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("ParseFile(main.go) error = %v", err)
	}
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatalf("Unquote(%s) error = %v", spec.Path.Value, err)
		}
		if !strings.HasPrefix(importPath, "github.com/OnslaughtSnail/caelis/") {
			continue
		}
		switch importPath {
		case "github.com/OnslaughtSnail/caelis/internal/bootstrap",
			"github.com/OnslaughtSnail/caelis/internal/cli":
			continue
		default:
			t.Fatalf("cmd/caelis imports %q; keep the command entrypoint limited to bootstrap and internal/cli", importPath)
		}
	}
}
