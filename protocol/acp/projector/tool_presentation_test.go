package projector

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
)

func TestBuiltinToolPresentationRequiresExactDefinitionName(t *testing.T) {
	t.Parallel()

	if id, ok := projectedDisplayTerminalID("call-1", shell.RunCommandToolName); !ok || id != "call-1" {
		t.Fatalf("projectedDisplayTerminalID(RunCommand) = %q, %v", id, ok)
	}
	if _, ok := projectedDisplayTerminalID("call-1", "RUN_COMMAND"); ok {
		t.Fatal("historical RUN_COMMAND alias unexpectedly acquired built-in terminal presentation")
	}
	if _, ok := projectedDisplayTerminalID("call-1", projectedToolKindExecute); ok {
		t.Fatal("generic execute kind unexpectedly acquired built-in terminal presentation")
	}
	if got := projectedToolKind("SEARCH"); got != projectedToolKindOther {
		t.Fatalf("projectedToolKind(SEARCH) = %q, want generic other", got)
	}
	if got := projectedToolTitle(spawn.ToolName, map[string]any{"agent": "self", "prompt": "inspect"}); got != "Spawn self: inspect" {
		t.Fatalf("projectedToolTitle(Spawn) = %q", got)
	}
}

func TestProtocolToolNameForUpdatePreservesExactName(t *testing.T) {
	t.Parallel()

	update := &session.ProtocolUpdate{ToolCallID: "call-1", Kind: shell.RunCommandToolName}
	if got := protocolToolNameForUpdate(&session.Event{}, update); got != shell.RunCommandToolName {
		t.Fatalf("protocolToolNameForUpdate() = %q, want %q", got, shell.RunCommandToolName)
	}
}
