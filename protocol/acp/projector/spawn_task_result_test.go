package projector

import (
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestSpawnTaskResultsFromEnvelopeUsesTypedSingularParent(t *testing.T) {
	completed := schema.ToolStatusCompleted
	env := eventstream.Envelope{
		Kind:  eventstream.KindSessionUpdate,
		Scope: eventstream.ScopeMain,
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-alpha",
			ToolName:   "Spawn",
		},
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
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
		Status:       schema.ToolStatusCompleted,
		RawOutput: map[string]any{
			"handle": "alpha", "parent_call": "spawn-alpha", "parent_tool": "Spawn",
			"state": "completed", "target_kind": "subagent", "final_message": "alpha done",
		},
	}}
	if got := SpawnTaskResultsFromEnvelope(env); !reflect.DeepEqual(got, want) {
		t.Fatalf("SpawnTaskResultsFromEnvelope() = %#v, want %#v", got, want)
	}

	env.ParentTool = nil
	env.Meta = map[string]any{"parent_call": "spawn-alpha", "parent_tool": "Spawn"}
	if got := SpawnTaskResultsFromEnvelope(env); len(got) != 0 {
		t.Fatalf("metadata-only parent produced results %#v, want none", got)
	}
}

func TestSpawnTaskResultsFromEnvelopeFiltersBatchItems(t *testing.T) {
	completed := schema.ToolStatusCompleted
	env := eventstream.Envelope{
		Kind:  eventstream.KindSessionUpdate,
		Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
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
				},
			},
		},
	}
	got := SpawnTaskResultsFromEnvelope(env)
	if len(got) != 3 || got[0].ParentCallID != "spawn-alpha" || got[0].Status != schema.ToolStatusCompleted ||
		got[1].ParentCallID != "spawn-gamma" || got[1].Status != schema.ToolStatusFailed ||
		got[2].ParentCallID != "spawn-alpha" || got[2].RawOutput["handle"] != "delta" {
		t.Fatalf("SpawnTaskResultsFromEnvelope() = %#v, want alpha/gamma plus distinct handle reusing spawn-alpha", got)
	}
}

func TestSpawnTaskResultsFromEnvelopeDoesNotDeduplicateMissingHandles(t *testing.T) {
	completed := schema.ToolStatusCompleted
	env := eventstream.Envelope{
		Kind:  eventstream.KindSessionUpdate,
		Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
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
