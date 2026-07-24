package tuiapp

import (
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestTaskWaitAndCancelUseActivityHintWithoutTranscriptRows(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Now())
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "spawn-1",
			Title: "SPAWN orbit: inspect", Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"agent": "orbit", "prompt": "inspect"}, Meta: acpToolNameMeta("SPAWN"),
		},
	})
	running := schema.ToolStatusInProgress
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "spawn-1", Status: &running,
			RawOutput: map[string]any{"handle": "command-48", "state": "running"}, Meta: acpToolNameMeta("SPAWN"),
		},
	})
	taskInput := map[string]any{
		"action": "wait",
		"handle": "command-48",
	}
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "task-wait-1",
			Title: "TASK wait command-48", Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: taskInput, Meta: acpToolNameMeta("TASK"),
		},
	})
	if model.runningActivity.Phase != runningPhaseWait || model.runningActivity.Target != runningTargetSubagent {
		t.Fatalf("runningActivity = %#v, want Wait subagent", model.runningActivity)
	}
	if hint := model.buildHintText(); !strings.Contains(hint, "Wait subagent") || strings.Contains(hint, "command-48") {
		t.Fatalf("hint = %q, want semantic activity without raw Task handle", hint)
	}
	if blocks := mainACPTurnBlocksForTest(model); len(blocks) != 1 {
		t.Fatalf("main blocks = %#v, want only the Spawn row", blocks)
	} else if physical := physicalTranscriptEventsForTest(blocks[0].Events); len(physical) != 1 || physical[0].CallID != "spawn-1" {
		t.Fatalf("main events = %#v, want only the physical Spawn row", blocks[0].Events)
	}
	completed := schema.ToolStatusCompleted
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-1",
			ToolName:   "SPAWN",
		},
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "task-wait-1", Status: &completed,
			RawInput: taskInput, RawOutput: map[string]any{
				"action": "wait", "handle": "command-48", "target_kind": "subagent",
				"state": "running", "parent_call": "spawn-1", "parent_tool": "SPAWN",
			},
			Meta: acpToolNameMeta("TASK"),
		},
	})
	if model.runningActivity.Phase != runningPhaseWait ||
		model.runningActivity.Key != "tool:turn-1:spawn-1" {
		t.Fatalf("runningActivity = %#v, want completed wait observer removed while running Spawn owner remains", model.runningActivity)
	}

	cancelInput := map[string]any{
		"action": "cancel",
		"handle": "command-48",
	}
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "task-cancel-1",
			Title: "TASK cancel command-48", Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: cancelInput, Meta: acpToolNameMeta("TASK"),
		},
	})
	if model.runningActivity.Phase != runningPhaseCancel || model.runningActivity.Target != runningTargetSubagent {
		t.Fatalf("runningActivity = %#v, want Cancel subagent", model.runningActivity)
	}
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "task-cancel-1", Status: &completed,
			RawInput: cancelInput, RawOutput: map[string]any{
				"action": "cancel", "handle": "command-48", "target_kind": "subagent", "state": "cancelled",
			},
			Meta: acpToolNameMeta("TASK"),
		},
	})
	if model.runningActivity.Phase != runningPhaseWait || model.runningActivity.Target != runningTargetSubagent {
		t.Fatalf("runningActivity = %#v, want the still-running Task wait restored after cancel completes", model.runningActivity)
	}
	if blocks := mainACPTurnBlocksForTest(model); len(blocks) != 1 {
		t.Fatalf("main blocks = %#v, want no TASK cancel row beside Spawn", blocks)
	} else if physical := physicalTranscriptEventsForTest(blocks[0].Events); len(physical) != 1 || physical[0].CallID != "spawn-1" {
		t.Fatalf("main events = %#v, want no physical TASK cancel row beside Spawn", blocks[0].Events)
	}

	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-1",
			ToolName:   "SPAWN",
		},
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "task-wait-1", Status: &completed,
			RawInput: taskInput, RawOutput: map[string]any{
				"action": "wait", "handle": "command-48", "target_kind": "subagent",
				"state": "completed", "parent_call": "spawn-1", "parent_tool": "SPAWN", "final_message": "done",
			},
			Meta: acpToolNameMeta("TASK"),
		},
	})
	if model.runningActivity.Phase != runningPhaseThinking {
		t.Fatalf("runningActivity = %#v, want terminal Task observation to close both observer and Spawn owner", model.runningActivity)
	}
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "spawn-1", Status: &completed,
			RawOutput: map[string]any{"handle": "command-48", "state": "completed"}, Meta: acpToolNameMeta("SPAWN"),
		},
	})
	if model.runningActivity.Phase != runningPhaseThinking {
		t.Fatalf("runningActivity = %#v, want thinking after the Spawn and Task controls complete", model.runningActivity)
	}

	failed := schema.ToolStatusFailed
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "task-cancel-failed",
			Title: "TASK cancel command-48", Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: cancelInput, Meta: acpToolNameMeta("TASK"),
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "task-cancel-failed", Status: &failed,
			RawInput: cancelInput, RawOutput: map[string]any{
				"action": "cancel", "handle": "command-48", "target_kind": "subagent", "error": "cancel denied",
			},
			Meta: acpToolNameMeta("TASK"),
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
		t.Fatalf("main blocks = %#v, want failed TASK cancel result to remain visible", blocks)
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
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "spawn-1",
			Title:         "SPAWN orbit: inspect",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"agent": "orbit", "prompt": "inspect"},
			Meta:          acpToolNameMeta("SPAWN"),
		},
	})
	running := schema.ToolStatusInProgress
	apply(eventstream.Envelope{
		EventID: "spawn-running",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "spawn-1",
			Status:        &running,
			RawOutput:     map[string]any{"handle": "orbit", "state": "running"},
			Meta:          acpToolNameMeta("SPAWN"),
		},
	})
	apply(eventstream.Envelope{
		EventID: "reasoning-1", ProjectionID: eventstream.FormatProjectionID("reasoning-1", 0), Final: true,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentThought,
			MessageID:     "reasoning-1",
			Content:       schema.TextContent{Type: "text", Text: "The sub-agent has been spawned. I will wait."},
		},
	})

	completed := schema.ToolStatusCompleted
	firstWaitInput := map[string]any{"action": "wait", "handle": "orbit"}
	apply(eventstream.Envelope{
		EventID: "wait-1-start",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "wait-1",
			Title:         "TASK wait orbit",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusInProgress,
			RawInput:      firstWaitInput,
			Meta:          acpToolNameMeta("TASK"),
		},
	})
	apply(eventstream.Envelope{
		EventID: "wait-1-result",
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-1",
			ToolName:   "SPAWN",
		},
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "wait-1",
			Status:        &completed,
			RawInput:      firstWaitInput,
			RawOutput: map[string]any{
				"action": "wait", "handle": "orbit", "target_kind": "subagent",
				"state": "running", "parent_call": "spawn-1", "parent_tool": "SPAWN",
			},
			Meta: acpToolNameMeta("TASK"),
		},
	})
	apply(eventstream.Envelope{
		EventID: "reasoning-2", ProjectionID: eventstream.FormatProjectionID("reasoning-2", 0), Final: true,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentThought,
			MessageID:     "reasoning-2",
			Content:       schema.TextContent{Type: "text", Text: "The task is still running. I will wait again."},
		},
	})

	secondWaitInput := map[string]any{"action": "wait", "handle": "orbit"}
	apply(eventstream.Envelope{
		EventID: "wait-2-start",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "wait-2",
			Title:         "TASK wait orbit",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusInProgress,
			RawInput:      secondWaitInput,
			Meta:          acpToolNameMeta("TASK"),
		},
	})
	apply(eventstream.Envelope{
		EventID: "wait-2-result",
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-1",
			ToolName:   "SPAWN",
		},
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "wait-2",
			Status:        &completed,
			RawInput:      secondWaitInput,
			RawOutput: map[string]any{
				"action": "wait", "handle": "orbit", "target_kind": "subagent",
				"state": "completed", "parent_call": "spawn-1", "parent_tool": "SPAWN",
				"final_message": "child done",
			},
			Meta: acpToolNameMeta("TASK"),
		},
	})
	apply(eventstream.Envelope{
		EventID: "reasoning-3", ProjectionID: eventstream.FormatProjectionID("reasoning-3", 0), Final: true,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentThought,
			MessageID:     "reasoning-3",
			Content:       schema.TextContent{Type: "text", Text: "The sub-agent completed. I will verify the result."},
		},
	})
	apply(eventstream.Envelope{
		EventID: "assistant-1", ProjectionID: eventstream.FormatProjectionID("assistant-1", 0), Final: true,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			MessageID:     "assistant-1",
			Content:       schema.TextContent{Type: "text", Text: "Verification complete."},
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
			if strings.EqualFold(toolSemanticName(event.Name, event.ToolKind), "TASK") {
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
