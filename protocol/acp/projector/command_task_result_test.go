package projector

import (
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestCommandTaskResultsFromEnvelopeUsesTypedSingularParent(t *testing.T) {
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
	if got := CommandTaskResultsFromEnvelope(env); !reflect.DeepEqual(got, want) {
		t.Fatalf("CommandTaskResultsFromEnvelope() = %#v, want %#v", got, want)
	}

	env.ParentTool = nil
	env.Meta = map[string]any{"parent_call": "command-call", "parent_tool": "RunCommand"}
	if got := CommandTaskResultsFromEnvelope(env); len(got) != 0 {
		t.Fatalf("metadata-only parent produced command results %#v", got)
	}
}

func TestCommandTaskResultsFromEnvelopeFiltersBatchItems(t *testing.T) {
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
	if got := CommandTaskResultsFromEnvelope(env); !reflect.DeepEqual(got, want) {
		t.Fatalf("CommandTaskResultsFromEnvelope() = %#v, want %#v", got, want)
	}
	repairs := TaskOwnerRepairsFromEnvelope(env)
	if !reflect.DeepEqual(repairs.Commands, want) || len(repairs.Spawns) != 1 ||
		repairs.Spawns[0].ParentCallID != "spawn-1" {
		t.Fatalf("TaskOwnerRepairsFromEnvelope() = %#v, want command-1 and spawn-1", repairs)
	}
}
