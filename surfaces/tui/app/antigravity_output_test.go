package tuiapp

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

func TestAntigravityCommandOutputRendersAfterApprovalInMainAndParticipant(t *testing.T) {
	data, err := os.ReadFile("../../../internal/acpagentbridge/client/testdata/antigravity-command.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []eventstream.Scope{eventstream.ScopeMain, eventstream.ScopeParticipant} {
		t.Run(string(scope), func(t *testing.T) {
			m := NewModel(Config{NoColor: true, NoAnimation: true})
			m.width, m.height, m.ready = 100, 30, true
			base := eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: scope, ScopeID: "turn-1", Actor: "Antigravity"}
			if scope == eventstream.ScopeParticipant {
				base.ParticipantID = "antigravity"
			}
			for i, wire := range raw {
				var update client.Update
				if i == 0 {
					var call client.ToolCall
					if err := json.Unmarshal(wire, &call); err != nil {
						t.Fatal(err)
					}
					update = call
				} else {
					var patch client.ToolCallUpdate
					if err := json.Unmarshal(wire, &patch); err != nil {
						t.Fatal(err)
					}
					update = patch
				}
				normalized, err := json.Marshal(client.NormalizeInboundUpdate(update))
				if err != nil {
					t.Fatal(err)
				}
				u, err := eventstream.DecodeUpdateJSON(normalized)
				if err != nil {
					t.Fatal(err)
				}
				env := base
				env.Update = u
				m = applyACPEnvelopeForTest(t, m, env)
				if i == 0 {
					approval := base
					approval.Kind = eventstream.KindApprovalReview
					approval.ApprovalReview = &eventstream.ApprovalReview{ToolCallID: "echo-1", ToolName: "execute", Status: "approved", Text: "approved"}
					m = applyACPEnvelopeForTest(t, m, approval)
				}
				if i == len(raw)-1 {
					// Repeated result snapshots replace the collection, never append a
					// second terminal delta or erase the retained approval.
					m = applyACPEnvelopeForTest(t, m, env)
				}
			}
			var events []SubagentEvent
			var rows []RenderedRow
			if scope == eventstream.ScopeMain {
				block := requireMainACPTurnBlockForTest(t, m)
				events, rows = block.Events, block.Render(m.blockRenderContext(96))
			} else {
				block := m.findParticipantTurnBlock("turn-1")
				if block == nil {
					t.Fatal("missing participant turn")
				}
				events, rows = block.Events, block.Render(m.blockRenderContext(96))
			}
			found := false
			for _, event := range events {
				if event.Kind == SEToolCall && strings.TrimSpace(event.Output) == "AGY_OUTPUT_OK" {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing command output: %#v", events)
			}
			plain := joinRenderedPlain(rows)
			if strings.Count(plain, "AGY_OUTPUT_OK") != 2 || !strings.Contains(plain, "approved") {
				t.Fatalf("expected command plus one output and approval:\n%s", plain)
			}
			t.Logf("%s:\n%s", scope, plain)
		})
	}
}
