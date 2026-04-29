package acpbridge_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInterfaceArchitectureHasSingleProtocolOwners(t *testing.T) {
	t.Parallel()

	repo := filepath.Join("..")
	forbidden := map[string][]string{
		"acpbridge/projector/projector.go": {
			"type Projector interface",
		},
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
