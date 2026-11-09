package tuiapp

import (
	tea "charm.land/bubbletea/v2"
	"fmt"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/projection"
	"strings"
	"testing"
)

// User's four production-projection cases plus inline equivalents. Every click
// is taken from the current Render result, never an injected missing token.
func TestPR203FileOutputProductionDisclosureReview(t *testing.T) {
	for _, file := range []bool{true, false} {
		for _, child := range []bool{false, true} {
			for _, failed := range []bool{false, true} {
				name := fmt.Sprintf("file_%v_child_%v_failed_%v", file, child, failed)
				t.Run(name, func(t *testing.T) {
					m := NewModel(Config{NoColor: true, NoAnimation: true})
					m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
					input := map[string]any{"code": "return {marker:'INLINE_SOURCE'};"}
					if file {
						input = map[string]any{"file": "probe.js"}
					}
					events := []*session.Event{{SessionID: "review", Type: session.EventTypeToolCall, Tool: &session.EventTool{ID: "script", Name: "js_exec", Status: "started", Input: input}}}
					if child {
						for _, status := range []string{"started", "completed"} {
							typ := session.EventTypeToolCall
							if status == "completed" {
								typ = session.EventTypeToolResult
							}
							events = append(events, &session.Event{SessionID: "review", Type: typ, Visibility: session.VisibilityMirror, Tool: &session.EventTool{ID: "child", Name: "service__lookup", Status: status, Parent: &session.EventParentTool{CallID: "script", Name: "js_exec"}, Input: map[string]any{"q": "child query"}, Content: []session.EventToolContent{{Type: "content", Text: "CHILD_RESULT"}}}})
						}
					}
					status := "completed"
					if failed {
						status = "failed"
					}
					events = append(events, &session.Event{SessionID: "review", Type: session.EventTypeToolResult, Tool: &session.EventTool{ID: "script", Name: "js_exec", Status: status, Input: input, Content: []session.EventToolContent{{Type: "content", Text: "UNIQUE_PARENT_RESULT_OR_ROOT_ERROR"}}}})
					for _, event := range events {
						base := projection.EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: "review"}, event, projection.SessionEventTransport{TurnID: "review-turn"})
						for _, env := range projection.ProjectSessionEventEnvelope(base, event) {
							m = applyACPEnvelopeForTest(t, m, env)
						}
					}
					b := requireMainACPTurnBlockForTest(t, m)
					ctx := m.blockRenderContext(80)
					for i := 0; i < 3 && !b.toolPanelFullOutput("script"); i++ {
						rows := b.Render(ctx)
						token := ""
						for _, row := range rows {
							if row.ClickToken == acpToolPanelClickToken("script") {
								token = row.ClickToken
								break
							}
						}
						if token == "" {
							t.Fatal("no rendered script disclosure target")
						}
						if !m.tryToggleFoldToken(b.BlockID(), token) {
							t.Fatal("rendered script click not routed")
						}
					}
					after := joinRenderedPlain(b.Render(ctx))
					t.Logf("PRODUCTION PROJECTION FIXTURE\n%s", after)
					for _, want := range []string{"Ran script", "UNIQUE_PARENT_RESULT_OR_ROOT_ERROR"} {
						if !strings.Contains(after, want) {
							t.Fatalf("missing %s: %s", want, after)
						}
					}
					if file && !strings.Contains(after, "probe.js") {
						t.Fatal(after)
					}
					if !file && !strings.Contains(after, "INLINE_SOURCE") {
						t.Fatal(after)
					}
					if failed && !strings.Contains(after, "[failed]") {
						t.Fatal(after)
					}
					for _, hidden := range []string{"service__lookup", "CHILD_RESULT", "js_exec", "Code Mode", " calls", " ms"} {
						if strings.Contains(after, hidden) {
							t.Fatalf("mixed child/parent display %s: %s", hidden, after)
						}
					}
					if child {
						found := false
						for _, ev := range b.Events {
							if ev.CallID == "child" && ev.ParentToolCallID == "script" {
								found = true
							}
						}
						if !found {
							t.Fatal("child audit projection deleted")
						}
					}
				})
			}
		}
	}
}
