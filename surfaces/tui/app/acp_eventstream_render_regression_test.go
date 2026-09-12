package tuiapp

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/evalharness"
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
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateUserMessage,
				Content:       eventstream.TextContent{Type: "text", Text: "run the smoke check"},
			},
		},
		{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "sess-regression",
			Update: eventstream.ToolCall{
				SessionUpdate: eventstream.UpdateToolCall,
				ToolCallID:    "call-1",
				Title:         "go test ./surfaces/tui/app",
				Kind:          eventstream.ToolKindExecute,
				Status:        eventstream.ToolStatusInProgress,
				RawInput:      map[string]any{"command": "go test ./surfaces/tui/app"},
				Meta:          acpToolNameMeta("RunCommand"),
			},
		},
		{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "sess-regression",
			Final:     true,
			Update: eventstream.ToolCallUpdate{
				SessionUpdate: eventstream.UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Title:         stringPtr("go test ./surfaces/tui/app"),
				Kind:          stringPtr(eventstream.ToolKindExecute),
				Status:        stringPtr(eventstream.ToolStatusCompleted),
				RawInput:      map[string]any{"command": "go test ./surfaces/tui/app"},
				RawOutput:     map[string]any{"exit_code": 0},
				Meta:          testMeta.WithTerminalOutput(acpToolNameMeta("RunCommand"), "call-1", "ok\nPASS\n"),
			},
		},
		{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "sess-regression",
			Final:     true,
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage,
				Content:       eventstream.TextContent{Type: "text", Text: "Smoke check passed."},
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
		"/tmp/workspace",
	})
}

func TestRegressionACPEventstreamInterruptedTurnNotice80x24(t *testing.T) {
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
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(*Model)
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(120, 0))

	for _, env := range []eventstream.Envelope{
		{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "sess-interrupted",
			Scope:     eventstream.ScopeMain,
			TurnID:    "turn-interrupted",
			Final:     true,
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage,
				Content:       eventstream.TextContent{Type: "text", Text: "Partial response before the stop."},
			},
		},
		eventstream.TurnCancelled("handle-interrupted", "run-interrupted", "turn-interrupted", "tui interrupt", time.Unix(121, 0)),
	} {
		updated, _ = model.Update(env)
		model = updated.(*Model)
	}

	block := requireMainACPTurnBlockForTest(t, model)
	plainRows := renderedPlainRows(block.Render(model.blockRenderContext(72)))
	plain := strings.Join(plainRows, "\n")
	want := strings.Join([]string{
		"Partial response before the stop.",
		"",
		interruptedTurnTitle,
		interruptedTurnCause,
		interruptedTurnNext,
	}, "\n")
	if !strings.Contains(plain, want) {
		t.Fatalf("interrupted turn render missing product notice or spacing:\n%s", plain)
	}
	if strings.Contains(plain, "⊘") {
		t.Fatalf("interrupted turn render retained warning icon:\n%s", plain)
	}

	frame := evalharness.NormalizeFrame(model.View().Content)
	assertFrameContainsInOrder(t, "interrupted turn 80x24", frame, []string{
		"Partial response before the stop.",
		interruptedTurnTitle,
		interruptedTurnCause,
		interruptedTurnNext,
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
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage,
				Content:       eventstream.TextContent{Type: "text", Text: "\n"},
			},
		},
		{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "sess-regression",
			TurnID:    "turn-whitespace-tool",
			Update: eventstream.ToolCall{
				SessionUpdate: eventstream.UpdateToolCall,
				ToolCallID:    "call-1",
				Title:         "cmpctl list ebs --json 2>&1",
				Kind:          eventstream.ToolKindExecute,
				Status:        eventstream.ToolStatusInProgress,
				RawInput:      map[string]any{"command": "cmpctl list ebs --json 2>&1"},
				Meta:          acpToolNameMeta("RunCommand"),
			},
		},
		{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "sess-regression",
			TurnID:    "turn-whitespace-tool",
			Final:     true,
			Update: eventstream.ToolCallUpdate{
				SessionUpdate: eventstream.UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Title:         stringPtr("cmpctl list ebs --json 2>&1"),
				Kind:          stringPtr(eventstream.ToolKindExecute),
				Status:        stringPtr(eventstream.ToolStatusFailed),
				RawInput:      map[string]any{"command": "cmpctl list ebs --json 2>&1"},
				RawOutput:     map[string]any{"exit_code": 1},
				Meta:          testMeta.WithTerminalOutput(acpToolNameMeta("RunCommand"), "call-1", "{\"status\":\"error\"}\n"),
			},
		},
		{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "sess-regression",
			TurnID:    "turn-whitespace-tool",
			Final:     true,
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage,
				Content:       eventstream.TextContent{Type: "text", Text: "没有查到云硬盘。"},
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

func TestRegressionACPEventstreamContextCompactingHint120x32(t *testing.T) {
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
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(120, 0))

	for _, env := range []eventstream.Envelope{
		{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "sess-regression",
			TurnID:    "turn-compact",
			Scope:     eventstream.ScopeMain,
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentThought,
				Content:       eventstream.TextContent{Type: "text", Text: "Reviewing the remaining context budget."},
			},
		},
		{
			Kind:      eventstream.KindLifecycle,
			SessionID: "sess-regression",
			TurnID:    "turn-compact",
			Scope:     eventstream.ScopeMain,
			Delivery:  &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
			Lifecycle: &eventstream.Lifecycle{State: session.LifecycleStatusContextCompacting},
		},
	} {
		updated, _ = model.Update(env)
		model = updated.(*Model)
	}

	frame := evalharness.NormalizeFrame(model.View().Content)
	if !strings.Contains(frame, "Compacting context") {
		t.Fatalf("context compacting frame missing dedicated hint:\n%s", frame)
	}
	if strings.Contains(frame, "Thinking ·") || strings.Contains(frame, session.LifecycleStatusContextCompacting) {
		t.Fatalf("context compacting frame leaked generic/internal status:\n%s", frame)
	}
}

func TestRegressionACPEventstreamPromptBeforeFirstOutputOmitsWaitingRow120x32(t *testing.T) {
	t.Parallel()

	model := newACPEventstreamRegressionModel(t, 120, 32)
	model = applyACPEventstreamRegressionEnvelope(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "sess-regression",
		Final:     true,
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateUserMessage,
			Content:       eventstream.TextContent{Type: "text", Text: "inspect the remaining context"},
		},
	})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(120, 0))
	model = applyACPEventstreamRegressionEnvelope(t, model, eventstream.Envelope{
		Kind:      eventstream.KindLifecycle,
		SessionID: "sess-regression",
		TurnID:    "turn-waiting",
		Scope:     eventstream.ScopeMain,
		Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateRunning},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 0 || block.Status != eventstream.LifecycleStateRunning {
		t.Fatalf("main ACP block = events %#v status %q, want empty running turn", block.Events, block.Status)
	}

	frame := evalharness.NormalizeFrame(model.View().Content)
	assertFrameContainsInOrder(t, "prompt before first output 120x32", frame, []string{
		"inspect the remaining context",
		"Waiting for response",
	})
	if strings.Contains(frame, "waiting for agent output") {
		t.Fatalf("prompt-before-first-output frame rendered waiting placeholder:\n%s", frame)
	}
}

func TestRegressionACPEventstreamContextCompactingBeforeOutputOmitsWaitingRow120x32(t *testing.T) {
	t.Parallel()

	model := newACPEventstreamRegressionModel(t, 120, 32)
	model = applyACPEventstreamRegressionEnvelope(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "sess-regression",
		Final:     true,
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateUserMessage,
			Content:       eventstream.TextContent{Type: "text", Text: "inspect the remaining context"},
		},
	})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(120, 0))
	for _, env := range []eventstream.Envelope{
		{
			Kind:      eventstream.KindLifecycle,
			SessionID: "sess-regression",
			TurnID:    "turn-compact",
			Scope:     eventstream.ScopeMain,
			Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateRunning},
		},
		{
			Kind:      eventstream.KindLifecycle,
			SessionID: "sess-regression",
			TurnID:    "turn-compact",
			Scope:     eventstream.ScopeMain,
			Delivery:  &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
			Lifecycle: &eventstream.Lifecycle{State: session.LifecycleStatusContextCompacting},
		},
	} {
		model = applyACPEventstreamRegressionEnvelope(t, model, env)
	}

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 0 {
		t.Fatalf("compact-before-output events = %#v, want no transcript facts", block.Events)
	}
	if block.Status == session.LifecycleStatusContextCompacting {
		t.Fatalf("main Turn status = %q, transient compact activity must remain hint-only", block.Status)
	}

	frame := evalharness.NormalizeFrame(model.View().Content)
	assertFrameContainsInOrder(t, "compacting before output 120x32", frame, []string{
		"inspect the remaining context",
		"Compacting context",
	})
	if strings.Contains(frame, "waiting for agent output") {
		t.Fatalf("compacting-before-output frame rendered waiting placeholder:\n%s", frame)
	}
	if strings.Contains(frame, "Waiting for response") || strings.Contains(frame, "Thinking ·") {
		t.Fatalf("compacting-before-output frame leaked model-wait/thinking hint:\n%s", frame)
	}
}

func newACPEventstreamRegressionModel(t *testing.T, width, height int) *Model {
	t.Helper()
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
	updated, _ := model.Update(tea.WindowSizeMsg{Width: width, Height: height})
	typed, ok := updated.(*Model)
	if !ok {
		t.Fatalf("model = %T, want *Model", updated)
	}
	return typed
}

func applyACPEventstreamRegressionEnvelope(t *testing.T, model *Model, env eventstream.Envelope) *Model {
	t.Helper()
	updated, _ := model.Update(env)
	typed, ok := updated.(*Model)
	if !ok {
		t.Fatalf("model = %T, want *Model", updated)
	}
	return typed
}

func acpToolNameMeta(name string) map[string]any {
	return testMeta.WithRuntimeSection(nil, testMeta.RuntimeTool, map[string]any{
		testMeta.RuntimeToolName: name,
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
