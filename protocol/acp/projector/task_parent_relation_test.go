package projector

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestEnvelopeBaseProjectsCanonicalTaskWaitSpawnParent(t *testing.T) {
	event := canonicalTaskWaitEventForParentTest()
	base := EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: "session-1"}, event, SessionEventTransport{})
	if base.ParentTool == nil || base.ParentTool.ToolCallID != "spawn-call-1" || base.ParentTool.ToolName != "Spawn" {
		t.Fatalf("ParentTool = %#v, want typed Spawn/spawn-call-1 relation", base.ParentTool)
	}
}

func TestEnvelopeBaseDoesNotGuessTaskParentFromMetadataOrIncompletePayload(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*session.Event)
	}{
		{
			name: "metadata only",
			mutate: func(event *session.Event) {
				delete(event.Tool.Output, "parent_call")
				delete(event.Tool.Output, "parent_tool")
				event.Meta = map[string]any{
					"caelis": map[string]any{"runtime": map[string]any{"task": map[string]any{
						"parent_call": "spawn-call-1",
						"parent_tool": "Spawn",
					}}},
				}
			},
		},
		{
			name: "non wait action",
			mutate: func(event *session.Event) {
				event.Tool.Input["action"] = "inspect"
			},
		},
		{
			name: "running observer",
			mutate: func(event *session.Event) {
				event.Tool.Status = "running"
			},
		},
		{
			name: "command target",
			mutate: func(event *session.Event) {
				event.Tool.Output["target_kind"] = "command"
			},
		},
		{
			name: "wrong parent tool",
			mutate: func(event *session.Event) {
				event.Tool.Output["parent_tool"] = "RunCommand"
			},
		},
		{
			name: "participant scoped result",
			mutate: func(event *session.Event) {
				event.Scope = &session.EventScope{Participant: session.ParticipantRef{ID: "child-1"}}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := canonicalTaskWaitEventForParentTest()
			tt.mutate(event)
			base := EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: "session-1"}, event, SessionEventTransport{})
			if base.ParentTool != nil {
				t.Fatalf("ParentTool = %#v, want no inferred relation", base.ParentTool)
			}
		})
	}
}

func canonicalTaskWaitEventForParentTest() *session.Event {
	return &session.Event{
		ID:         "task-result-1",
		SessionID:  "session-1",
		Type:       session.EventTypeToolResult,
		Visibility: session.VisibilityCanonical,
		Tool: &session.EventTool{
			ID:     "task-call-1",
			Name:   "TASK",
			Status: "completed",
			Input:  map[string]any{"action": "wait", "task_id": "helper"},
			Output: map[string]any{
				"task_id":       "helper",
				"state":         "completed",
				"target_kind":   "subagent",
				"parent_call":   "spawn-call-1",
				"parent_tool":   "SPAWN",
				"final_message": "完成",
			},
		},
	}
}
