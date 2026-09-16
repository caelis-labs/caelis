package projection

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestApprovalDecisionJournalProjectsDisplayWithoutControlState(t *testing.T) {
	for _, approved := range []bool{true, false} {
		for _, child := range []bool{false, true} {
			event := &session.Event{ID: "decision-1", Seq: 17, Type: session.EventTypeLifecycle, Visibility: session.VisibilityJournal,
				Meta: map[string]any{"private": "must-not-leak"},
				Journal: &session.ExecutionJournalEntry{Kind: session.JournalKindPauseToken, PauseToken: &session.PauseToken{
					TokenID: "private-token", RunID: "private-run", TurnID: "turn-1", ToolCallID: "call-1", ToolName: "RunCommand",
					Status: session.PauseTokenResolved, Approved: approved, ReviewText: "review complete",
					Input: json.RawMessage(`{"command":"git status"}`), Metadata: map[string]any{"private": "must-not-leak"},
				}},
			}
			if child {
				event.Journal.PauseToken.Metadata = map[string]any{"subagent": true, "task_id": "task-1", "parent_call_id": "spawn-1", "parent_tool": "StartThread", "agent": "reviewer"}
			}
			base := EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: "session-1"}, event, SessionEventTransport{})
			out := ProjectSessionEventEnvelope(base, event)
			if len(out) != 1 {
				t.Fatalf("projection = %#v", out)
			}
			env := out[0]
			want := "denied"
			if approved {
				want = "approved"
			}
			if env.Kind != eventstream.KindApprovalReview || env.TurnID != "turn-1" || env.ApprovalReview.Status != want || env.ApprovalReview.RawInput["command"] != "git status" || env.Delivery.Mode != eventstream.DeliveryMirror || env.Position.Durable.Seq != 17 {
				t.Fatalf("decision projection = %#v", env)
			}
			if child && (env.Scope != eventstream.ScopeSubagent || env.ScopeID != "task-1" || env.ParentTool == nil || env.ParentTool.ToolCallID != "spawn-1" || env.Actor != "reviewer") {
				t.Fatalf("child decision routed to parent: %#v", env)
			}
			data, err := json.Marshal(env)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"must-not-leak", "private-token", "private-run", "pause_token", "journal"} {
				if strings.Contains(string(data), secret) {
					t.Fatalf("control state leaked: %s", data)
				}
			}
		}
	}
}
