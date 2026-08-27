package metautil

import (
	"encoding/json"
	"testing"
)

func TestRuntimeSectionReadsCanonicalToolFields(t *testing.T) {
	t.Parallel()

	meta := WithRuntimeSection(nil, "tool", map[string]any{
		"name":   "RunCommand",
		"status": "completed",
	})

	got := RuntimeSection(meta, "tool")
	if got["name"] != "RunCommand" {
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

func TestCanonicalTerminalOutputWinsAndClearsProviderAlias(t *testing.T) {
	t.Parallel()

	meta := WithTerminalOutput(map[string]any{
		TerminalOutputDeltaKey: map[string]any{"terminal_id": "command-1", "data": "stale provider output\n"},
	}, "command-1", "canonical output\n")
	if _, ok := meta[TerminalOutputDeltaKey]; ok {
		t.Fatalf("WithTerminalOutput() retained provider alias: %#v", meta)
	}
	output, ok := TerminalOutput(meta)
	if !ok || output.Data != "canonical output\n" {
		t.Fatalf("TerminalOutput() = %#v, %v; want canonical output", output, ok)
	}

	without := WithoutTerminalOutput(map[string]any{
		TerminalOutputKey:      map[string]any{"terminal_id": "command-1", "data": "canonical\n"},
		TerminalOutputDeltaKey: map[string]any{"terminal_id": "command-1", "data": "provider\n"},
		"kept":                 true,
	})
	if _, ok := TerminalOutput(without); ok || without["kept"] != true {
		t.Fatalf("WithoutTerminalOutput() = %#v, want both terminal keys removed and unrelated metadata kept", without)
	}
}

func TestInt64DistinguishesZeroFromMissingMetadata(t *testing.T) {
	t.Parallel()

	meta := WithRuntimeSection(nil, RuntimeStream, map[string]any{
		RuntimeOutputCursor: json.Number("0"),
	})
	if got, ok := Int64(meta, Root, Runtime, RuntimeStream, RuntimeOutputCursor); !ok || got != 0 {
		t.Fatalf("Int64(output_cursor) = %d, %v; want 0, true", got, ok)
	}
	if _, ok := Int64(meta, Root, Runtime, RuntimeTask, RuntimeOutputCursor); ok {
		t.Fatal("Int64(missing task output_cursor) reported a value")
	}
	meta = WithRuntimeSection(meta, RuntimeTask, map[string]any{
		RuntimeOutputCursor: 1.5,
	})
	if _, ok := Int64(meta, Root, Runtime, RuntimeTask, RuntimeOutputCursor); ok {
		t.Fatal("Int64(fractional output_cursor) reported an integer")
	}
	meta = WithRuntimeSection(meta, RuntimeTask, map[string]any{
		RuntimeOutputCursor: float64(1 << 63),
	})
	if _, ok := Int64(meta, Root, Runtime, RuntimeTask, RuntimeOutputCursor); ok {
		t.Fatal("Int64(out-of-range output_cursor) reported an integer")
	}
	meta = WithRuntimeSection(meta, RuntimeTask, map[string]any{
		RuntimeOutputCursor: "12",
	})
	if _, ok := Int64(meta, Root, Runtime, RuntimeTask, RuntimeOutputCursor); ok {
		t.Fatal("Int64(string output_cursor) accepted a non-canonical wire type")
	}
}
