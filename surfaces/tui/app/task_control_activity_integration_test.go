package tuiapp

import (
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestTaskWaitAndCancelUseActivityHintWithoutTranscriptRows(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Now())
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "spawn-1",
			Title: "Spawn orbit: inspect", Kind: eventstream.ToolKindExecute, Status: eventstream.ToolStatusInProgress,
			RawInput: map[string]any{"agent": "orbit", "prompt": "inspect"}, Meta: acpToolNameMeta("Spawn"),
		},
	})
	running := eventstream.ToolStatusInProgress
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "spawn-1", Status: &running,
			RawOutput: map[string]any{"handle": "command-48", "state": "running"}, Meta: acpToolNameMeta("Spawn"),
		},
	})
	taskInput := map[string]any{
		"action": "wait",
		"handle": "command-48",
	}
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "task-wait-1",
			Title: "Task wait command-48", Kind: eventstream.ToolKindOther, Status: eventstream.ToolStatusInProgress,
			RawInput: taskInput, Meta: acpToolNameMeta("Task"),
		},
	})
	if model.runningActivity.Phase != runningPhaseToolWait || model.runningActivity.Target != runningTargetSubagent {
		t.Fatalf("runningActivity = %#v, want Wait subagent", model.runningActivity)
	}
	if hint := model.buildHintText(); !strings.Contains(hint, "Waiting on subagent") || strings.Contains(hint, "command-48") {
		t.Fatalf("hint = %q, want semantic activity without raw Task handle", hint)
	}
	if blocks := mainACPTurnBlocksForTest(model); len(blocks) != 1 {
		t.Fatalf("main blocks = %#v, want only the Spawn row", blocks)
	} else if physical := physicalTranscriptEventsForTest(blocks[0].Events); len(physical) != 1 || physical[0].CallID != "spawn-1" {
		t.Fatalf("main events = %#v, want only the physical Spawn row", blocks[0].Events)
	}
	completed := eventstream.ToolStatusCompleted
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-1",
			ToolName:   "Spawn",
		},
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "task-wait-1", Status: &completed,
			RawInput: taskInput, RawOutput: map[string]any{
				"action": "wait", "handle": "command-48", "target_kind": "subagent",
				"state": "running", "parent_call": "spawn-1", "parent_tool": "Spawn",
			},
			Meta: acpToolNameMeta("Task"),
		},
	})
	if model.runningActivity.Phase != runningPhaseToolWait ||
		model.runningActivity.Key != "tool:turn-1:spawn-1" {
		t.Fatalf("runningActivity = %#v, want completed wait observer removed while running Spawn owner remains", model.runningActivity)
	}

	cancelInput := map[string]any{
		"action": "cancel",
		"handle": "command-48",
	}
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "task-cancel-1",
			Title: "Task cancel command-48", Kind: eventstream.ToolKindOther, Status: eventstream.ToolStatusInProgress,
			RawInput: cancelInput, Meta: acpToolNameMeta("Task"),
		},
	})
	if model.runningActivity.Phase != runningPhaseCancel || model.runningActivity.Target != runningTargetSubagent {
		t.Fatalf("runningActivity = %#v, want Cancel subagent", model.runningActivity)
	}
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "task-cancel-1", Status: &completed,
			RawInput: cancelInput, RawOutput: map[string]any{
				"action": "cancel", "handle": "command-48", "target_kind": "subagent", "state": "cancelled",
			},
			Meta: acpToolNameMeta("Task"),
		},
	})
	if model.runningActivity.Phase != runningPhaseToolWait || model.runningActivity.Target != runningTargetSubagent {
		t.Fatalf("runningActivity = %#v, want the still-running Task wait restored after cancel completes", model.runningActivity)
	}
	if blocks := mainACPTurnBlocksForTest(model); len(blocks) != 1 {
		t.Fatalf("main blocks = %#v, want no Task cancel row beside Spawn", blocks)
	} else if physical := physicalTranscriptEventsForTest(blocks[0].Events); len(physical) != 1 || physical[0].CallID != "spawn-1" {
		t.Fatalf("main events = %#v, want no physical Task cancel row beside Spawn", blocks[0].Events)
	}

	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-1",
			ToolName:   "Spawn",
		},
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "task-wait-1", Status: &completed,
			RawInput: taskInput, RawOutput: map[string]any{
				"action": "wait", "handle": "command-48", "target_kind": "subagent",
				"state": "completed", "parent_call": "spawn-1", "parent_tool": "Spawn", "final_message": "done",
			},
			Meta: acpToolNameMeta("Task"),
		},
	})
	if model.runningActivity.Phase != runningPhaseModelWait {
		t.Fatalf("runningActivity = %#v, want terminal Task observation to close both observer and Spawn owner", model.runningActivity)
	}
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "spawn-1", Status: &completed,
			RawOutput: map[string]any{"handle": "command-48", "state": "completed"}, Meta: acpToolNameMeta("Spawn"),
		},
	})
	if model.runningActivity.Phase != runningPhaseModelWait {
		t.Fatalf("runningActivity = %#v, want model waiting after the Spawn and Task controls complete", model.runningActivity)
	}

	failed := eventstream.ToolStatusFailed
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "task-cancel-failed",
			Title: "Task cancel command-48", Kind: eventstream.ToolKindOther, Status: eventstream.ToolStatusInProgress,
			RawInput: cancelInput, Meta: acpToolNameMeta("Task"),
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "task-cancel-failed", Status: &failed,
			RawInput: cancelInput, RawOutput: map[string]any{
				"action": "cancel", "handle": "command-48", "target_kind": "subagent", "error": "cancel denied",
			},
			Meta: acpToolNameMeta("Task"),
		},
	})
	blocks := mainACPTurnBlocksForTest(model)
	foundFailure := false
	for _, block := range blocks {
		for _, event := range block.Events {
			if event.CallID == "task-cancel-failed" && event.Err {
				foundFailure = true
			}
		}
	}
	if !foundFailure {
		t.Fatalf("main blocks = %#v, want failed Task cancel result to remain visible", blocks)
	}
}

func TestSpawnPollingPreservesEveryNarrativeStepAndClosesActivity(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(300, 0))
	apply := func(env eventstream.Envelope) {
		env.Kind = eventstream.KindSessionUpdate
		env.SessionID = "session-1"
		env.TurnID = "turn-1"
		env.Scope = eventstream.ScopeMain
		model = applyACPEnvelopeForTest(t, model, env)
	}
	apply(eventstream.Envelope{
		EventID: "spawn-start",
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall,
			ToolCallID:    "spawn-1",
			Title:         "Spawn orbit: inspect",
			Kind:          eventstream.ToolKindExecute,
			Status:        eventstream.ToolStatusInProgress,
			RawInput:      map[string]any{"agent": "orbit", "prompt": "inspect"},
			Meta:          acpToolNameMeta("Spawn"),
		},
	})
	running := eventstream.ToolStatusInProgress
	apply(eventstream.Envelope{
		EventID: "spawn-running",
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "spawn-1",
			Status:        &running,
			RawOutput:     map[string]any{"handle": "orbit", "state": "running"},
			Meta:          acpToolNameMeta("Spawn"),
		},
	})
	apply(eventstream.Envelope{
		EventID: "reasoning-1", ProjectionID: "acp-projection:cmVhc29uaW5nLTE:0", Final: true,
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentThought,
			MessageID:     "reasoning-1",
			Content:       eventstream.TextContent{Type: "text", Text: "The sub-agent has been spawned. I will wait."},
		},
	})

	completed := eventstream.ToolStatusCompleted
	firstWaitInput := map[string]any{"action": "wait", "handle": "orbit"}
	apply(eventstream.Envelope{
		EventID: "wait-1-start",
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall,
			ToolCallID:    "wait-1",
			Title:         "Task wait orbit",
			Kind:          eventstream.ToolKindOther,
			Status:        eventstream.ToolStatusInProgress,
			RawInput:      firstWaitInput,
			Meta:          acpToolNameMeta("Task"),
		},
	})
	apply(eventstream.Envelope{
		EventID: "wait-1-result",
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-1",
			ToolName:   "Spawn",
		},
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "wait-1",
			Status:        &completed,
			RawInput:      firstWaitInput,
			RawOutput: map[string]any{
				"action": "wait", "handle": "orbit", "target_kind": "subagent",
				"state": "running", "parent_call": "spawn-1", "parent_tool": "Spawn",
			},
			Meta: acpToolNameMeta("Task"),
		},
	})
	apply(eventstream.Envelope{
		EventID: "reasoning-2", ProjectionID: "acp-projection:cmVhc29uaW5nLTI:0", Final: true,
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentThought,
			MessageID:     "reasoning-2",
			Content:       eventstream.TextContent{Type: "text", Text: "The task is still running. I will wait again."},
		},
	})

	secondWaitInput := map[string]any{"action": "wait", "handle": "orbit"}
	apply(eventstream.Envelope{
		EventID: "wait-2-start",
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall,
			ToolCallID:    "wait-2",
			Title:         "Task wait orbit",
			Kind:          eventstream.ToolKindOther,
			Status:        eventstream.ToolStatusInProgress,
			RawInput:      secondWaitInput,
			Meta:          acpToolNameMeta("Task"),
		},
	})
	apply(eventstream.Envelope{
		EventID: "wait-2-result",
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-1",
			ToolName:   "Spawn",
		},
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "wait-2",
			Status:        &completed,
			RawInput:      secondWaitInput,
			RawOutput: map[string]any{
				"action": "wait", "handle": "orbit", "target_kind": "subagent",
				"state": "completed", "parent_call": "spawn-1", "parent_tool": "Spawn",
				"final_message": "child done",
			},
			Meta: acpToolNameMeta("Task"),
		},
	})
	apply(eventstream.Envelope{
		EventID: "reasoning-3", ProjectionID: "acp-projection:cmVhc29uaW5nLTM:0", Final: true,
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentThought,
			MessageID:     "reasoning-3",
			Content:       eventstream.TextContent{Type: "text", Text: "The sub-agent completed. I will verify the result."},
		},
	})
	apply(eventstream.Envelope{
		EventID: "assistant-1", ProjectionID: "acp-projection:YXNzaXN0YW50LTE:0", Final: true,
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentMessage,
			MessageID:     "assistant-1",
			Content:       eventstream.TextContent{Type: "text", Text: "Verification complete."},
		},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	narratives := make([]SubagentEvent, 0, 4)
	var spawnEvent *SubagentEvent
	for index := range block.Events {
		event := &block.Events[index]
		switch event.Kind {
		case SEReasoning, SEAssistant:
			narratives = append(narratives, *event)
		case SEToolCall:
			if event.Name == surfaceToolTask {
				t.Fatalf("hidden Task control rendered a panel: %#v", *event)
			}
			if event.CallID == "spawn-1" {
				spawnEvent = event
			}
		}
	}
	wantNarratives := []string{
		"The sub-agent has been spawned. I will wait.",
		"The task is still running. I will wait again.",
		"The sub-agent completed. I will verify the result.",
		"Verification complete.",
	}
	if len(narratives) != len(wantNarratives) {
		t.Fatalf("narratives = %#v, want all polling-step reasoning and assistant messages", narratives)
	}
	for index, want := range wantNarratives {
		if narratives[index].Text != want {
			t.Fatalf("narrative[%d] = %q, want %q", index, narratives[index].Text, want)
		}
	}
	if spawnEvent == nil || !spawnEvent.Done || spawnEvent.Err || spawnEvent.Output != "child done" {
		t.Fatalf("Spawn event = %#v, want terminal observed child result", spawnEvent)
	}
	if model.runningActivity.Phase != runningPhaseResponding {
		t.Fatalf("runningActivity = %#v, want response focus with no stale wait owner/observer", model.runningActivity)
	}
}
