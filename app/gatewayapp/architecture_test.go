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
		"github.com/OnslaughtSnail/caelis/surfaces/headless":  "headless surface adapter",
		"github.com/OnslaughtSnail/caelis/surfaces/acpserver": "ACP surface adapter",
		"github.com/OnslaughtSnail/caelis/surfaces/tui/":      "TUI surface package",
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

func TestGatewayAppPromptWiringLivesInInternalPackage(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "prompt.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("ParseFile(prompt.go) error = %v", err)
	}
	const want = "github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/promptassembly"
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatalf("Unquote(%s) error = %v", spec.Path.Value, err)
		}
		if importPath == want {
			return
		}
	}
	t.Fatalf("prompt.go does not import %q; keep prompt assembly implementation in the internal promptassembly package", want)
}

func TestGatewayAppModelRegistryLivesInInternalPackage(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "local.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("ParseFile(local.go) error = %v", err)
	}
	const want = "github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/modelregistry"
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatalf("Unquote(%s) error = %v", spec.Path.Value, err)
		}
		if importPath == want {
			return
		}
	}
	t.Fatalf("local.go does not import %q; keep model configuration normalization in the internal modelregistry package", want)
}

func TestGatewayAppConfigStoreLivesInInternalPackage(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "config_store.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("ParseFile(config_store.go) error = %v", err)
	}
	const want = "github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/configstore"
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatalf("Unquote(%s) error = %v", spec.Path.Value, err)
		}
		if importPath == want {
			return
		}
	}
	t.Fatalf("config_store.go does not import %q; keep app config persistence in the internal configstore package", want)
}

func TestGatewayAppAgentRegistryLivesInInternalPackage(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "local.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("ParseFile(local.go) error = %v", err)
	}
	const want = "github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/agentregistry"
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatalf("Unquote(%s) error = %v", spec.Path.Value, err)
		}
		if importPath == want {
			return
		}
	}
	t.Fatalf("local.go does not import %q; keep ACP agent registry defaults in the internal agentregistry package", want)
}

func TestGatewayAppSandboxWiringLivesInInternalPackage(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "local.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("ParseFile(local.go) error = %v", err)
	}
	const want = "github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/sandboxpolicy"
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatalf("Unquote(%s) error = %v", spec.Path.Value, err)
		}
		if importPath == want {
			return
		}
	}
	t.Fatalf("local.go does not import %q; keep sandbox root wiring in the internal sandboxpolicy package", want)
}

func TestGatewayAppToolWiringLivesInInternalPackage(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "reconfigure.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("ParseFile(reconfigure.go) error = %v", err)
	}
	const want = "github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/toolset"
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatalf("Unquote(%s) error = %v", spec.Path.Value, err)
		}
		if importPath == want {
			return
		}
	}
	t.Fatalf("reconfigure.go does not import %q; keep builtin tool construction in the internal toolset package", want)
}

func TestGatewayAppApprovalWiringLivesInInternalPackage(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "reconfigure.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("ParseFile(reconfigure.go) error = %v", err)
	}
	const want = "github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/approvalstrategy"
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatalf("Unquote(%s) error = %v", spec.Path.Value, err)
		}
		if importPath == want {
			return
		}
	}
	t.Fatalf("reconfigure.go does not import %q; keep approval strategy construction in the internal approvalstrategy package", want)
}
