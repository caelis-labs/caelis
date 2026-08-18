package projector

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestEnvelopeBaseProjectsCanonicalTaskObservationParent(t *testing.T) {
	for _, test := range []struct {
		name       string
		action     string
		targetKind string
		parentCall string
		parentTool string
	}{
		{name: "wait Spawn", action: "wait", targetKind: "subagent", parentCall: "spawn-call-1", parentTool: "Spawn"},
		{name: "read Spawn", action: "read", targetKind: "subagent", parentCall: "spawn-call-1", parentTool: "Spawn"},
		{name: "wait RunCommand", action: "wait", targetKind: "command", parentCall: "command-call-1", parentTool: "RunCommand"},
		{name: "read RunCommand terminal alias", action: "read", targetKind: "terminal", parentCall: "command-call-1", parentTool: "RunCommand"},
	} {
		t.Run(test.name, func(t *testing.T) {
			event := canonicalTaskObservationEventForParentTest(
				test.action,
				test.targetKind,
				test.parentCall,
				test.parentTool,
			)
			base := EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: "session-1"}, event, SessionEventTransport{})
			if base.ParentTool == nil || base.ParentTool.ToolCallID != test.parentCall || base.ParentTool.ToolName != test.parentTool {
				t.Fatalf("ParentTool = %#v, want typed %s/%s relation", base.ParentTool, test.parentTool, test.parentCall)
			}
		})
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
			name: "non observation action",
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
			name: "parent tool mismatches target",
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
			event := canonicalTaskObservationEventForParentTest("wait", "subagent", "spawn-call-1", "Spawn")
			tt.mutate(event)
			base := EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: "session-1"}, event, SessionEventTransport{})
			if base.ParentTool != nil {
				t.Fatalf("ParentTool = %#v, want no inferred relation", base.ParentTool)
			}
		})
	}
}

func canonicalTaskObservationEventForParentTest(
	action string,
	targetKind string,
	parentCall string,
	parentTool string,
) *session.Event {
	return &session.Event{
		ID:         "task-result-1",
		SessionID:  "session-1",
		Type:       session.EventTypeToolResult,
		Visibility: session.VisibilityCanonical,
		Tool: &session.EventTool{
			ID:     "task-call-1",
			Name:   "Task",
			Status: "completed",
			Input:  map[string]any{"action": action, "task_id": "helper"},
			Output: map[string]any{
				"task_id":       "helper",
				"state":         "completed",
				"target_kind":   targetKind,
				"parent_call":   parentCall,
				"parent_tool":   parentTool,
				"final_message": "完成",
			},
		},
	}
}
