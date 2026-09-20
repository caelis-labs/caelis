package tuiapp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
)

func TestChildHistoryPagesStayPrivateUntilCommit(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.currentSessionID = "session"
	m.taskStreamWanted["task"] = true
	m.taskStreamTokens["task"] = 7
	m.taskStreamCallIDsByID["task"] = "spawn"
	view := m.ensureSubagentOutputView("spawn")
	view.block.AppendStreamEvent(SEAssistant, "old history", narrativeSourceIdentity{})
	old := view.document
	apply := func(kind taskstream.DeliveryKind, events []eventstream.Envelope) {
		m.handleTaskStreamBatch(taskStreamBatchMsg{sessionID: "session", taskID: "task", token: 7, phase: kind, events: events, cursor: "committed"})
	}
	apply(taskstream.DeliveryReplaceBegin, nil)
	for page := range 3 {
		apply(taskstream.DeliveryReplacePage, []eventstream.Envelope{{
			Kind: eventstream.KindSessionUpdate, SessionID: "session", TurnID: "turn", Scope: eventstream.ScopeSubagent,
			ParentTool: &eventstream.ParentToolRelation{ToolCallID: "spawn", ToolName: "StartThread"},
			Update:     eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "final", Content: eventstream.TextContent{Type: "text", Text: fmt.Sprint(page)}},
		}})
		if view.document != old || m.taskStreamCursors["task"] != "" {
			t.Fatal("partial history or cursor became visible")
		}
	}
	apply(taskstream.DeliveryReplaceEnd, nil)
	if view.document == old || !view.historyResolved || m.taskStreamCursors["task"] != "committed" {
		t.Fatal("complete history did not commit")
	}
	text := ""
	for _, event := range view.block.Events {
		if event.Kind == SEAssistant {
			text += event.Text
		}
	}
	if text != "012" {
		t.Fatalf("child final response = %q", text)
	}
	old = view.document
	apply(taskstream.DeliveryReplaceBegin, nil)
	m.handleTaskStreamClosed(taskStreamClosedMsg{sessionID: "session", taskID: "task", token: 7, err: fmt.Errorf("broken history")})
	if view.document != old || view.history != nil {
		t.Fatal("failed child replay replaced the committed document")
	}
}

func TestChildHistorySendMessageResultPresentation(t *testing.T) {
	for _, tc := range []struct {
		name, tool, status, output, want, hidden string
	}{
		{"receipt", "SendMessage", eventstream.ToolStatusCompleted, `{"id":"mail-1","status":"queued"}`, "@parent: progress update", "queued"},
		{"failure", "SendMessage", eventstream.ToolStatusFailed, "Agent cannot accept messages while running", "Agent cannot accept messages while running", "queued"},
		{"generic", "mcp_echo", eventstream.ToolStatusCompleted, `{"id":"mail-1","status":"queued"}`, "queued", "@parent:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel(Config{NoColor: true, NoAnimation: true})
			m.currentSessionID = "session"
			m.taskStreamWanted["task"] = true
			m.taskStreamTokens["task"] = 7
			m.taskStreamCallIDsByID["task"] = "spawn"
			view := m.ensureSubagentOutputView("spawn")
			apply := func(kind taskstream.DeliveryKind, events []eventstream.Envelope) {
				m.handleTaskStreamBatch(taskStreamBatchMsg{sessionID: "session", taskID: "task", token: 7, phase: kind, events: events})
			}
			apply(taskstream.DeliveryReplaceBegin, nil)
			apply(taskstream.DeliveryReplacePage, []eventstream.Envelope{{
				Kind: eventstream.KindSessionUpdate, SessionID: "session", TurnID: "turn", Scope: eventstream.ScopeSubagent,
				ParentTool: &eventstream.ParentToolRelation{ToolCallID: "spawn", ToolName: "StartThread"},
				Update: eventstream.ToolCall{
					SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "send", Title: tc.tool, Kind: eventstream.ToolKindOther, Status: tc.status,
					RawInput: map[string]any{"to": "parent", "message": "progress update"}, Meta: acpToolNameMeta(tc.tool),
					Content: []eventstream.ToolCallContent{{Type: "content", Content: eventstream.TextContent{Type: "text", Text: tc.output}}},
				},
			}})
			apply(taskstream.DeliveryReplaceEnd, nil)
			view.prepareVisibleRender()
			plain := renderedRowsPlain(m.subagentOutputRows(view, 120, 40))
			if !strings.Contains(plain, tc.want) || strings.Contains(plain, tc.hidden) {
				t.Fatalf("child history result:\n%s", plain)
			}
		})
	}
}
