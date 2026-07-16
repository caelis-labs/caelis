package tuiapp

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/internal/evalharness"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
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
		completedRegressionTurn("sess-regression", ""),
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
		"> Type a message, /agent-name prompt, #path/to/file, or $skill",
		"/tmp/workspace",
	})
}

func TestRegressionACPEventstreamWhitespaceOnlyAssistantChunkDoesNotRenderBeforeTool(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{
		AppName:     "CAELIS",
		Version:     "dev",
		Workspace:   "/tmp/workspace",
		ModelAlias:  "glm-4.5",
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
			TurnID:    "turn-whitespace-tool",
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				Content:       schema.TextContent{Type: "text", Text: "\n"},
			},
		},
		{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "sess-regression",
			TurnID:    "turn-whitespace-tool",
			Update: schema.ToolCall{
				SessionUpdate: schema.UpdateToolCall,
				ToolCallID:    "call-1",
				Title:         "cmpctl list ebs --json 2>&1",
				Kind:          schema.ToolKindExecute,
				Status:        schema.ToolStatusInProgress,
				RawInput:      map[string]any{"command": "cmpctl list ebs --json 2>&1"},
				Meta:          acpToolNameMeta("RUN_COMMAND"),
			},
		},
		{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "sess-regression",
			TurnID:    "turn-whitespace-tool",
			Final:     true,
			Update: schema.ToolCallUpdate{
				SessionUpdate: schema.UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Title:         stringPtr("cmpctl list ebs --json 2>&1"),
				Kind:          stringPtr(schema.ToolKindExecute),
				Status:        stringPtr(schema.ToolStatusFailed),
				RawInput:      map[string]any{"command": "cmpctl list ebs --json 2>&1"},
				RawOutput:     map[string]any{"exit_code": 1},
				Meta:          metautil.WithTerminalOutput(acpToolNameMeta("RUN_COMMAND"), "call-1", "{\"status\":\"error\"}\n"),
			},
		},
		{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "sess-regression",
			TurnID:    "turn-whitespace-tool",
			Final:     true,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				Content:       schema.TextContent{Type: "text", Text: "没有查到云硬盘。"},
			},
		},
		completedRegressionTurn("sess-regression", "turn-whitespace-tool"),
	} {
		updated, _ = model.Update(env)
		model = updated.(*Model)
	}

	block := requireMainACPTurnBlockForTest(t, model)
	for _, event := range block.Events {
		if activeNarrativeEventKind(event.Kind) && !renderableTextHasContent(event.Text) {
			t.Fatalf("main ACP events include whitespace-only narrative event: %#v", block.Events)
		}
	}
	rows := block.Render(model.blockRenderContext(96))
	plain := renderedPlainRows(rows)
	if !renderedRowsContainPlain(rows, "没有查到云硬盘。") {
		t.Fatalf("rendered rows missing final assistant text: %#v", plain)
	}
	toolIndex := -1
	for i, row := range plain {
		if strings.Contains(row, "Ran cmpctl list ebs --json 2>&1") {
			toolIndex = i
			break
		}
	}
	if toolIndex < 0 {
		t.Fatalf("rendered rows missing tool header: %#v", plain)
	}
	for i := 0; i < toolIndex; i++ {
		if strings.TrimSpace(plain[i]) == "·" {
			t.Fatalf("whitespace-only assistant chunk rendered as standalone prefix before tool header at row %d: %#v", i, plain)
		}
		if strings.TrimSpace(plain[i]) == "" {
			t.Fatalf("whitespace-only assistant chunk inserted fixed blank spacing before tool header at row %d: %#v", i, plain)
		}
	}
}

func acpToolNameMeta(name string) map[string]any {
	return metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: name,
	})
}

func completedRegressionTurn(sessionID string, turnID string) eventstream.Envelope {
	env := eventstream.TurnCompleted("", "", turnID, time.Unix(1, 0))
	env.SessionID = sessionID
	env.ScopeID = sessionID
	return env
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
