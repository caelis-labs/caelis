package taskstream

import (
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestTaskOwnerRepairsFromEnvelopeUsesTypedSingularCommandParent(t *testing.T) {
	t.Parallel()

	completed := schema.ToolStatusCompleted
	env := eventstream.Envelope{
		Kind:  eventstream.KindSessionUpdate,
		Scope: eventstream.ScopeMain,
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "command-call",
			ToolName:   "RunCommand",
		},
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "read-command",
			Status:        &completed,
			RawInput:      map[string]any{"action": "read", "handle": "command-3"},
			RawOutput: map[string]any{
				"handle": "command-3", "parent_call": "command-call", "parent_tool": "RunCommand",
				"state": "completed", "target_kind": "command",
			},
		},
	}
	want := []CommandTaskResult{{
		ParentCallID: "command-call",
		Handle:       "command-3",
	}}
	if got := TaskOwnerRepairsFromEnvelope(env).Commands; !reflect.DeepEqual(got, want) {
		t.Fatalf("TaskOwnerRepairsFromEnvelope().Commands = %#v, want %#v", got, want)
	}

	env.ParentTool = nil
	env.Meta = map[string]any{"parent_call": "command-call", "parent_tool": "RunCommand"}
	if got := TaskOwnerRepairsFromEnvelope(env).Commands; len(got) != 0 {
		t.Fatalf("metadata-only parent produced command results %#v", got)
	}
}

func TestTaskOwnerRepairsFromEnvelopeFiltersBatchItems(t *testing.T) {
	t.Parallel()

	completed := schema.ToolStatusCompleted
	env := eventstream.Envelope{
		Kind:  eventstream.KindSessionUpdate,
		Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "wait-batch",
			Status:        &completed,
			RawInput:      map[string]any{"action": "wait", "handle": "command-1,command-2,maia"},
			RawOutput: map[string]any{
				"tasks": []any{
					map[string]any{
						"handle": "command-1", "parent_call": "command-call-1", "parent_tool": "RunCommand",
						"state": "completed", "target_kind": "command",
					},
					map[string]any{
						"handle": "command-2", "parent_call": "command-call-2", "parent_tool": "RunCommand",
						"state": "running", "target_kind": "command",
					},
					map[string]any{
						"handle": "maia", "parent_call": "spawn-1", "parent_tool": "Spawn",
						"state": "completed", "target_kind": "subagent",
					},
				},
			},
		},
	}
	want := []CommandTaskResult{{
		ParentCallID: "command-call-1",
		Handle:       "command-1",
	}}
	repairs := TaskOwnerRepairsFromEnvelope(env)
	if !reflect.DeepEqual(repairs.Commands, want) || len(repairs.Spawns) != 1 ||
		repairs.Spawns[0].ParentCallID != "spawn-1" {
		t.Fatalf("TaskOwnerRepairsFromEnvelope() = %#v, want command-1 and spawn-1", repairs)
	}
}
