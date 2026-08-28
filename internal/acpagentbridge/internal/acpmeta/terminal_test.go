package acpmeta

import "testing"

func TestTerminalMetadataRoundTripAndAliasRemoval(t *testing.T) {
	t.Parallel()

	meta := WithTerminalInfo(map[string]any{
		TerminalOutputDeltaKey: map[string]any{"terminal_id": "terminal-1", "data": "stale\n"},
	}, "terminal-1")
	meta = WithTerminalOutput(meta, "terminal-1", "canonical\n")
	code := 0
	meta = WithTerminalExit(meta, "terminal-1", &code, nil)

	if _, ok := meta[TerminalOutputDeltaKey]; ok {
		t.Fatalf("provider terminal alias retained: %#v", meta)
	}
	info, ok := ReadTerminalInfo(meta)
	if !ok || info.TerminalID != "terminal-1" {
		t.Fatalf("ReadTerminalInfo() = %#v, %v", info, ok)
	}
	output, ok := ReadTerminalOutput(meta)
	if !ok || output.TerminalID != "terminal-1" || output.Data != "canonical\n" {
		t.Fatalf("ReadTerminalOutput() = %#v, %v", output, ok)
	}
	exit, ok := ReadTerminalExit(meta)
	if !ok || exit.ExitCode == nil || *exit.ExitCode != 0 {
		t.Fatalf("ReadTerminalExit() = %#v, %v", exit, ok)
	}
}
