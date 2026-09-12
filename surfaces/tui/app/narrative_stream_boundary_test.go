package tuiapp

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestACPStreamStepBoundaryKeepsReasoningAfterAssistantAndTool(t *testing.T) {
	for _, lane := range []string{"main", "participant", "child"} {
		t.Run(lane, func(t *testing.T) {
			const width, height = 100, 32
			model := NewModel(Config{NoColor: true, NoAnimation: true})
			next, _ := model.Update(tea.WindowSizeMsg{Width: width, Height: height})
			model = next.(*Model)
			model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(500, 0))
			if lane == "child" {
				model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
					Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
					Update: eventstream.ToolCall{
						SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "spawn-1",
						Title: "Spawn reviewer: inspect", Kind: eventstream.ToolKindExecute, Status: eventstream.ToolStatusInProgress,
						RawInput: map[string]any{"agent": "reviewer", "prompt": "inspect"}, Meta: acpToolNameMeta("StartThread"),
					},
				})
			}
			apply := func(update eventstream.Update) {
				t.Helper()
				env := eventstream.Envelope{
					Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1",
					Scope: eventstream.ScopeMain, Update: update,
				}
				if lane != "main" {
					env.Scope, env.ScopeID, env.Actor = eventstream.ScopeSubagent, "reviewer", "reviewer"
				}
				if lane == "participant" {
					env.Scope, env.ParticipantID = eventstream.ScopeParticipant, "reviewer"
				}
				if lane == "child" {
					env.ParentTool = &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "StartThread"}
				}
				model = applyACPEnvelopeForTest(t, model, env)
			}
			reason := func(text string) {
				apply(eventstream.ContentChunk{
					SessionUpdate: eventstream.UpdateAgentThought, MessageID: "reused-message",
					Content: eventstream.TextContent{Type: "text", Text: text},
				})
			}
			var frames []string
			var plain string
			snapshot := func() []SubagentEvent {
				t.Helper()
				var events []SubagentEvent
				var rows []RenderedRow
				switch lane {
				case "main":
					block := requireMainACPTurnBlockForTest(t, model)
					events, rows = block.Events, block.Render(model.blockRenderContext(width))
				case "participant":
					block := model.findParticipantTurnBlock("turn-1")
					if block == nil {
						t.Fatal("participant block missing")
					}
					events, rows = block.Events, block.Render(model.blockRenderContext(width))
				case "child":
					block := requireSubagentOutputViewForTest(t, model, "spawn-1").block
					events, rows = block.Events, block.Render(model.blockRenderContext(width))
				}
				plain = joinRenderedPlain(rows)
				if len(rows) > height {
					t.Fatalf("transcript has %d rows, exceeds frame height %d", len(rows), height)
				}
				styled := make([]string, height)
				for i, row := range rows {
					styled[i] = row.Styled
				}
				frames = append(frames, strings.Join(styled, "\n"))
				return events
			}

			reason("step one reasoning")
			apply(eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "reused-message",
				Content: eventstream.TextContent{Type: "text", Text: "step one answer"},
			})
			apply(eventstream.ToolCall{
				SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "tool-1", Title: "RunCommand",
				Kind: eventstream.ToolKindExecute, Status: eventstream.ToolStatusInProgress,
				RawInput: map[string]any{"command": "echo boundary"}, Meta: acpToolNameMeta("RunCommand"),
			})
			snapshot()
			reason("step two reasoning")
			snapshot()
			// A sparse repair to the existing call is not a new step boundary.
			completed := eventstream.ToolStatusCompleted
			apply(eventstream.ToolCallUpdate{
				SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "tool-1", Status: &completed,
			})
			reason(" continued")
			events := snapshot()
			if len(events) != 4 || events[0].Kind != SEReasoning || events[0].Text != "step one reasoning" ||
				events[1].Kind != SEAssistant || events[1].Text != "step one answer" || events[2].Kind != SEToolCall ||
				events[3].Kind != SEReasoning || events[3].Text != "step two reasoning continued" {
				t.Fatalf("events = %#v, want reasoning → assistant → tool → next reasoning", events)
			}
			last := -1
			for _, text := range []string{"step one reasoning", "step one answer", "echo boundary", "step two reasoning continued"} {
				index := strings.Index(plain, text)
				if index <= last {
					t.Fatalf("missing or reordered %q in rendered transcript:\n%s", text, plain)
				}
				last = index
			}
			updates := renderFullscreenFramesForTest(t, width, height, frames...)
			assertPhysicalFullscreenFrame(t, width, height, frames[len(frames)-1], updates)
		})
	}
}
