package eventmeta

import "testing"

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
