package metautil

import "testing"

func TestRuntimeSectionReadsCanonicalTerminalFields(t *testing.T) {
	t.Parallel()

	meta := WithRuntimeSection(nil, Terminal, map[string]any{
		"terminal_id": "call-1",
		"tool":        "RUN_COMMAND",
		"data":        "line 1\n",
	})

	got := RuntimeSection(meta, Terminal)
	if got["terminal_id"] != "call-1" {
		t.Fatalf("terminal_id = %#v, want canonical terminal id", got["terminal_id"])
	}
	if got["tool"] != "RUN_COMMAND" {
		t.Fatalf("tool = %#v, want canonical tool", got["tool"])
	}
	if got["data"] != "line 1\n" {
		t.Fatalf("data = %#v, want terminal output data", got["data"])
	}
}
