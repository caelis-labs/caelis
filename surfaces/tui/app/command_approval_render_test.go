package tuiapp

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/evalharness"
)

func TestCommandApprovalTaskKeepsOneLiveRowWhileModelContinues(t *testing.T) {
	t.Parallel()
	m := NewModel(Config{NoColor: true, NoAnimation: true, Workspace: "/tmp/workspace"})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = updated.(*Model)
	m.beginLiveTurn(SubmissionModeDefault, false, time.Now())
	m = applyACPEnvelopeForTest(t, m, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCall{SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "command-1", Title: "go test ./...", Kind: eventstream.ToolKindExecute, Status: eventstream.ToolStatusInProgress,
			RawInput: map[string]any{"command": "go test ./..."}, Meta: acpToolNameMeta("RunCommand")},
	})
	m = applyACPEnvelopeForTest(t, m, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "command-1", Status: stringPtr(eventstream.ToolStatusInProgress),
			RawOutput: map[string]any{"handle": "command", "state": "waiting_approval", "supports_input": false}, Meta: acpToolNameMeta("RunCommand")},
	})
	block := requireMainACPTurnBlockForTest(t, m)
	physical := physicalTranscriptEventsForTest(block.Events)
	if len(physical) != 1 || physical[0].Done || physical[0].TaskHandle != "command" {
		t.Fatalf("pending command row = %#v", physical)
	}
	m = applyACPEnvelopeForTest(t, m, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, Content: eventstream.TextContent{Type: "text", Text: "Inspecting independent files while approval is pending."}},
	})
	m.syncViewportContent()
	frame := evalharness.NormalizeFrame(m.View().Content)
	if strings.Count(frame, "go test ./...") != 1 || !strings.Contains(frame, "Inspecting independent files") || !m.turnRunning() {
		t.Fatalf("pending frame did not preserve independent progress:\n%s", frame)
	}
	m = applyACPEnvelopeForTest(t, m, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "command-1", Status: stringPtr(eventstream.ToolStatusCompleted),
			RawOutput: map[string]any{"handle": "command", "state": "completed", "exit_code": 0}, Meta: testMeta.WithTerminalOutput(acpToolNameMeta("RunCommand"), "command-1", "PASS\n")},
	})
	physical = physicalTranscriptEventsForTest(requireMainACPTurnBlockForTest(t, m).Events)
	commands := 0
	for _, event := range physical {
		if event.CallID == "command-1" {
			commands++
			if !event.Done || event.Err {
				t.Fatalf("settled row = %#v", event)
			}
		}
	}
	if commands != 1 || !m.turnRunning() {
		t.Fatalf("command rows=%d turn running=%v", commands, m.turnRunning())
	}
}
