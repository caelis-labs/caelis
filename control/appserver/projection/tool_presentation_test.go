package projection

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/memorytool"
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
	if got := projectedToolKind(tasktool.ToolName); got != projectedToolKindOther {
		t.Fatalf("projectedToolKind(Task) = %q, want control-plane other", got)
	}
	if got := projectedToolTitle(spawn.ToolName, map[string]any{"agent": "self", "prompt": "inspect"}, eventstream.ToolStatusPending); got != "Spawn self: inspect" {
		t.Fatalf("projectedToolTitle(Spawn) = %q", got)
	}
}

func TestMemoryToolPresentationUsesProductSemantics(t *testing.T) {
	t.Parallel()

	if got := projectedToolKind(memorytool.RememberToolName); got != projectedToolKindEdit {
		t.Fatalf("projectedToolKind(Remember) = %q, want edit", got)
	}
	for status, want := range map[string]string{
		eventstream.ToolStatusPending:   "Updating memory",
		eventstream.ToolStatusCompleted: "Updated memory",
		eventstream.ToolStatusFailed:    "Update memory failed",
	} {
		if got := projectedToolTitle(memorytool.RememberToolName, map[string]any{"text": "private fact"}, status); got != want {
			t.Fatalf("projectedToolTitle(Remember, %q) = %q, want %q", status, got, want)
		}
	}
	if got := projectedToolKind(memorytool.RecallToolName); got != projectedToolKindSearch {
		t.Fatalf("projectedToolKind(Recall) = %q, want search", got)
	}
	if got := projectedToolTitle(memorytool.RecallToolName, map[string]any{"query": "项目技术栈"}, eventstream.ToolStatusPending); got != "Search 项目技术栈" {
		t.Fatalf("projectedToolTitle(Recall) = %q", got)
	}
	if got := projectedToolKind("remember"); got != projectedToolKindOther {
		t.Fatalf("lowercase historical alias kind = %q, want other", got)
	}

	content := projectedToolResultContent(
		"remember-1",
		memorytool.RememberToolName,
		map[string]any{"text": "private fact"},
		map[string]any{"accepted": true},
		nil,
		eventstream.ToolStatusCompleted,
	)
	if len(content) != 0 {
		t.Fatalf("successful Remember presentation content = %#v, want silent completion", content)
	}
}

func TestProtocolToolNameForUpdatePreservesExactName(t *testing.T) {
	t.Parallel()

	update := &session.ProtocolUpdate{ToolCallID: "call-1", Kind: shell.RunCommandToolName}
	if got := protocolToolNameForUpdate(&session.Event{}, update); got != shell.RunCommandToolName {
		t.Fatalf("protocolToolNameForUpdate() = %q, want %q", got, shell.RunCommandToolName)
	}
}
