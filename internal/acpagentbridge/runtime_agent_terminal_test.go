package acpagentbridge

import (
	"fmt"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpmeta"
)

func TestACPChildTerminalProjectorEmitsOnlyFinalResponseContent(t *testing.T) {
	t.Parallel()

	projector := newACPChildTerminalProjector()
	parent := &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "Spawn"}
	child := func(update eventstream.Update) eventstream.Envelope {
		return eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeSubagent,
			ScopeID: "task-1", TurnID: "child-turn-1", ParentTool: parent, Update: update,
		}
	}
	updates := []eventstream.Update{
		eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "message-progress",
			Content: eventstream.TextContent{Type: "text", Text: "intermediate narrative"},
		},
		eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentThought,
			Content:       eventstream.TextContent{Type: "text", Text: "private thought"},
		},
		eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "child-tool-1",
			Title: "child tool", Kind: eventstream.ToolKindExecute, Status: eventstream.ToolStatusInProgress,
		},
		eventstream.PlanUpdate{
			SessionUpdate: eventstream.UpdatePlan,
			Entries:       []eventstream.PlanEntry{{Content: "private plan", Status: "in_progress"}},
		},
		eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "message-final",
			Content: eventstream.TextContent{Type: "text", Text: "final "},
		},
		eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "message-final",
			Content: eventstream.TextContent{Type: "text", Text: "answer"},
		},
	}
	for _, update := range updates {
		notification, handled := projector.project(child(update), "")
		if !handled || notification.Update != nil {
			t.Fatalf("child update %T projection = %#v handled=%v, want consumed without output", update, notification, handled)
		}
	}
	if notification, handled := projector.projectNotice(eventstream.Envelope{
		Kind: eventstream.KindNotice, SessionID: "session-1", Scope: eventstream.ScopeSubagent,
		TurnID: "child-turn-1", ParentTool: parent, Notice: "private notice",
	}, ""); !handled || notification.Update != nil {
		t.Fatalf("child notice projection = %#v handled=%v, want consumed without output", notification, handled)
	}

	notification, handled := projector.projectLifecycle(eventstream.Envelope{
		Kind: eventstream.KindLifecycle, SessionID: "session-1", Scope: eventstream.ScopeSubagent,
		ScopeID: "task-1", TurnID: "child-turn-1", ParentTool: parent, Final: true,
		Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateCompleted},
	}, "")
	if !handled {
		t.Fatal("child lifecycle was not handled")
	}
	assertACPChildFinalResult(t, notification, eventstream.ToolStatusCompleted, "final answer")
}

func TestACPChildTerminalProjectorGatesEachTurnAndParentIndependently(t *testing.T) {
	t.Parallel()

	projector := newACPChildTerminalProjector()
	progress := func(parentCallID, turnID, text string) eventstream.Envelope {
		return eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeSubagent,
			ScopeID: "task-1", TurnID: turnID,
			ParentTool: &eventstream.ParentToolRelation{ToolCallID: parentCallID, ToolName: "Spawn"},
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage, MessageID: turnID,
				Content: eventstream.TextContent{Type: "text", Text: text},
			},
		}
	}
	lifecycle := func(parentCallID, turnID, state, reason string) eventstream.Envelope {
		return eventstream.Envelope{
			Kind: eventstream.KindLifecycle, SessionID: "session-1", Scope: eventstream.ScopeSubagent,
			ScopeID: "task-1", TurnID: turnID, Final: true,
			ParentTool: &eventstream.ParentToolRelation{ToolCallID: parentCallID, ToolName: "Spawn"},
			Lifecycle:  &eventstream.Lifecycle{State: state, Reason: reason},
		}
	}

	if notification, handled := projector.project(progress("spawn-1", "turn-1", "first"), ""); !handled || notification.Update != nil {
		t.Fatalf("first progress = %#v handled=%v", notification, handled)
	}
	first, handled := projector.projectLifecycle(lifecycle("spawn-1", "turn-1", eventstream.LifecycleStateCompleted, ""), "")
	if !handled {
		t.Fatal("first lifecycle was not handled")
	}
	assertACPChildFinalResult(t, first, eventstream.ToolStatusCompleted, "first")
	if duplicate, handled := projector.projectLifecycle(lifecycle("spawn-1", "turn-1", eventstream.LifecycleStateCompleted, ""), ""); !handled || duplicate.Update != nil {
		t.Fatalf("duplicate lifecycle = %#v handled=%v, want suppressed", duplicate, handled)
	}
	if late, handled := projector.project(progress("spawn-1", "turn-1", "late"), ""); !handled || late.Update != nil {
		t.Fatalf("late same-turn update = %#v handled=%v, want suppressed", late, handled)
	}

	projector.project(progress("spawn-1", "turn-2", "second"), "")
	second, handled := projector.projectLifecycle(lifecycle("spawn-1", "turn-2", eventstream.LifecycleStateCompleted, ""), "")
	if !handled {
		t.Fatal("second child Turn lifecycle was not handled")
	}
	assertACPChildFinalResult(t, second, eventstream.ToolStatusCompleted, "second")

	for index, test := range []struct {
		state  string
		reason string
	}{
		{state: eventstream.LifecycleStateFailed, reason: "child failed"},
		{state: eventstream.LifecycleStateInterrupted, reason: "child interrupted"},
		{state: eventstream.LifecycleStateCancelled, reason: "child cancelled by parent"},
	} {
		parentCallID := fmt.Sprintf("spawn-%d", index+2)
		projector.project(progress(parentCallID, "turn-a", "intermediate narrative"), "")
		failed, handled := projector.projectLifecycle(lifecycle(parentCallID, "turn-a", test.state, test.reason), "")
		if !handled {
			t.Fatalf("independent %s child lifecycle was not handled", test.state)
		}
		assertACPChildFinalResult(t, failed, eventstream.ToolStatusFailed, test.reason)
	}
}

func TestACPChildTerminalProjectorDeduplicatesLifecycleAndFallbackPerTurn(t *testing.T) {
	t.Parallel()

	projector := newACPChildTerminalProjector()
	parent := &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "Spawn"}
	progress := func(turnID, messageID, text string) eventstream.Envelope {
		return eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeSubagent,
			ScopeID: "task-1", TurnID: turnID, ParentTool: parent,
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage, MessageID: messageID,
				Content: eventstream.TextContent{Type: "text", Text: text},
			},
		}
	}
	lifecycle := func(turnID string) eventstream.Envelope {
		return eventstream.Envelope{
			Kind: eventstream.KindLifecycle, SessionID: "session-1", Scope: eventstream.ScopeSubagent,
			ScopeID: "task-1", TurnID: turnID, ParentTool: parent, Final: true,
			Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateCompleted},
		}
	}
	fallback := func(turnID, text string) eventstream.Envelope {
		completed := eventstream.ToolStatusCompleted
		return eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain, Final: true,
			Update: eventstream.ToolCallUpdate{
				SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "wait-1", Status: &completed,
				RawInput: map[string]any{"action": "wait", "handle": "orbit"},
				RawOutput: map[string]any{
					"action": "wait",
					"tasks": []any{map[string]any{
						"handle": "orbit", "parent_call": "spawn-1", "parent_tool": "Spawn",
						"state": "completed", "target_kind": "subagent", "turn_id": turnID,
						"final_message": text,
					}},
				},
			},
		}
	}

	projector.project(progress("turn-1", "message-1", "stream final"), "")
	streamWinner, handled := projector.projectLifecycle(lifecycle("turn-1"), "")
	if !handled {
		t.Fatal("turn-1 lifecycle was not handled")
	}
	assertACPChildFinalResult(t, streamWinner, eventstream.ToolStatusCompleted, "stream final")
	if duplicate := projector.projectObservedParentCloses(fallback("turn-1", "fallback duplicate"), ""); len(duplicate) != 0 {
		t.Fatalf("turn-1 fallback after lifecycle = %#v, want suppressed", duplicate)
	}

	projector.project(progress("turn-2", "message-2", "uncommitted stream final"), "")
	if projector.parentOpen("session-1", "spawn-1", "turn-1") {
		t.Fatal("closed turn-1 was reported open after turn-2 started")
	}
	if !projector.parentOpen("session-1", "spawn-1", "turn-2") {
		t.Fatal("open turn-2 was reported closed by stale turn-1 fallback")
	}
	fallbackWinner := projector.projectObservedParentCloses(fallback("turn-2", "fallback final"), "")
	if len(fallbackWinner) != 1 {
		t.Fatalf("turn-2 fallback = %#v, want one terminal result", fallbackWinner)
	}
	assertACPChildFinalResult(t, fallbackWinner[0], eventstream.ToolStatusCompleted, "fallback final")
	if duplicate, handled := projector.projectLifecycle(lifecycle("turn-2"), ""); !handled || duplicate.Update != nil {
		t.Fatalf("turn-2 lifecycle after fallback = %#v handled=%v, want suppressed", duplicate, handled)
	}

	turnThreeWinner := projector.projectObservedParentCloses(fallback("turn-3", "third final"), "")
	if len(turnThreeWinner) != 1 {
		t.Fatalf("turn-3 fallback = %#v, want one terminal result", turnThreeWinner)
	}
	assertACPChildFinalResult(t, turnThreeWinner[0], eventstream.ToolStatusCompleted, "third final")
	if duplicate, handled := projector.projectLifecycle(lifecycle("turn-3"), ""); !handled || duplicate.Update != nil {
		t.Fatalf("turn-3 lifecycle after fallback-first = %#v handled=%v, want suppressed", duplicate, handled)
	}

	emptyLifecycle, handled := projector.projectLifecycle(lifecycle("turn-4"), "")
	if !handled || emptyLifecycle.Update != nil {
		t.Fatalf("empty turn-4 lifecycle = %#v handled=%v, want consumed without sealing an empty result", emptyLifecycle, handled)
	}
	if !projector.parentOpen("session-1", "spawn-1", "turn-4") {
		t.Fatal("empty turn-4 lifecycle closed the fallback gate")
	}
	turnFourWinner := projector.projectObservedParentCloses(fallback("turn-4", "recovered final"), "")
	if len(turnFourWinner) != 1 {
		t.Fatalf("turn-4 fallback = %#v, want recovered terminal result", turnFourWinner)
	}
	assertACPChildFinalResult(t, turnFourWinner[0], eventstream.ToolStatusCompleted, "recovered final")

	failedLifecycle := lifecycle("turn-5")
	failedLifecycle.Lifecycle.State = eventstream.LifecycleStateFailed
	failedLifecycleResult, handled := projector.projectLifecycle(failedLifecycle, "")
	if !handled || failedLifecycleResult.Update != nil {
		t.Fatalf("empty failed turn-5 lifecycle = %#v handled=%v, want consumed without sealing an empty result", failedLifecycleResult, handled)
	}
	if !projector.parentOpen("session-1", "spawn-1", "turn-5") {
		t.Fatal("empty failed turn-5 lifecycle closed the fallback gate")
	}
	failedStatus := eventstream.ToolStatusCompleted
	failedFallback := eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain, Final: true,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "wait-2", Status: &failedStatus,
			RawInput: map[string]any{"action": "wait", "handle": "orbit"},
			RawOutput: map[string]any{
				"action": "wait",
				"tasks": []any{map[string]any{
					"handle": "orbit", "parent_call": "spawn-1", "parent_tool": "Spawn",
					"state": "failed", "target_kind": "subagent", "turn_id": "turn-5",
					"error": "canonical child failure",
				}},
			},
		},
	}
	turnFiveWinner := projector.projectObservedParentCloses(failedFallback, "")
	if len(turnFiveWinner) != 1 {
		t.Fatalf("turn-5 failed fallback = %#v, want recovered terminal result", turnFiveWinner)
	}
	assertACPChildFinalResult(t, turnFiveWinner[0], eventstream.ToolStatusFailed, "canonical child failure")
	if duplicate, handled := projector.projectLifecycle(failedLifecycle, ""); !handled || duplicate.Update != nil {
		t.Fatalf("turn-5 lifecycle after fallback = %#v handled=%v, want suppressed", duplicate, handled)
	}
}

func TestACPChildTerminalProjectorCompactsSequentialClosedTurns(t *testing.T) {
	t.Parallel()

	const turnCount = 300
	projector := newACPChildTerminalProjector()
	parent := &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "Spawn"}
	for sequence := 1; sequence <= turnCount; sequence++ {
		turnID := fmt.Sprintf("task-1:%d", sequence)
		projector.project(eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeSubagent,
			TurnID: turnID, ParentTool: parent,
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage, MessageID: turnID,
				Content: eventstream.TextContent{Type: "text", Text: "final response"},
			},
		}, "")
		result, handled := projector.projectLifecycle(eventstream.Envelope{
			Kind: eventstream.KindLifecycle, SessionID: "session-1", Scope: eventstream.ScopeSubagent,
			TurnID: turnID, ParentTool: parent, Final: true,
			Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateCompleted},
		}, "")
		if !handled || result.Update == nil {
			t.Fatalf("terminal projection %d = %#v handled=%v, want one result", sequence, result, handled)
		}
	}

	for _, sequence := range []int{1, turnCount - 1, turnCount} {
		duplicate, handled := projector.projectLifecycle(eventstream.Envelope{
			Kind: eventstream.KindLifecycle, SessionID: "session-1", Scope: eventstream.ScopeSubagent,
			TurnID: fmt.Sprintf("task-1:%d", sequence), ParentTool: parent, Final: true,
			Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateCompleted},
		}, "")
		if !handled || duplicate.Update != nil {
			t.Fatalf("late duplicate for sequence %d = %#v handled=%v, want suppressed", sequence, duplicate, handled)
		}
	}
	completed := eventstream.ToolStatusCompleted
	oldFallback := eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain, Final: true,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "read-old", Status: &completed,
			RawInput: map[string]any{"action": "read", "handle": "orbit"},
			RawOutput: map[string]any{
				"action": "read",
				"tasks": []any{map[string]any{
					"handle": "orbit", "parent_call": "spawn-1", "parent_tool": "Spawn",
					"state": "completed", "target_kind": "subagent", "turn_id": "task-1:1",
					"final_message": "late duplicate fallback",
				}},
			},
		},
	}
	if duplicate := projector.projectObservedParentCloses(oldFallback, ""); len(duplicate) != 0 {
		t.Fatalf("old Task fallback after compaction = %#v, want suppressed", duplicate)
	}

	projector.mu.Lock()
	defer projector.mu.Unlock()
	if got := len(projector.turns); got != 0 {
		t.Fatalf("open turn states = %d, want none after terminal projection", got)
	}
	if got := len(projector.closed); got != 0 {
		t.Fatalf("unsequenced tombstones = %d, want none for physical Task Turn IDs", got)
	}
	if got := len(projector.closedThrough); got != 1 {
		t.Fatalf("closed Turn series = %d, want one compact watermark", got)
	}
	if got := len(projector.current); got != 1 {
		t.Fatalf("current parents = %d, want one", got)
	}
}

func TestACPChildTerminalProjectorKeepsEarlierEmptySequentialTurnOpenAfterLaterClose(t *testing.T) {
	t.Parallel()

	projector := newACPChildTerminalProjector()
	parent := &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "Spawn"}
	lifecycle := func(turnID, state string) eventstream.Envelope {
		return eventstream.Envelope{
			Kind: eventstream.KindLifecycle, SessionID: "session-1", Scope: eventstream.ScopeSubagent,
			TurnID: turnID, ParentTool: parent, Final: true,
			Lifecycle: &eventstream.Lifecycle{State: state},
		}
	}

	first, handled := projector.projectLifecycle(lifecycle("task-1:1", eventstream.LifecycleStateFailed), "")
	if !handled || first.Update != nil {
		t.Fatalf("empty first lifecycle = %#v handled=%v, want no result before fallback", first, handled)
	}
	projector.project(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeSubagent,
		TurnID: "task-1:2", ParentTool: parent,
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "turn-2",
			Content: eventstream.TextContent{Type: "text", Text: "second final"},
		},
	}, "")
	second, handled := projector.projectLifecycle(lifecycle("task-1:2", eventstream.LifecycleStateCompleted), "")
	if !handled {
		t.Fatal("second lifecycle was not handled")
	}
	assertACPChildFinalResult(t, second, eventstream.ToolStatusCompleted, "second final")
	if !projector.parentOpen("session-1", "spawn-1", "task-1:1") {
		t.Fatal("later sequential close sealed the earlier empty lifecycle")
	}

	completed := eventstream.ToolStatusCompleted
	fallback := eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain, Final: true,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "wait-1", Status: &completed,
			RawInput: map[string]any{"action": "wait", "handle": "orbit"},
			RawOutput: map[string]any{
				"action": "wait",
				"tasks": []any{map[string]any{
					"handle": "orbit", "parent_call": "spawn-1", "parent_tool": "Spawn",
					"state": "failed", "target_kind": "subagent", "turn_id": "task-1:1",
					"error": "first canonical failure",
				}},
			},
		},
	}
	firstFallback := projector.projectObservedParentCloses(fallback, "")
	if len(firstFallback) != 1 {
		t.Fatalf("earlier fallback after later close = %#v, want one terminal result", firstFallback)
	}
	assertACPChildFinalResult(t, firstFallback[0], eventstream.ToolStatusFailed, "first canonical failure")
	if duplicate := projector.projectObservedParentCloses(fallback, ""); len(duplicate) != 0 {
		t.Fatalf("repeated earlier fallback = %#v, want suppressed", duplicate)
	}

	projector.mu.Lock()
	defer projector.mu.Unlock()
	if got := len(projector.closed); got != 0 {
		t.Fatalf("gap tombstones = %d, want compacted after earlier fallback closed", got)
	}
	if got := len(projector.closedThrough); got != 1 {
		t.Fatalf("closed Turn series = %d, want one compact watermark", got)
	}
}

func TestACPChildTerminalProjectorDropsEmptyTerminalAccumulatorWhileWaitingForFallback(t *testing.T) {
	t.Parallel()

	projector := newACPChildTerminalProjector()
	parent := &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "Spawn"}
	for sequence := 1; sequence <= 300; sequence++ {
		turnID := fmt.Sprintf("task-1:%d", sequence)
		projector.project(eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeSubagent,
			TurnID: turnID, ParentTool: parent,
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage, MessageID: turnID,
				Content: eventstream.TextContent{Type: "text", Text: "stale success"},
			},
		}, "")
		result, handled := projector.projectLifecycle(eventstream.Envelope{
			Kind: eventstream.KindLifecycle, SessionID: "session-1", Scope: eventstream.ScopeSubagent,
			TurnID: turnID, ParentTool: parent, Final: true,
			Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateFailed},
		}, "")
		if !handled || result.Update != nil {
			t.Fatalf("empty failed lifecycle %d = %#v handled=%v, want no result before fallback", sequence, result, handled)
		}
	}

	projector.mu.Lock()
	defer projector.mu.Unlock()
	if got := len(projector.turns); got != 0 {
		t.Fatalf("pending terminal states = %d, want no retained accumulators", got)
	}
	if got := len(projector.closedThrough); got != 0 {
		t.Fatalf("closed Turn series = %d, want empty lifecycles to leave the fallback gate open", got)
	}
}

func TestChildTerminalResultTextDoesNotReuseStaleSuccessForFailure(t *testing.T) {
	t.Parallel()

	rawOutput := map[string]any{
		"state":         "cancelled",
		"final_message": "stale completed final",
	}
	if got := childTerminalResultText(eventstream.ToolStatusFailed, rawOutput); got != "" {
		t.Fatalf("failed child result text = %q, want stale FinalResponse suppressed", got)
	}
	rawOutput["reason"] = "cancelled by parent"
	if got := childTerminalResultText(eventstream.ToolStatusFailed, rawOutput); got != "cancelled by parent" {
		t.Fatalf("failed child result text = %q, want cancellation reason", got)
	}
}

func assertACPChildFinalResult(t *testing.T, notification eventstream.SessionNotification, wantStatus, wantText string) {
	t.Helper()
	update, ok := notification.Update.(eventstream.ToolCallUpdate)
	if !ok || update.Status == nil || *update.Status != wantStatus {
		t.Fatalf("final update = %#v, want status %q", notification, wantStatus)
	}
	if update.Meta != nil {
		t.Fatalf("final meta = %#v, want no terminal compatibility metadata", update.Meta)
	}
	if strings.TrimSpace(wantText) == "" {
		if len(update.Content) != 0 {
			t.Fatalf("final content = %#v, want empty", update.Content)
		}
		return
	}
	if len(update.Content) != 1 || update.Content[0].Type != "content" {
		t.Fatalf("final content = %#v, want one standard content result", update.Content)
	}
	text, ok := update.Content[0].Content.(eventstream.TextContent)
	if !ok || text.Type != "text" || text.Text != wantText {
		t.Fatalf("final content = %#v, want text %q", update.Content, wantText)
	}
}

func TestACPObservedParentClosesFromBatchWaitOnlyTerminalSubagents(t *testing.T) {
	completed := eventstream.ToolStatusCompleted
	envelope := eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain, Final: true,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "wait-call-1",
			Status:        &completed,
			RawInput:      map[string]any{"action": "wait", "handle": "alpha,beta,gamma"},
			RawOutput: map[string]any{
				"action": "wait",
				"tasks": []any{
					map[string]any{
						"handle": "alpha", "parent_call": "spawn-alpha", "parent_tool": "Spawn", "turn_id": "alpha-turn-1",
						"state": "completed", "target_kind": "subagent", "final_message": "alpha done",
					},
					map[string]any{
						"handle": "beta", "parent_call": "spawn-beta", "parent_tool": "Spawn",
						"state": "running", "target_kind": "subagent",
					},
					map[string]any{
						"handle": "gamma", "parent_call": "spawn-gamma", "parent_tool": "Spawn", "turn_id": "gamma-turn-1",
						"state": "failed", "target_kind": "subagent", "final_message": "stale completed final", "error": "gamma failed",
					},
					map[string]any{
						"handle": "command", "parent_call": "command-call", "parent_tool": "RunCommand",
						"state": "completed", "target_kind": "command",
					},
					map[string]any{
						"handle": "alpha", "parent_call": "spawn-alpha", "parent_tool": "Spawn", "turn_id": "alpha-turn-1",
						"state": "completed", "target_kind": "subagent", "final_message": "duplicate",
					},
				},
			},
		},
	}

	observed := acpObservedParentClosesFromEnvelope(envelope)
	if len(observed) != 2 {
		t.Fatalf("observed parent closes = %#v, want completed alpha and failed gamma", observed)
	}
	if observed[0].parentCallID != "spawn-alpha" || displayMapString(observed[0].rawOutput, "final_message") != "alpha done" {
		t.Fatalf("first observed parent close = %#v, want alpha result", observed[0])
	}
	if observed[1].parentCallID != "spawn-gamma" || displayMapString(observed[1].rawOutput, "error") != "gamma failed" {
		t.Fatalf("second observed parent close = %#v, want gamma failure", observed[1])
	}

	projector := newACPChildTerminalProjector()
	closes := projector.projectObservedParentCloses(envelope, "session-fallback")
	if len(closes) != 2 {
		t.Fatalf("projected parent closes = %#v, want two terminal updates", closes)
	}
	wantStatus := []string{eventstream.ToolStatusCompleted, eventstream.ToolStatusFailed}
	wantText := []string{"alpha done", "gamma failed"}
	for index, closeNotification := range closes {
		assertACPChildFinalResult(t, closeNotification, wantStatus[index], wantText[index])
	}
	if duplicate := projector.projectObservedParentCloses(envelope, "session-fallback"); len(duplicate) != 0 {
		t.Fatalf("late duplicate parent closes = %#v, want none", duplicate)
	}
}

func displayMapString(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func TestNormalizeACPStdioTerminalExtensionDoesNotInventTerminalForPlainTool(t *testing.T) {
	notification := normalizeACPStdioTerminalExtension(eventstream.SessionNotification{
		SessionID: "session-1",
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall,
			ToolCallID:    "call-1",
			Title:         "LIST",
			Kind:          eventstream.ToolKindSearch,
			Status:        eventstream.ToolStatusPending,
		},
	})
	call, ok := notification.Update.(eventstream.ToolCall)
	if !ok {
		t.Fatalf("update = %T, want ToolCall", notification.Update)
	}
	if _, ok := acpmeta.ReadTerminalInfo(call.Meta); ok {
		t.Fatalf("meta = %#v, want no terminal_info for plain tool", call.Meta)
	}
	if len(call.Content) != 0 {
		t.Fatalf("content = %#v, want no terminal anchor for plain tool", call.Content)
	}

	status := eventstream.ToolStatusCompleted
	notification = normalizeACPStdioTerminalExtension(eventstream.SessionNotification{
		SessionID: "session-1",
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Status:        &status,
			RawOutput:     map[string]any{"result": "ok"},
		},
	})
	update, ok := notification.Update.(eventstream.ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %T, want ToolCallUpdate", notification.Update)
	}
	if _, ok := acpmeta.ReadTerminalExit(update.Meta); ok {
		t.Fatalf("meta = %#v, want no terminal_exit for plain tool", update.Meta)
	}
}

func TestNormalizeACPStdioTerminalExtensionDoesNotMountFinalOnlySpawn(t *testing.T) {
	meta := acpmeta.WithToolName(nil, "Spawn")
	meta = acpmeta.WithTerminalInfo(meta, "spawn-1")
	notification := normalizeACPStdioTerminalExtension(eventstream.SessionNotification{
		SessionID: "session-1",
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall,
			ToolCallID:    "spawn-1",
			Title:         "Spawn orbit: inspect",
			Kind:          eventstream.ToolKindExecute,
			Status:        eventstream.ToolStatusPending,
			Content:       []eventstream.ToolCallContent{{Type: "terminal", TerminalID: "spawn-1"}},
			Meta:          meta,
		},
	})
	call, ok := notification.Update.(eventstream.ToolCall)
	if !ok {
		t.Fatalf("update = %T, want ToolCall", notification.Update)
	}
	if len(call.Content) != 0 {
		t.Fatalf("Spawn content = %#v, want no terminal mount", call.Content)
	}
	if _, ok := acpmeta.ReadTerminalInfo(call.Meta); ok {
		t.Fatalf("Spawn meta = %#v, want no terminal_info", call.Meta)
	}
	if got := acpmeta.ToolName(call.Meta); got != "Spawn" {
		t.Fatalf("Spawn runtime tool name = %q, want preserved", got)
	}

	status := eventstream.ToolStatusInProgress
	for _, test := range []struct {
		name        string
		content     []eventstream.ToolCallContent
		wantPresent bool
	}{
		{name: "filtered terminal mount", content: []eventstream.ToolCallContent{{Type: "terminal", TerminalID: "spawn-1"}}},
		{name: "explicit empty patch", content: []eventstream.ToolCallContent{}, wantPresent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			updateMeta := acpmeta.WithToolName(nil, "Spawn")
			updateMeta = acpmeta.WithTerminalInfo(updateMeta, "spawn-1")
			normalized := normalizeACPStdioTerminalExtension(eventstream.SessionNotification{
				SessionID: "session-1",
				Update: eventstream.ToolCallUpdate{
					SessionUpdate: eventstream.UpdateToolCallInfo,
					ToolCallID:    "spawn-1",
					Status:        &status,
					Content:       test.content,
					Meta:          updateMeta,
				},
			})
			update, ok := normalized.Update.(eventstream.ToolCallUpdate)
			if !ok {
				t.Fatalf("update = %T, want ToolCallUpdate", normalized.Update)
			}
			if present := update.Content != nil; present != test.wantPresent {
				t.Fatalf("Spawn update content = %#v, presence = %t, want %t", update.Content, present, test.wantPresent)
			}
		})
	}
}

func TestNormalizeACPStdioTerminalExtensionKeepsAnchorAndMovesOutputToMeta(t *testing.T) {
	status := eventstream.ToolStatusCompleted
	notification := normalizeACPStdioTerminalExtension(eventstream.SessionNotification{
		SessionID: "session-1",
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Status:        &status,
			RawOutput:     map[string]any{"exit_code": 0},
			Content: []eventstream.ToolCallContent{{
				Type:       "terminal",
				TerminalID: "terminal-1",
				Content:    eventstream.TextContent{Type: "text", Text: "line\n"},
			}},
		},
	})
	update, ok := notification.Update.(eventstream.ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %T, want ToolCallUpdate", notification.Update)
	}
	if len(update.Content) != 1 || update.Content[0].Type != "terminal" || update.Content[0].TerminalID != "terminal-1" {
		t.Fatalf("content = %#v, want one terminal anchor", update.Content)
	}
	if update.Content[0].Content != nil {
		t.Fatalf("terminal anchor content = %#v, want empty", update.Content[0].Content)
	}
	if info, ok := acpmeta.ReadTerminalInfo(update.Meta); !ok || info.TerminalID != "terminal-1" {
		t.Fatalf("terminal_info = %#v, want terminal-1", update.Meta)
	}
	if output, ok := acpmeta.ReadTerminalOutput(update.Meta); !ok || output.TerminalID != "terminal-1" || output.Data != "line\n" {
		t.Fatalf("terminal_output = %#v, want line output", update.Meta)
	}
	if exit, ok := acpmeta.ReadTerminalExit(update.Meta); !ok || exit.TerminalID != "terminal-1" {
		t.Fatalf("terminal_exit = %#v, want terminal-1", update.Meta)
	}
}

func TestACPNarrativeFilterForwardsTerminalDeltasVerbatim(t *testing.T) {
	filter := newACPNarrativeFilter(false)
	running := eventstream.ToolStatusInProgress
	for _, text := range []string{"line 1\n", "line 1\nline 2\n"} {
		filtered, ok := filter.FilterNotification(eventstream.SessionNotification{
			SessionID: "session-1",
			Update: eventstream.ToolCallUpdate{
				SessionUpdate: eventstream.UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Status:        &running,
				Meta:          acpmeta.WithTerminalOutput(nil, "terminal-1", text),
			},
		})
		if !ok {
			t.Fatalf("terminal delta %q was suppressed", text)
		}
		update := filtered.Update.(eventstream.ToolCallUpdate)
		output, exists := acpmeta.ReadTerminalOutput(update.Meta)
		if !exists || output.Data != text {
			t.Fatalf("terminal delta = %#v, want exact producer bytes %q", update.Meta, text)
		}
	}
}

func TestACPNarrativeFilterOnlySuppressesUserEcho(t *testing.T) {
	filter := newACPNarrativeFilter(true)
	if _, ok := filter.FilterNotification(eventstream.SessionNotification{
		SessionID: "session-1",
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateUserMessage,
			Content:       eventstream.TextContent{Type: "text", Text: "hello"},
		},
	}); ok {
		t.Fatal("user echo was forwarded")
	}
	agent, ok := filter.FilterNotification(eventstream.SessionNotification{
		SessionID: "session-1",
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentMessage,
			Content:       eventstream.TextContent{Type: "text", Text: "hello"},
		},
	})
	if !ok || acpTextContentText(agent.Update.(eventstream.ContentChunk).Content) != "hello" {
		t.Fatalf("agent delta = %#v, want unchanged", agent)
	}
}
