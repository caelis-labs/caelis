package tuiapp

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/internal/evalharness"
	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/metautil"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestRegressionACPEventstreamToolCallFrame120x32(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{
		AppName:     "CAELIS",
		Version:     "dev",
		Workspace:   "/tmp/workspace",
		ModelAlias:  "minimax/MiniMax-M1",
		Commands:    DefaultCommands(),
		Wizards:     DefaultWizards(),
		NoColor:     true,
		NoAnimation: true,
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	model = updated.(*Model)

	for _, env := range []eventstream.Envelope{
		{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "sess-regression",
			Final:     true,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateUserMessage,
				Content:       schema.TextContent{Type: "text", Text: "run the smoke check"},
			},
		},
		{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "sess-regression",
			Update: schema.ToolCall{
				SessionUpdate: schema.UpdateToolCall,
				ToolCallID:    "call-1",
				Title:         "go test ./surfaces/tui/app",
				Kind:          schema.ToolKindExecute,
				Status:        schema.ToolStatusInProgress,
				RawInput:      map[string]any{"command": "go test ./surfaces/tui/app"},
				Meta:          acpToolNameMeta("RUN_COMMAND"),
			},
		},
		{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "sess-regression",
			Final:     true,
			Update: schema.ToolCallUpdate{
				SessionUpdate: schema.UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Title:         stringPtr("go test ./surfaces/tui/app"),
				Kind:          stringPtr(schema.ToolKindExecute),
				Status:        stringPtr(schema.ToolStatusCompleted),
				RawInput:      map[string]any{"command": "go test ./surfaces/tui/app"},
				RawOutput:     map[string]any{"exit_code": 0},
				Meta:          metautil.WithTerminalOutput(acpToolNameMeta("RUN_COMMAND"), "call-1", "ok\nPASS\n"),
			},
		},
		{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "sess-regression",
			Final:     true,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				Content:       schema.TextContent{Type: "text", Text: "Smoke check passed."},
			},
		},
	} {
		updated, _ = model.Update(env)
		model = updated.(*Model)
	}

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 2 || block.Events[1].Kind != SEAssistant || block.Events[1].Text != "Smoke check passed." {
		t.Fatalf("ACP block events = %#v, want tool call followed by final assistant text", block.Events)
	}
	if rows := block.Render(model.blockRenderContext(96)); !renderedRowsContainPlain(rows, "Smoke check passed.") {
		t.Fatalf("ACP block render missing assistant text: %#v", renderedPlainRows(rows))
	}

	frame := evalharness.NormalizeFrame(model.View().Content)
	assertFrameContainsInOrder(t, "ACP tool call 120x32", frame, []string{
		"run the smoke check",
		"Ran go test ./surfaces/tui/app",
		"/tmp/workspace",
		"> Type your message, @agent, #path/to/file, or $skill",
	})
}

func acpToolNameMeta(name string) map[string]any {
	return map[string]any{
		gateway.EventMetaRoot: map[string]any{
			gateway.EventMetaRuntime: map[string]any{
				gateway.EventMetaRuntimeTool: map[string]any{
					gateway.EventMetaRuntimeToolName: name,
				},
			},
		},
	}
}

func assertFrameContainsInOrder(t *testing.T, name string, frame string, want []string) {
	t.Helper()
	cursor := 0
	for _, fragment := range want {
		idx := strings.Index(frame[cursor:], fragment)
		if idx < 0 {
			t.Fatalf("%s missing fragment %q after byte %d\nframe:\n%s", name, fragment, cursor, frame)
		}
		cursor += idx + len(fragment)
	}
}
