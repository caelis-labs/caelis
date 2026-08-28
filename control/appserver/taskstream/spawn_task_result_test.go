package taskstream

import (
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestSpawnTaskResultsFromEnvelopeUsesTypedSingularParent(t *testing.T) {
	completed := eventstream.ToolStatusCompleted
	env := eventstream.Envelope{
		Kind:  eventstream.KindSessionUpdate,
		Scope: eventstream.ScopeMain,
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-alpha",
			ToolName:   "Spawn",
		},
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "wait-alpha",
			Status:        &completed,
			RawInput:      map[string]any{"action": "wait", "handle": "alpha"},
			RawOutput: map[string]any{
				"handle": "alpha", "parent_call": "spawn-alpha", "parent_tool": "Spawn",
				"state": "completed", "target_kind": "subagent", "final_message": "alpha done",
			},
		},
	}
	want := []SpawnTaskResult{{
		ParentCallID: "spawn-alpha",
		Status:       eventstream.ToolStatusCompleted,
		RawOutput: map[string]any{
			"handle": "alpha", "parent_call": "spawn-alpha", "parent_tool": "Spawn",
			"state": "completed", "target_kind": "subagent", "final_message": "alpha done",
		},
	}}
	if got := SpawnTaskResultsFromEnvelope(env); !reflect.DeepEqual(got, want) {
		t.Fatalf("SpawnTaskResultsFromEnvelope() = %#v, want %#v", got, want)
	}

	readUpdate := env.Update.(eventstream.ToolCallUpdate)
	readUpdate.ToolCallID = "read-alpha"
	readUpdate.RawInput = map[string]any{"action": "read", "handle": "alpha"}
	env.Update = readUpdate
	if got := SpawnTaskResultsFromEnvelope(env); !reflect.DeepEqual(got, want) {
		t.Fatalf("SpawnTaskResultsFromEnvelope(read) = %#v, want %#v", got, want)
	}
	repairs := TaskOwnerRepairsFromEnvelope(env)
	if len(repairs.Spawns) != 1 || len(repairs.Commands) != 0 {
		t.Fatalf("TaskOwnerRepairsFromEnvelope(read) = %#v, want one Spawn repair", repairs)
	}

	env.ParentTool = nil
	env.Meta = map[string]any{"parent_call": "spawn-alpha", "parent_tool": "Spawn"}
	if got := SpawnTaskResultsFromEnvelope(env); len(got) != 0 {
		t.Fatalf("metadata-only parent produced results %#v, want none", got)
	}
}

func TestSpawnTaskResultsFromEnvelopeExpandsUnreadTurnFinalResponses(t *testing.T) {
	t.Parallel()

	completed := eventstream.ToolStatusCompleted
	env := eventstream.Envelope{
		Kind:  eventstream.KindSessionUpdate,
		Scope: eventstream.ScopeMain,
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-alpha",
			ToolName:   "Spawn",
		},
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "read-alpha",
			Status:        &completed,
			RawInput:      map[string]any{"action": "read", "handle": "alpha"},
			RawOutput: map[string]any{
				"handle": "alpha", "parent_call": "spawn-alpha", "parent_tool": "Spawn",
				"state": "running", "target_kind": "subagent", "output_preview": "third turn still running",
				"final_responses": []any{
					map[string]any{"turn_id": "task-1:1", "turn_seq": int64(1), "final_message": "\nfirst done\n"},
					map[string]any{"turn_id": "task-1:2", "turn_seq": int64(2), "final_message": "second done"},
				},
			},
		},
	}

	got := SpawnTaskResultsFromEnvelope(env)
	if len(got) != 2 || got[0].Status != eventstream.ToolStatusCompleted || got[1].Status != eventstream.ToolStatusCompleted {
		t.Fatalf("SpawnTaskResultsFromEnvelope() = %#v, want two completed Turn results", got)
	}
	if got[0].RawOutput["turn_id"] != "task-1:1" || got[0].RawOutput["final_message"] != "\nfirst done\n" ||
		got[1].RawOutput["turn_id"] != "task-1:2" || got[1].RawOutput["final_message"] != "second done" {
		t.Fatalf("expanded results = %#v, want ordered exact Turn finals", got)
	}
	if _, exists := got[0].RawOutput["final_responses"]; exists {
		t.Fatalf("expanded RawOutput retained aggregate final_responses: %#v", got[0].RawOutput)
	}
}

func TestSpawnTaskResultsFromEnvelopeKeepsCurrentFailureAfterUnreadFinals(t *testing.T) {
	t.Parallel()

	completed := eventstream.ToolStatusCompleted
	env := eventstream.Envelope{
		Kind:  eventstream.KindSessionUpdate,
		Scope: eventstream.ScopeMain,
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-alpha",
			ToolName:   "Spawn",
		},
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "read-alpha",
			Status:        &completed,
			RawInput:      map[string]any{"action": "read", "handle": "alpha"},
			RawOutput: map[string]any{
				"handle": "alpha", "parent_call": "spawn-alpha", "parent_tool": "Spawn",
				"state": "failed", "target_kind": "subagent", "turn_id": "task-1:2",
				"error": "second Turn failed", "final_message": "first done",
				"final_responses": []any{
					map[string]any{"turn_id": "task-1:1", "turn_seq": int64(1), "final_message": "first done"},
				},
			},
		},
	}

	got := SpawnTaskResultsFromEnvelope(env)
	if len(got) != 2 || got[0].Status != eventstream.ToolStatusCompleted || got[1].Status != eventstream.ToolStatusFailed {
		t.Fatalf("SpawnTaskResultsFromEnvelope() = %#v, want prior completion plus current failure", got)
	}
	if got[1].RawOutput["turn_id"] != "task-1:2" || got[1].RawOutput["error"] != "second Turn failed" {
		t.Fatalf("current failure = %#v, want Turn 2 diagnostic", got[1])
	}
	if _, exists := got[1].RawOutput["final_message"]; exists {
		t.Fatalf("current failure retained stale final_message: %#v", got[1].RawOutput)
	}
}

func TestSpawnTaskResultsFromEnvelopeFiltersBatchItems(t *testing.T) {
	completed := eventstream.ToolStatusCompleted
	env := eventstream.Envelope{
		Kind:  eventstream.KindSessionUpdate,
		Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "wait-batch",
			Status:        &completed,
			RawInput:      map[string]any{"action": "wait", "handle": "alpha,beta,gamma"},
			RawOutput: map[string]any{
				"action": "wait",
				"tasks": []any{
					map[string]any{
						"handle": "alpha", "parent_call": "spawn-alpha", "parent_tool": "Spawn",
						"state": "completed", "target_kind": "subagent", "final_message": "alpha done",
					},
					map[string]any{
						"handle": "beta", "parent_call": "spawn-beta", "parent_tool": "Spawn",
						"state": "running", "target_kind": "subagent",
					},
					map[string]any{
						"handle": "gamma", "parent_call": "spawn-gamma", "parent_tool": "Spawn",
						"state": "failed", "target_kind": "subagent", "error": "gamma failed",
					},
					map[string]any{
						"handle": "command", "parent_call": "command-call", "parent_tool": "RunCommand",
						"state": "completed", "target_kind": "command",
					},
					map[string]any{
						"handle": "alpha", "parent_call": "spawn-alpha", "parent_tool": "Spawn",
						"state": "completed", "target_kind": "subagent", "final_message": "duplicate",
					},
					map[string]any{
						"handle": "delta", "parent_call": "spawn-alpha", "parent_tool": "Spawn",
						"state": "completed", "target_kind": "subagent", "final_message": "reused call ID",
					},
					map[string]any{
						"handle": "epsilon", "parent_call": "spawn-epsilon", "parent_tool": "Spawn",
						"state": "unknown_outcome", "target_kind": "subagent", "error": "delivery outcome unknown",
					},
				},
			},
		},
	}
	got := SpawnTaskResultsFromEnvelope(env)
	if len(got) != 4 || got[0].ParentCallID != "spawn-alpha" || got[0].Status != eventstream.ToolStatusCompleted ||
		got[1].ParentCallID != "spawn-gamma" || got[1].Status != eventstream.ToolStatusFailed ||
		got[2].ParentCallID != "spawn-alpha" || got[2].RawOutput["handle"] != "delta" ||
		got[3].ParentCallID != "spawn-epsilon" || got[3].Status != eventstream.ToolStatusFailed {
		t.Fatalf("SpawnTaskResultsFromEnvelope() = %#v, want alpha/gamma/delta plus failed unknown-outcome epsilon", got)
	}
}

func TestSpawnTaskResultsFromEnvelopeDoesNotDeduplicateMissingHandles(t *testing.T) {
	completed := eventstream.ToolStatusCompleted
	env := eventstream.Envelope{
		Kind:  eventstream.KindSessionUpdate,
		Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "wait-batch",
			Status:        &completed,
			RawInput:      map[string]any{"action": "wait"},
			RawOutput: map[string]any{
				"action": "wait",
				"tasks": []any{
					map[string]any{
						"parent_call": "spawn-reused", "parent_tool": "Spawn",
						"state": "completed", "target_kind": "subagent", "final_message": "first",
					},
					map[string]any{
						"parent_call": "spawn-reused", "parent_tool": "Spawn",
						"state": "failed", "target_kind": "subagent", "error": "second",
					},
				},
			},
		},
	}

	if got := SpawnTaskResultsFromEnvelope(env); len(got) != 2 {
		t.Fatalf("SpawnTaskResultsFromEnvelope() = %#v, want both handle-free observations preserved for fail-closed resolution", got)
	}
}
