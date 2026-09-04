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

func TestACPChildTerminalProjectorDeduplicatesLifecyclePerTurn(t *testing.T) {
	t.Parallel()

	projector := newACPChildTerminalProjector()
	parent := &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "Spawn"}
	progress := eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeSubagent,
		ScopeID: "task-1", TurnID: "turn-1", ParentTool: parent,
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "message-1",
			Content: eventstream.TextContent{Type: "text", Text: "stream final"},
		},
	}
	lifecycle := func(turnID, state string) eventstream.Envelope {
		return eventstream.Envelope{
			Kind: eventstream.KindLifecycle, SessionID: "session-1", Scope: eventstream.ScopeSubagent,
			ScopeID: "task-1", TurnID: turnID, ParentTool: parent, Final: true,
			Lifecycle: &eventstream.Lifecycle{State: state},
		}
	}

	projector.project(progress, "")
	result, handled := projector.projectLifecycle(lifecycle("turn-1", eventstream.LifecycleStateCompleted), "")
	if !handled {
		t.Fatal("turn-1 lifecycle was not handled")
	}
	assertACPChildFinalResult(t, result, eventstream.ToolStatusCompleted, "stream final")
	if duplicate, handled := projector.projectLifecycle(lifecycle("turn-1", eventstream.LifecycleStateCompleted), ""); !handled || duplicate.Update != nil {
		t.Fatalf("duplicate lifecycle = %#v handled=%v, want suppressed", duplicate, handled)
	}

	empty, handled := projector.projectLifecycle(lifecycle("turn-2", eventstream.LifecycleStateFailed), "")
	if !handled {
		t.Fatal("empty failed lifecycle was not handled")
	}
	assertACPChildFinalResult(t, empty, eventstream.ToolStatusFailed, "")
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

func TestACPChildTerminalProjectorClosesEmptyFailedLifecycle(t *testing.T) {
	t.Parallel()

	projector := newACPChildTerminalProjector()
	result, handled := projector.projectLifecycle(eventstream.Envelope{
		Kind: eventstream.KindLifecycle, SessionID: "session-1", Scope: eventstream.ScopeSubagent,
		TurnID:     "task-1:1",
		ParentTool: &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "Spawn"},
		Final:      true,
		Lifecycle:  &eventstream.Lifecycle{State: eventstream.LifecycleStateFailed},
	}, "")
	if !handled {
		t.Fatal("empty failed lifecycle was not handled")
	}
	assertACPChildFinalResult(t, result, eventstream.ToolStatusFailed, "")
	if duplicate, handled := projector.projectLifecycle(eventstream.Envelope{
		Kind: eventstream.KindLifecycle, SessionID: "session-1", Scope: eventstream.ScopeSubagent,
		TurnID:     "task-1:1",
		ParentTool: &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "Spawn"},
		Final:      true,
		Lifecycle:  &eventstream.Lifecycle{State: eventstream.LifecycleStateFailed},
	}, ""); !handled || duplicate.Update != nil {
		t.Fatalf("duplicate empty failed lifecycle = %#v handled=%v, want suppressed", duplicate, handled)
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
