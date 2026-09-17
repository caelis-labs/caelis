package tuiapp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	acpprojector "github.com/caelis-labs/caelis/control/appserver/projection"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
)

func TestChildApprovalReplaySurvivesDeferredToolHistory(t *testing.T) {
	for _, status := range []string{"approved", "denied"} {
		for _, when := range []string{"hidden", "parent-page", "begin", "page", "end"} {
			t.Run(status+"/"+when, func(t *testing.T) {
				m := NewModel(Config{NoColor: true, NoAnimation: true})
				m.currentSessionID = "session-1"
				m = applyACPEnvelopeForTest(t, m, eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "parent", Scope: eventstream.ScopeMain,
					Update: eventstream.ToolCall{SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "spawn-1", Title: "StartThread helper", Status: eventstream.ToolStatusCompleted, Meta: acpToolNameMeta("StartThread")}})
				decision := &session.Event{ID: "review-1", Seq: 7, Type: session.EventTypeLifecycle, Visibility: session.VisibilityJournal,
					Journal: &session.ExecutionJournalEntry{Kind: session.JournalKindPauseToken, PauseToken: &session.PauseToken{
						Status: session.PauseTokenResolved, TurnID: "parent", ToolCallID: "child-call", ToolName: "RunCommand", Approved: status == "approved", ReviewText: status + ": review reason", Input: json.RawMessage(`{"command":"git status"}`),
						Metadata: map[string]any{"subagent": true, "task_id": "task-1", "parent_call_id": "spawn-1", "parent_tool": "StartThread"}}}}
				base := acpprojector.EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: "session-1"}, decision, acpprojector.SessionEventTransport{})
				review := acpprojector.ProjectSessionEventEnvelope(base, decision)[0]
				sendReview := func() {
					if when == "parent-page" {
						older := NewModel(Config{NoColor: true, NoAnimation: true})
						older = applyACPEnvelopeForTest(t, older, review)
						m.prependSessionHistory(older)
						return
					}
					live := review
					live.EventID, live.ProjectionID, live.Position = "live-review", "", nil
					live.Delivery = &eventstream.Delivery{Mode: eventstream.DeliveryTransient}
					m = applyACPEnvelopeForTest(t, m, live)
					m = applyACPEnvelopeForTest(t, m, review)
					m = applyACPEnvelopeForTest(t, m, review)
				}
				if when == "hidden" || when == "parent-page" {
					sendReview()
				}
				// The parent feed is already complete before the panel is opened.
				m.subagentOutputOverlay = &subagentOutputOverlayState{callID: "spawn-1"}
				m.taskStreamWanted["task-1"] = true
				m.taskStreamTokens["task-1"] = 7
				m.taskStreamCallIDsByID["task-1"] = "spawn-1"
				m.taskStreamIDsByCallID["spawn-1"] = "task-1"
				tool := eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "child-turn", Scope: eventstream.ScopeSubagent, ScopeID: "task-1", ParentTool: &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "StartThread"},
					Update: eventstream.ToolCall{SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "child-call", Title: "RunCommand", Kind: eventstream.ToolKindExecute, Status: eventstream.ToolStatusCompleted, RawInput: map[string]any{"command": "git status"}, Meta: acpToolNameMeta("RunCommand")}}
				for pass := range 2 {
					if pass == 1 {
						// Cold-pane eviction uses this same projection reset before reload.
						m.subagentOutputViews["spawn-1"].resetForReplacement()
					}
					// Replacing a pane must retain even reviews already attached previously.
					for _, phase := range []taskstream.DeliveryKind{taskstream.DeliveryReplaceBegin, taskstream.DeliveryReplacePage, taskstream.DeliveryReplaceEnd} {
						var events []eventstream.Envelope
						if phase == taskstream.DeliveryReplacePage {
							events = []eventstream.Envelope{tool}
						}
						m.handleTaskStreamBatch(taskStreamBatchMsg{sessionID: "session-1", taskID: "task-1", token: 7, phase: phase, events: events, cursor: fmt.Sprint(pass)})
						if pass == 0 && ((when == "begin" && phase == taskstream.DeliveryReplaceBegin) || (when == "page" && phase == taskstream.DeliveryReplacePage) || (when == "end" && phase == taskstream.DeliveryReplaceEnd)) {
							sendReview()
						}
					}
					view := m.subagentOutputViews["spawn-1"]
					view.prepareVisibleRender()
					plain := strings.Join(renderedPlainRows(m.subagentOutputRows(view, 100, 24)), "\n")
					if strings.Count(plain, "git status ["+status+"]") != 1 {
						t.Fatalf("pass %d missing or repeated review:\n%s", pass, plain)
					}
					if status == "denied" && strings.Count(plain, "review reason") != 1 {
						t.Fatalf("denial reason lost or duplicated:\n%s", plain)
					}
					if view.document.Len() != 1 {
						t.Fatal("review created a parent Turn inside the child pane")
					}
				}
			})
		}
	}
}
