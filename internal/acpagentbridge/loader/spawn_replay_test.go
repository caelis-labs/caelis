package loader

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

func TestSpawnReplayCanonicalParentResultWinsOverEarlierTaskWait(t *testing.T) {
	directFinal := &session.Event{
		Type:       session.EventTypeToolResult,
		Visibility: session.VisibilityCanonical,
		Tool: &session.EventTool{
			ID:     "spawn-alpha",
			Name:   "Spawn",
			Status: "completed",
			Output: map[string]any{
				"state": "completed", "target_kind": "subagent", "final_message": "canonical final",
			},
		},
	}
	replay := newSpawnReplayProjector([]*session.Event{directFinal})
	completed := acp.ToolStatusCompleted
	wait := eventstream.Envelope{
		Kind:  eventstream.KindSessionUpdate,
		Scope: eventstream.ScopeMain,
		Update: acp.ToolCallUpdate{
			SessionUpdate: acp.UpdateToolCallInfo,
			ToolCallID:    "wait-alpha",
			Status:        &completed,
			RawInput:      map[string]any{"action": "wait", "handle": "alpha"},
			RawOutput: map[string]any{
				"action": "wait",
				"tasks": []any{map[string]any{
					"handle": "alpha", "parent_call": "spawn-alpha", "parent_tool": "Spawn",
					"state": "completed", "target_kind": "subagent", "final_message": "observer final",
				}},
			},
		},
	}
	if got := replay.observedParentCloses(wait, "session-1"); len(got) != 0 {
		t.Fatalf("observer closes = %#v, want none when a canonical parent result exists", got)
	}

	notification := replay.normalize(directFinal, acp.SessionNotification{
		SessionID: "session-1",
		Update: acp.ToolCallUpdate{
			SessionUpdate: acp.UpdateToolCallInfo,
			ToolCallID:    "spawn-alpha",
			Status:        &completed,
			RawOutput:     directFinal.Tool.Output,
		},
	})
	update := notification.Update.(acp.ToolCallUpdate)
	if output, ok := metautil.TerminalOutput(update.Meta); !ok || output.Data != "canonical final" {
		t.Fatalf("canonical Spawn terminal_output = %#v, want canonical final", update.Meta)
	}
	if exit, ok := metautil.TerminalExit(update.Meta); !ok || exit.TerminalID != "spawn-alpha" {
		t.Fatalf("canonical Spawn terminal_exit = %#v, want spawn-alpha", update.Meta)
	}
}
