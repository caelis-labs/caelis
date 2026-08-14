package transcript

import (
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

func TestTerminalRuntimeOutputTextUsesOnlyTerminalOutputMeta(t *testing.T) {
	t.Parallel()

	meta := map[string]any{
		"caelis": map[string]any{
			"runtime": map[string]any{
				"task": map[string]any{"result": "legacy task output"},
			},
		},
	}
	if got := TerminalRuntimeOutputText(meta); got != "" {
		t.Fatalf("TerminalRuntimeOutputText(task meta) = %q, want empty without terminal_output", got)
	}
	meta = metautil.WithTerminalOutput(meta, "term-1", "terminal bytes")
	if got := TerminalRuntimeOutputText(meta); got != "terminal bytes" {
		t.Fatalf("TerminalRuntimeOutputText(terminal_output) = %q, want terminal bytes", got)
	}
}
