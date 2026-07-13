package metautil

import "testing"

func TestRuntimeSectionReadsCanonicalToolFields(t *testing.T) {
	t.Parallel()

	meta := WithRuntimeSection(nil, "tool", map[string]any{
		"name":   "RUN_COMMAND",
		"status": "completed",
	})

	got := RuntimeSection(meta, "tool")
	if got["name"] != "RUN_COMMAND" {
		t.Fatalf("name = %#v, want canonical tool name", got["name"])
	}
	if got["status"] != "completed" {
		t.Fatalf("status = %#v, want completed", got["status"])
	}
}

func TestStringAndBoolReadNestedMeta(t *testing.T) {
	t.Parallel()

	meta := WithRuntimeSection(nil, RuntimeStream, map[string]any{
		RuntimeStreamParentTool: "  SPAWN  ",
		"active":                true,
	})

	if got := String(meta, Root, Runtime, RuntimeStream, RuntimeStreamParentTool); got != "SPAWN" {
		t.Fatalf("String() = %q, want trimmed tool name", got)
	}
	if !Bool(meta, Root, Runtime, RuntimeStream, "active") {
		t.Fatal("Bool() = false, want true")
	}
}

func TestTerminalMetaRoundTrip(t *testing.T) {
	t.Parallel()

	meta := WithTerminalInfo(nil, "call-1")
	meta = WithTerminalOutput(meta, "call-1", "line 1\n")
	code := 0
	meta = WithTerminalExit(meta, "call-1", &code, nil)

	info, ok := TerminalInfo(meta)
	if !ok || info.TerminalID != "call-1" {
		t.Fatalf("TerminalInfo() = %+v, %v; want call-1", info, ok)
	}
	output, ok := TerminalOutput(meta)
	if !ok || output.TerminalID != "call-1" || output.Data != "line 1\n" {
		t.Fatalf("TerminalOutput() = %+v, %v; want line output", output, ok)
	}
	exit, ok := TerminalExit(meta)
	if !ok || exit.TerminalID != "call-1" || exit.ExitCode == nil || *exit.ExitCode != 0 {
		t.Fatalf("TerminalExit() = %+v, %v; want exit code 0", exit, ok)
	}
}
