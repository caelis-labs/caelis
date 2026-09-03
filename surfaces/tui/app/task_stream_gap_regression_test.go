package tuiapp

import (
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestRunningCommandObservationSeedsTaskStreamOwnerWithoutGap(t *testing.T) {
	t.Parallel()

	const (
		prefix = "command prefix\n"
		middle = "command middle\n"
	)
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(300, 0))
	apply := func(update eventstream.Update) {
		model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1",
			Scope: eventstream.ScopeMain, Update: update,
		})
	}
	applyStream := func(output string, cursor int64, sequence uint64) {
		model = applyCommandTaskStreamFrameForGapTest(t, model, output, cursor, sequence)
	}

	applyCommandStartForGapTest(apply)
	running := eventstream.ToolStatusInProgress
	runMeta := commandObservationMetaForGapTest(prefix, 0, int64(len([]byte(prefix))))
	apply(eventstream.ToolCallUpdate{
		SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "command-call", Status: &running,
		RawOutput: map[string]any{"handle": "command-1", "state": "running", "latest_output": prefix},
		Meta:      runMeta,
	})

	command := requireMainACPTurnBlockForTest(t, model).Events[0]
	if command.Output != prefix || command.OutputGapBefore || command.OutputCursor != int64(len([]byte(prefix))) {
		t.Fatalf("running observation owner = %#v, want exact prefix without a gap", command)
	}
	// A running task-backed observation without new content is a standard sparse
	// patch. It must preserve the already represented bytes and cursor.
	anchorMeta := commandObservationMetaForGapTest("", 0, int64(len([]byte(prefix))))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1",
		Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "command-call", Status: &running,
			Meta: anchorMeta,
		},
	})
	command = requireMainACPTurnBlockForTest(t, model).Events[0]
	if command.Output != prefix || command.OutputGapBefore || command.OutputCursor != int64(len([]byte(prefix))) {
		t.Fatalf("sparse observation owner = %#v, want exact prefix and cursor preserved", command)
	}

	// The initial TaskStream catch-up may race before or after the running tool
	// result. Its absolute range makes the already seeded prefix a duplicate.
	applyStream(prefix, int64(len([]byte(prefix))), 1)
	applyStream(middle, int64(len([]byte(prefix+middle))), 2)
	command = requireMainACPTurnBlockForTest(t, model).Events[0]
	if command.Output != prefix+middle || command.OutputGapBefore {
		t.Fatalf("streamed command owner = %#v, want ordered exact output without a gap", command)
	}

	completed := eventstream.ToolStatusCompleted
	waitInput := map[string]any{"action": "wait", "handle": "command-1"}
	waitMeta := testMeta.WithRuntimeSection(acpToolNameMeta("Task"), testMeta.RuntimeTool, map[string]any{
		testMeta.RuntimeToolName: "Task", "action": "wait", "target_handle": "command-1", "target_kind": "command",
	})
	apply(eventstream.ToolCall{
		SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "task-wait",
		Title: "Task wait command-1", Kind: eventstream.ToolKindOther,
		Status: eventstream.ToolStatusInProgress, RawInput: waitInput, Meta: waitMeta,
	})
	waitMeta = testMeta.WithRuntimeSection(waitMeta, testMeta.RuntimeTask, map[string]any{
		"task_id": "command-task", "handle": "command-1", "running": false, "state": "completed",
		testMeta.RuntimeTaskTerminalID: "terminal-1",
		testMeta.RuntimeOutputStart:    int64(0),
		testMeta.RuntimeOutputCursor:   int64(len([]byte(prefix + middle))),
		testMeta.RuntimeOutputDelta:    prefix + middle,
	})
	waitMeta = testMeta.WithTerminalInfo(waitMeta, "terminal-1")
	apply(eventstream.ToolCallUpdate{
		SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "task-wait", Status: &completed,
		RawInput: waitInput,
		RawOutput: map[string]any{
			"action": "wait", "handle": "command-1", "target_kind": "command", "state": "completed",
		},
		Meta: waitMeta,
	})
	command = requireMainACPTurnBlockForTest(t, model).Events[0]
	if command.Output != prefix+middle || command.OutputGapBefore {
		t.Fatalf("completed observation owner = %#v, want complete exact output without a gap", command)
	}
	for _, event := range requireMainACPTurnBlockForTest(t, model).Events {
		if event.CallID == "task-wait" {
			t.Fatalf("task wait lifecycle created a separate output owner: %#v", event)
		}
	}
	model.syncViewportContent()
	if plain := strings.Join(model.viewportPlainLines, "\n"); strings.Contains(plain, "unavailable") {
		t.Fatalf("TUI exposed internal stream continuity state:\n%s", plain)
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

func TestLateExactTaskStreamCatchupRepairsObservationRaceSilently(t *testing.T) {
	t.Parallel()

	const (
		earlier   = "earlier output\n"
		observed  = "first visible output\n"
		continued = "continued output\n"
	)
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(301, 0))
	apply := func(update eventstream.Update) {
		model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1",
			Scope: eventstream.ScopeMain, Update: update,
		})
	}
	applyStream := func(output string, cursor int64, sequence uint64) {
		model = applyCommandTaskStreamFrameForGapTest(t, model, output, cursor, sequence)
	}

	applyCommandStartForGapTest(apply)
	// Simulate a recovery/compatibility producer that has a compact running
	// snapshot but no exact output_delta. The Surface must wait for TaskStream.
	running := eventstream.ToolStatusInProgress
	runMeta := commandObservationMetaForGapTest("", 0, int64(len([]byte(earlier))))
	apply(eventstream.ToolCallUpdate{
		SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "command-call", Status: &running,
		RawOutput: map[string]any{"handle": "command-1", "state": "running", "latest_output": earlier},
		Meta:      runMeta,
	})

	readInput := map[string]any{"action": "read", "handle": "command-1"}
	readMeta := testMeta.WithRuntimeSection(acpToolNameMeta("Task"), testMeta.RuntimeTool, map[string]any{
		testMeta.RuntimeToolName: "Task", "action": "read", "target_handle": "command-1", "target_kind": "command",
	})
	readMeta = testMeta.WithRuntimeSection(readMeta, testMeta.RuntimeTask, map[string]any{
		"task_id": "command-task", "handle": "command-1", "running": true, "state": "running",
		testMeta.RuntimeTaskTerminalID: "terminal-1",
		testMeta.RuntimeOutputStart:    int64(len([]byte(earlier))),
		testMeta.RuntimeOutputCursor:   int64(len([]byte(earlier + observed))),
		testMeta.RuntimeOutputDelta:    observed,
	})
	readMeta = testMeta.WithTerminalInfo(readMeta, "terminal-1")
	apply(eventstream.ToolCall{
		SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "task-read",
		Title: "Task read command-1", Kind: eventstream.ToolKindOther,
		Status: eventstream.ToolStatusInProgress, RawInput: readInput, Meta: readMeta,
	})
	completed := eventstream.ToolStatusCompleted
	apply(eventstream.ToolCallUpdate{
		SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "task-read", Status: &completed,
		RawInput: readInput,
		RawOutput: map[string]any{
			"action": "read", "handle": "command-1", "target_kind": "command", "state": "running",
		},
		Meta: readMeta,
	})

	command := requireMainACPTurnBlockForTest(t, model).Events[0]
	if command.Output != observed || !command.OutputGapBefore {
		t.Fatalf("observation race owner = %#v, want available suffix with internal gap state", command)
	}
	model.syncViewportContent()
	plain := strings.Join(model.viewportPlainLines, "\n")
	if !strings.Contains(plain, strings.TrimSpace(observed)) || strings.Contains(plain, "unavailable") {
		t.Fatalf("TUI did not silently render the available suffix:\n%s", plain)
	}

	applyStream(earlier+observed, int64(len([]byte(earlier+observed))), 1)
	command = requireMainACPTurnBlockForTest(t, model).Events[0]
	if command.Output != earlier+observed || command.OutputGapBefore {
		t.Fatalf("late exact catch-up owner = %#v, want complete repaired output", command)
	}

	applyStream(continued, int64(len([]byte(earlier+observed+continued))), 2)
	command = requireMainACPTurnBlockForTest(t, model).Events[0]
	if command.Output != earlier+observed+continued || command.OutputGapBefore {
		t.Fatalf("continued stream owner = %#v, want repaired progressive output", command)
	}
}

func applyCommandStartForGapTest(apply func(eventstream.Update)) {
	apply(eventstream.ToolCall{
		SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "command-call",
		Title: "RunCommand long job", Kind: eventstream.ToolKindExecute,
		Status:   eventstream.ToolStatusInProgress,
		RawInput: map[string]any{"command": "long job"},
		Content:  []eventstream.ToolCallContent{{Type: "terminal", TerminalID: "terminal-1"}},
		Meta:     acpToolNameMeta("RunCommand"),
	})
}

func commandObservationMetaForGapTest(output string, start int64, cursor int64) map[string]any {
	meta := testMeta.WithRuntimeSection(acpToolNameMeta("RunCommand"), testMeta.RuntimeTask, map[string]any{
		"task_id": "command-task", "handle": "command-1", "running": true, "state": "running",
		testMeta.RuntimeTaskTerminalID: "terminal-1",
		testMeta.RuntimeOutputStart:    start,
		testMeta.RuntimeOutputCursor:   cursor,
	})
	if output != "" {
		meta = testMeta.WithRuntimeSection(meta, testMeta.RuntimeTask, map[string]any{
			testMeta.RuntimeOutputDelta: output,
		})
		meta = testMeta.WithTerminalOutput(meta, "terminal-1", output)
	}
	meta = testMeta.WithRuntimeSection(meta, "binding", map[string]any{"task_result": true})
	return testMeta.WithTerminalInfo(meta, "terminal-1")
}

func applyCommandTaskStreamFrameForGapTest(
	t *testing.T,
	model *Model,
	output string,
	cursor int64,
	sequence uint64,
) *Model {
	t.Helper()
	meta := runningSnapshotTerminalMeta("RunCommand", "command-task", "terminal-1", output, "append")
	meta = testMeta.WithRuntimeSection(meta, testMeta.RuntimeTask, map[string]any{"handle": "command-1"})
	meta = testMeta.WithRuntimeSection(meta, testMeta.RuntimeStream, map[string]any{
		testMeta.RuntimeStreamMode: "append", testMeta.RuntimeOutputCursor: cursor,
	})
	running := eventstream.ToolStatusInProgress
	return applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "terminal-1",
		Scope:      eventstream.ScopeMain,
		ParentTool: &eventstream.ParentToolRelation{ToolCallID: "command-call", ToolName: "RunCommand"},
		Delivery:   &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Position: &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{
			Generation: "task-stream-1", Sequence: sequence,
		}},
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "command-call",
			Status: &running, Meta: meta,
		},
	})
}
