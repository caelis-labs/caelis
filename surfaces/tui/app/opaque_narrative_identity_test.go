package tuiapp

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestOpaqueNarrativeIDsRemainDistinctInLiveAndReplayDisplay(t *testing.T) {
	for _, lane := range []string{"main", "participant", "child"} {
		for _, kind := range []string{eventstream.UpdateAgentMessage, eventstream.UpdateAgentThought} {
			t.Run(lane+"/"+kind, func(t *testing.T) {
				newModel := func() *Model {
					m := NewModel(Config{NoColor: true, NoAnimation: true})
					next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
					m = next.(*Model)
					m.beginLiveTurn(SubmissionModeDefault, false, time.Unix(500, 0))
					if lane == "child" {
						m = applyACPEnvelopeForTest(t, m, eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "session", TurnID: "turn", Scope: eventstream.ScopeMain, Update: eventstream.ToolCall{SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "spawn", Name: stringPtr("StartThread"), Status: "in_progress"}})
					}
					return m
				}
				apply := func(m *Model, id, text string, replace bool) *Model {
					env := eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "session", TurnID: "turn", Scope: eventstream.ScopeMain, Update: eventstream.ContentChunk{SessionUpdate: kind, MessageID: id, Content: eventstream.TextContent{Type: "text", Text: text}, Replacement: replace}}
					if lane != "main" {
						env.Scope, env.ScopeID, env.Actor = eventstream.ScopeSubagent, "child", "child"
					}
					if lane == "participant" {
						env.Scope, env.ParticipantID = eventstream.ScopeParticipant, "participant"
					}
					if lane == "child" {
						env.ParentTool = &eventstream.ParentToolRelation{ToolCallID: "spawn", ToolName: "StartThread"}
					}
					return applyACPEnvelopeForTest(t, m, env)
				}
				render := func(m *Model) string {
					if lane == "main" {
						return joinRenderedPlain(requireMainACPTurnBlockForTest(t, m).Render(m.blockRenderContext(100)))
					}
					if lane == "participant" {
						return joinRenderedPlain(m.findParticipantTurnBlock("turn").Render(m.blockRenderContext(100)))
					}
					return joinRenderedPlain(requireSubagentOutputViewForTest(t, m, "spawn").block.Render(m.blockRenderContext(100)))
				}
				live := apply(newModel(), " answer ", "old body", false)
				live = apply(live, "answer", "current body", true)
				live = apply(live, " answer ", "revised old body", true)
				got := render(live)
				if !strings.Contains(got, "current body") || !strings.Contains(got, "revised old body") {
					t.Fatalf("distinct narrative identities collided: %q", got)
				}
				live = apply(live, " answer ", "", true)
				if got := render(live); !strings.Contains(got, "current body") || strings.Contains(got, "old body") {
					t.Fatalf("old-ID clear changed current narrative: %q", got)
				}
				replay := apply(newModel(), " answer ", "", true)
				replay = apply(replay, "answer", "current body", true)
				if got, want := render(live), render(replay); got != want {
					t.Fatalf("live/replay differ: live=%q replay=%q", got, want)
				}
			})
		}
	}
}

func TestOpaqueObservedUserAndNarrativeBatchIDsRemainDistinct(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	for _, id := range []string{" user ", "user"} {
		m.applyGatewayUserEcho(gatewayUserEchoOptions{displayLine: id + " body", event: TranscriptEvent{Scope: ACPProjectionMain, ScopeID: "session", TurnID: "turn", MessageID: id, Replacement: true}})
	}
	m.applyGatewayUserEcho(gatewayUserEchoOptions{event: TranscriptEvent{Scope: ACPProjectionMain, ScopeID: "session", TurnID: "turn", MessageID: " user ", Replacement: true}})
	var bodies []string
	for _, item := range m.doc.Blocks() {
		if b, ok := item.(*UserNarrativeBlock); ok {
			bodies = append(bodies, b.Raw)
		}
	}
	if len(bodies) != 2 || bodies[0] != "" || bodies[1] != "user body" {
		t.Fatalf("distinct observed user identities collided: %#v", bodies)
	}
	keys := map[string]bool{}
	for _, id := range []string{" answer ", "answer", " "} {
		env := eventstream.Envelope{Kind: eventstream.KindSessionUpdate, Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, MessageID: id, Content: eventstream.TextContent{Type: "text", Text: "delta"}}}
		key, ok := eventStreamNarrativeBatchKey(env)
		if !ok || keys[key] || newNarrativeSourceIdentity(id, "", "").stableKey() != "message:"+id {
			t.Fatalf("narrative batch identity collapsed %q", id)
		}
		keys[key] = true
	}
}
