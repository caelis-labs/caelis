package tuiapp

import (
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestRegressionSubagentInputKeepsItsPlaceWithinTurn(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		name := "live"
		if replacement {
			name = "replacement"
		}
		t.Run(name, func(t *testing.T) {
			initial := "initial assignment\n" + overlayNavLongText("initial-prompt-end")
			guidance := "first guidance\n" + overlayNavLongText("follow-up-end")
			model := overlayNavModel(t)
			model = applyOverlayNavSpawn(t, model, "parent-turn", "spawn-1", "child", initial)
			block := requireMainACPTurnBlockForTest(t, model)
			if !model.openSubagentOutputOverlay(block.BlockID(), "spawn-1") {
				t.Fatal("Spawn did not open child overlay")
			}
			model.taskStreamWanted["task-1"] = true
			model.taskStreamTokens["task-1"] = 7
			model.taskStreamIDsByCallID["spawn-1"] = "task-1"
			model.taskStreamCallIDsByID["task-1"] = "spawn-1"
			view := requireSubagentOutputViewForTest(t, model, "spawn-1")
			if replacement {
				view.observeChildEvent(TranscriptEvent{Kind: TranscriptEventNarrative, NarrativeKind: TranscriptNarrativeAssistant, Text: "stale prefix"})
			}
			chunk := func(kind, id, text string) eventstream.Envelope {
				return eventstream.Envelope{Kind: eventstream.KindSessionUpdate, Update: eventstream.ContentChunk{
					SessionUpdate: kind, MessageID: id, Content: eventstream.TextContent{Type: "text", Text: text},
				}}
			}
			completed := eventstream.ToolStatusCompleted
			events := []eventstream.Envelope{
				chunk(eventstream.UpdateUserMessage, "initial", initial),
				chunk(eventstream.UpdateAgentMessage, "before", "before guidance"),
				{Kind: eventstream.KindSessionUpdate, Update: eventstream.ToolCall{
					SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "read-1", Title: "Read file.go", Kind: eventstream.ToolKindRead,
					Status: eventstream.ToolStatusInProgress, Meta: acpToolNameMeta("Read"),
				}},
				chunk(eventstream.UpdateUserMessage, "guidance-1", guidance),
				{Kind: eventstream.KindSessionUpdate, Update: eventstream.ToolCallUpdate{
					SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "read-1", Status: &completed,
				}},
				chunk(eventstream.UpdateAgentMessage, "after", "after first guidance"),
				chunk(eventstream.UpdateUserMessage, "guidance-2", "second guidance"),
				chunk(eventstream.UpdateAgentMessage, "last", "after second guidance"),
				{Kind: eventstream.KindLifecycle, Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateCompleted}, Final: true},
			}
			for index := range events {
				event := &events[index]
				event.SessionID = "session-1"
				event.Scope, event.ScopeID = eventstream.ScopeSubagent, "task-1"
				event.ParentTool = &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "Spawn"}
				event.Actor = "parent"
				event.TurnID = "same-child-turn"
				event.OccurredAt = time.Unix(100+int64(index), 0)
			}
			_, _ = model.handleTaskStreamBatch(taskStreamBatchMsg{
				sessionID: "session-1", taskID: "task-1", token: 7, events: events, replacement: replacement,
			})
			view.prepareVisibleRender()
			plain := strings.Join(renderedPlainRows(model.subagentOutputRows(view, 100, 40)), "\n")
			previous := -1
			for _, text := range []string{"> parent: initial assignment", "before guidance", "> parent: first guidance", "after first guidance", "> parent: second guidance", "after second guidance"} {
				position := strings.Index(plain, text)
				if position <= previous {
					t.Fatalf("input/output order lost at %q:\n%s", text, plain)
				}
				previous = position
			}
			for _, input := range []string{initial, guidance} {
				if count := strings.Count(strings.Join(strings.Fields(plain), ""), strings.Join(strings.Fields(input), "")); count != 1 {
					t.Fatalf("full input rendered %d times, want once:\n%s", count, plain)
				}
			}
			if strings.Contains(plain, "stale prefix") {
				t.Fatalf("replacement retained old text:\n%s", plain)
			}
			tools := 0
			for _, event := range view.block.Events {
				if event.Kind == SEToolCall && event.CallID == "read-1" {
					tools++
					if !event.Done {
						t.Fatal("input boundary orphaned the existing tool lifecycle")
					}
				}
			}
			if tools != 1 || len(view.turnBlocks) != 1 {
				t.Fatalf("input split tool/Turn identity: tools=%d turns=%d", tools, len(view.turnBlocks))
			}
		})
	}
}
