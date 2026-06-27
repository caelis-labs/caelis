package metautil

import "testing"

func TestTerminalSectionMergesLegacyFallbackFields(t *testing.T) {
	t.Parallel()

	meta := WithRuntimeSection(map[string]any{
		LegacyTerminalOutput: map[string]any{
			"terminal_id": "legacy-call",
			"data":        "line 1\n",
		},
	}, Terminal, map[string]any{
		"terminal_id": "call-1",
		"tool":        "RUN_COMMAND",
	})

	got := TerminalSection(meta, LegacyTerminalOutput)
	if got["terminal_id"] != "call-1" {
		t.Fatalf("terminal_id = %#v, want canonical terminal id", got["terminal_id"])
	}
	if got["tool"] != "RUN_COMMAND" {
		t.Fatalf("tool = %#v, want canonical tool", got["tool"])
	}
	if got["data"] != "line 1\n" {
		t.Fatalf("data = %#v, want legacy terminal output data fallback", got["data"])
	}
}
