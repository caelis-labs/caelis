package tuiapp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	acpprojector "github.com/caelis-labs/caelis/control/appserver/projection"
)

func TestApprovalDecisionReplayRendersOnCommandHeader(t *testing.T) {
	for _, status := range []string{"approved", "denied"} {
		for _, liveFirst := range []bool{false, true} {
			m := NewModel(Config{NoColor: true, NoAnimation: true})
			m.currentSessionID = "session-1"
			tool := eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
				Update: eventstream.ToolCall{SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "call-1", Title: "RunCommand", Kind: eventstream.ToolKindExecute, Status: eventstream.ToolStatusCompleted,
					RawInput: map[string]any{"command": "git status"}, Meta: acpToolNameMeta("RunCommand")},
			}
			m = applyACPEnvelopeForTest(t, m, tool)
			event := &session.Event{ID: "decision-1", Seq: 7, Type: session.EventTypeLifecycle, Visibility: session.VisibilityJournal,
				Journal: &session.ExecutionJournalEntry{Kind: session.JournalKindPauseToken, PauseToken: &session.PauseToken{
					Status: session.PauseTokenResolved, TurnID: "turn-1", ToolCallID: "call-1", ToolName: "RunCommand", Approved: status == "approved",
					Input: json.RawMessage(`{"command":"git status"}`), ReviewText: status + ": review reason",
				}},
			}
			base := acpprojector.EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: "session-1"}, event, acpprojector.SessionEventTransport{})
			out := acpprojector.ProjectSessionEventEnvelope(base, event)
			if len(out) != 1 {
				t.Fatalf("decision projection = %#v", out)
			}
			if liveFirst {
				live := out[0]
				live.EventID, live.Position, live.Delivery = "live-review", nil, &eventstream.Delivery{Mode: eventstream.DeliveryTransient}
				m = applyACPEnvelopeForTest(t, m, live)
			}
			m = applyACPEnvelopeForTest(t, m, out[0])
			var lines []string
			for _, block := range m.doc.Blocks() {
				lines = append(lines, renderedPlainRows(block.Render(m.blockRenderContext(100)))...)
			}
			plain := strings.Join(lines, "\n")
			t.Log(plain)
			if strings.Count(plain, "git status ["+status+"]") != 1 {
				t.Fatalf("missing or duplicate review header (live=%v): %s", liveFirst, plain)
			}
			if status == "denied" && strings.Count(plain, "review reason") != 1 {
				t.Fatalf("denial reason missing or duplicated: %s", plain)
			}
		}
	}
}
