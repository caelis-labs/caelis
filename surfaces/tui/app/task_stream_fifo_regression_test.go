package tuiapp

import (
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestCommandTaskStreamAppendsFIFOWithoutTextReconciliation(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(300, 0))
	apply := func(envelope eventstream.Envelope) {
		model = applyACPEnvelopeForTest(t, model, envelope)
	}
	apply(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "command-call",
			Title: "RunCommand long job", Kind: eventstream.ToolKindExecute,
			Status: eventstream.ToolStatusInProgress, RawInput: map[string]any{"command": "long job"},
			Content: []eventstream.ToolCallContent{{Type: "terminal", TerminalID: "terminal-1"}},
			Meta:    acpToolNameMeta("RunCommand"),
		},
	})

	running := eventstream.ToolStatusInProgress
	for sequence, delta := range []string{"same line\n", "same line\n", "tail\n"} {
		meta := runningSnapshotTerminalMeta("RunCommand", "command-task", "terminal-1", delta, "append")
		meta = testMeta.WithRuntimeSection(meta, testMeta.RuntimeTask, map[string]any{"handle": "command-1"})
		apply(eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "terminal-1", Scope: eventstream.ScopeMain,
			ParentTool: &eventstream.ParentToolRelation{ToolCallID: "command-call", ToolName: "RunCommand"},
			Delivery:   &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
			Position: &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{
				Generation: "task-stream-1", Sequence: uint64(sequence + 1),
			}},
			Update: eventstream.ToolCallUpdate{
				SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "command-call", Status: &running, Meta: meta,
			},
		})
	}

	command := requireMainACPTurnBlockForTest(t, model).Events[0]
	if got, want := command.Output, "same line\nsame line\ntail\n"; got != want {
		t.Fatalf("FIFO command output = %q, want every delivered delta %q", got, want)
	}
}

func TestStandardACPTerminalOnlyCollectionClearsPriorContent(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(302, 0))
	apply := func(update eventstream.Update) {
		model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1",
			Scope: eventstream.ScopeMain, Update: update,
		})
	}
	apply(eventstream.ToolCall{
		SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "standard-command",
		Title: "RunCommand standard", Kind: eventstream.ToolKindExecute,
		Status: eventstream.ToolStatusInProgress, RawInput: map[string]any{"command": "standard"},
		Content: []eventstream.ToolCallContent{{
			Type: "content", Content: eventstream.TextContent{Type: "text", Text: "stale snapshot"},
		}},
		Meta: acpToolNameMeta("RunCommand"),
	})
	running := eventstream.ToolStatusInProgress
	apply(eventstream.ToolCallUpdate{
		SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "standard-command", Status: &running,
		Content: []eventstream.ToolCallContent{{Type: "terminal", TerminalID: "terminal-1"}},
		Meta:    testMeta.WithTerminalInfo(acpToolNameMeta("RunCommand"), "terminal-1"),
	})

	command := requireMainACPTurnBlockForTest(t, model).Events[0]
	if command.Output != "" || !command.OutputCollection {
		t.Fatalf("standard ACP terminal-only replacement = %#v, want prior content cleared", command)
	}
}
