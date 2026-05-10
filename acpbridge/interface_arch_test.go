package acpbridge_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInterfaceArchitectureHasSingleProtocolOwners(t *testing.T) {
	t.Parallel()

	repo := filepath.Join("..")
	forbidden := map[string][]string{
		"acpbridge/terminal/terminal.go": {
			"type TerminalAdapter interface",
		},
		"acpbridge/loader/loader.go": {
			"type PromptCallbacks interface",
			"type SessionModesProvider interface",
			"type SessionConfigProvider interface",
		},
		"sdk/runtime/service.go": {
			"type ControlPlane interface",
		},
		"sdk/controller/controller.go": {
			"type ACP interface",
		},
		"sdk/task/task.go": {
			"List(context.Context) ([]Snapshot, error)",
		},
		"sdk/tool/tool.go": {
			"type ProgressReporter interface",
			"ContextWithProgressReporter",
			"ProgressReporterFromContext",
		},
		"sdk/subagent/subagent.go": {
			"type StreamSink interface",
			"type Streams interface",
		},
	}
	for rel, needles := range forbidden {
		raw, err := os.ReadFile(filepath.Join(repo, rel))
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", rel, err)
		}
		text := string(raw)
		for _, needle := range needles {
			if strings.Contains(text, needle) {
				t.Fatalf("%s still contains legacy interface surface %q", rel, needle)
			}
		}
	}
}

func TestACPBridgeAppDependencyIsConfinedToGatewayAgentAdapter(t *testing.T) {
	t.Parallel()

	repo := filepath.Join("..")
	allowed := map[string]bool{
		filepath.Join("acpbridge", "gatewayagent", "gatewayagent.go"): true,
	}
	err := filepath.WalkDir(filepath.Join(repo, "acpbridge"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(repo, path)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			if strings.Trim(spec.Path.Value, `"`) == "github.com/OnslaughtSnail/caelis/app/gatewayapp" && !allowed[rel] {
				t.Fatalf("%s imports app/gatewayapp; keep app wiring confined to acpbridge/gatewayagent", rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(acpbridge) error = %v", err)
	}
}
