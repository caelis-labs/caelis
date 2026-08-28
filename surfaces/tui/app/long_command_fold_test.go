package tuiapp

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func TestProjectACPToolCallFoldsLongCommandAndKeepsFullArgs(t *testing.T) {
	t.Parallel()

	command := longCommandFoldFixture()
	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall,
			ToolCallID:    "command-1",
			Title:         "RunCommand python3",
			Kind:          eventstream.ToolKindExecute,
			Status:        eventstream.ToolStatusInProgress,
			RawInput:      map[string]any{"command": command},
			Meta:          acpToolNameMeta("RunCommand"),
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one command event", events)
	}
	event := events[0]
	if event.ToolFullArgs != command {
		t.Fatalf("ToolFullArgs = %q, want exact command %q", event.ToolFullArgs, command)
	}
	if strings.Contains(event.ToolArgs, "\n") {
		t.Fatalf("ToolArgs = %q, want a single-line folded preview", event.ToolArgs)
	}
	if !strings.Contains(event.ToolArgs, "... +25 lines") {
		t.Fatalf("ToolArgs = %q, want hidden-line count", event.ToolArgs)
	}
	if displayColumns(event.ToolArgs) > toolArgsPreviewWidth {
		t.Fatalf("ToolArgs width = %d, want <= %d: %q", displayColumns(event.ToolArgs), toolArgsPreviewWidth, event.ToolArgs)
	}

	singleLine := "python3 -c " + strings.Repeat(`print("long command");`, 12)
	generic := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall,
			ToolCallID:    "command-generic",
			Title:         "Shell",
			Kind:          eventstream.ToolKindExecute,
			Status:        eventstream.ToolStatusInProgress,
			RawInput:      map[string]any{"command": singleLine},
		},
	})
	if len(generic) != 1 {
		t.Fatalf("generic execute events = %#v, want one command event", generic)
	}
	if generic[0].ToolFullArgs != singleLine || generic[0].ToolArgs == singleLine ||
		!strings.Contains(generic[0].ToolArgs, "...") ||
		displayColumns(generic[0].ToolArgs) > toolArgsPreviewWidth {
		t.Fatalf("generic single-line command event = %#v, want folded preview and exact full arguments", generic[0])
	}

	short := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall,
			ToolCallID:    "command-2",
			Title:         "RunCommand go test ./...",
			Kind:          eventstream.ToolKindExecute,
			Status:        eventstream.ToolStatusInProgress,
			RawInput:      map[string]any{"command": "go test ./..."},
			Meta:          acpToolNameMeta("RunCommand"),
		},
	})
	if len(short) != 1 || short[0].ToolArgs != "go test ./..." || short[0].ToolFullArgs != "" {
		t.Fatalf("short command event = %#v, want unchanged inline arguments", short)
	}
}

func TestProjectACPToolCallFoldsLongCommandFromTitleWithoutRawInput(t *testing.T) {
	t.Parallel()

	command := "python3 -c " + strings.Repeat(`print("title only");`, 12)
	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall,
			ToolCallID:    "command-title-only",
			Title:         "RunCommand " + command,
			Kind:          eventstream.ToolKindExecute,
			Status:        eventstream.ToolStatusInProgress,
			Meta:          acpToolNameMeta("RunCommand"),
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one command event", events)
	}
	event := events[0]
	if event.ToolFullArgs != command {
		t.Fatalf("ToolFullArgs = %q, want exact title command %q", event.ToolFullArgs, command)
	}
	if event.ToolArgs == command || !strings.Contains(event.ToolArgs, "...") {
		t.Fatalf("ToolArgs = %q, want a folded title command preview", event.ToolArgs)
	}
	if displayColumns(event.ToolArgs) > toolArgsPreviewWidth {
		t.Fatalf("ToolArgs width = %d, want <= %d: %q", displayColumns(event.ToolArgs), toolArgsPreviewWidth, event.ToolArgs)
	}
}

func TestLongCommandHeaderClickExpandsAndCollapsesCommand(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*Model)
	command := longCommandFoldFixture()
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall,
			ToolCallID:    "command-1",
			Title:         "RunCommand python3",
			Kind:          eventstream.ToolKindExecute,
			Status:        eventstream.ToolStatusInProgress,
			RawInput:      map[string]any{"command": command},
			Meta:          acpToolNameMeta("RunCommand"),
		},
	})
	completed := eventstream.ToolStatusCompleted
	title := "RunCommand python3"
	kind := eventstream.ToolKindExecute
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Final:     true,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "command-1",
			Title:         &title,
			Kind:          &kind,
			Status:        &completed,
			Meta:          acpToolNameMeta("RunCommand"),
		},
	})
	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 || !strings.Contains(block.Events[0].Args, "... +25 lines") || block.Events[0].FullArgs != command {
		t.Fatalf("completed command event = %#v, want folded start arguments preserved", block.Events)
	}
	model.syncViewportContent()

	headerLine := -1
	for index, line := range model.viewportPlainLines {
		if strings.Contains(line, "• Ran python3") {
			headerLine = index
			break
		}
	}
	if headerLine < 0 {
		t.Fatalf("folded command header missing: %#v", model.viewportPlainLines)
	}
	if token := model.viewportClickTokens[headerLine]; token != "acp_tool_panel:command-1" {
		t.Fatalf("header click token = %q, want command panel token", token)
	}
	if plain := strings.Join(model.viewportPlainLines, "\n"); strings.Contains(plain, "marker_line_13") {
		t.Fatalf("folded transcript leaked the middle of the command:\n%s", plain)
	}

	clickViewportLine(t, model, headerLine)
	if !block.toolPanelFullOutput("command-1") {
		t.Fatal("full command state = false after clicking folded header")
	}
	if plain := strings.Join(model.viewportPlainLines, "\n"); !strings.Contains(plain, "marker_line_13") {
		t.Fatalf("expanded transcript missing the full command:\n%s", plain)
	}

	headerLine = -1
	for index, line := range model.viewportPlainLines {
		if strings.Contains(line, "• Ran python3") {
			headerLine = index
			break
		}
	}
	if headerLine < 0 {
		t.Fatalf("expanded command header missing: %#v", model.viewportPlainLines)
	}
	clickViewportLine(t, model, headerLine)
	if block.toolPanelFullOutput("command-1") {
		t.Fatal("full command state = true after collapsing command")
	}
	if plain := strings.Join(model.viewportPlainLines, "\n"); strings.Contains(plain, "marker_line_13") {
		t.Fatalf("collapsed transcript still contains the full command:\n%s", plain)
	}
}

func clickViewportLine(t *testing.T, model *Model, line int) {
	t.Helper()
	mouse := tea.Mouse{
		Button: tea.MouseLeft,
		X:      model.mainColumnX() + tuikit.GutterNarrative + 2,
		Y:      line - model.viewportVisibleOffset(),
	}
	_ = model.handleViewportMousePress(mouse)
	_ = model.handleViewportMouseRelease(mouse)
}

func longCommandFoldFixture() string {
	lines := []string{"python3 - <<'PY'"}
	for index := 1; index <= 24; index++ {
		lines = append(lines, fmt.Sprintf("marker_line_%02d = %q", index, strings.Repeat("x", 16)))
	}
	lines = append(lines, "PY")
	return strings.Join(lines, "\n")
}
