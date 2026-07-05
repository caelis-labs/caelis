package eval

import (
	"context"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/internal/evalharness"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

type projectionTraceEntry struct {
	Kind           string `json:"kind,omitempty"`
	UpdateType     string `json:"update_type,omitempty"`
	Text           string `json:"text,omitempty"`
	ToolCallID     string `json:"tool_call_id,omitempty"`
	ToolCallName   string `json:"tool_call_name,omitempty"`
	ToolCallStatus string `json:"tool_call_status,omitempty"`
}

func projectionTrace(envs []eventstream.Envelope) []projectionTraceEntry {
	out := make([]projectionTraceEntry, 0, len(envs))
	for _, env := range envs {
		entry := projectionTraceEntry{
			Kind:       string(env.Kind),
			UpdateType: eventstream.UpdateType(env.Update),
		}
		if chunk, ok := env.Update.(schema.ContentChunk); ok {
			entry.Text = schema.ExtractTextValue(chunk.Content)
		}
		if call, ok := eventstream.ToolCallFromEnvelope(env); ok {
			entry.ToolCallID = call.ToolCallID
			entry.ToolCallName = call.Kind
			entry.ToolCallStatus = call.Status
		}
		if update, ok := eventstream.ToolCallUpdateFromEnvelope(env); ok {
			entry.ToolCallID = update.ToolCallID
			entry.ToolCallName = stringPtrValue(update.Kind)
			entry.ToolCallStatus = stringPtrValue(update.Status)
		}
		out = append(out, entry)
	}
	return out
}

func TestRegressionProjectionGoldenToolLoop(t *testing.T) {
	t.Parallel()

	scripted := evalharness.NewScriptedModel("proj-tool-loop",
		evalharness.ToolCallStep("", model.ToolCall{
			ID:   "call-1",
			Name: "ECHO",
			Args: `{"value":"pong"}`,
		}),
		evalharness.TextStep("pong"),
	)
	run, err := evalharness.RunChatScenario(context.Background(), evalharness.ChatScenario{
		Name:         "proj-tool-loop",
		SessionID:    "sess-proj-tool-loop",
		Prompt:       "say pong",
		SystemPrompt: "Use tools when needed.",
		Model:        scripted,
		Tools:        []tool.Tool{evalharness.EchoTool("ECHO")},
	})
	if err != nil {
		t.Fatalf("RunChatScenario() error = %v", err)
	}

	envs := projectRunEvents(session.SessionRef{SessionID: "sess-proj-tool-loop"}, run.Events)
	got := evalharness.StableJSON(projectionTrace(envs))
	want := `[
  {
    "kind": "session/update",
    "update_type": "tool_call",
    "tool_call_id": "call-1",
    "tool_call_name": "other",
    "tool_call_status": "pending"
  },
  {
    "kind": "session/update",
    "update_type": "tool_call_update",
    "tool_call_id": "call-1",
    "tool_call_name": "other",
    "tool_call_status": "completed"
  },
  {
    "kind": "session/update",
    "update_type": "agent_message_chunk",
    "text": "pong"
  }
]`
	if got != want {
		t.Fatalf("projection golden mismatch\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

func TestRegressionProjectionGoldenReasoning(t *testing.T) {
	t.Parallel()

	scripted := evalharness.NewScriptedModel("proj-reasoning",
		evalharness.AssistantPartsStep("the answer", "internal chain of thought"),
	)
	run, err := evalharness.RunChatScenario(context.Background(), evalharness.ChatScenario{
		Name:         "proj-reasoning",
		SessionID:    "sess-proj-reasoning",
		Prompt:       "what is 2+2",
		SystemPrompt: "Think step by step.",
		Model:        scripted,
	})
	if err != nil {
		t.Fatalf("RunChatScenario() error = %v", err)
	}

	trace := projectionTrace(projectRunEvents(session.SessionRef{SessionID: "sess-proj-reasoning"}, run.Events))
	if len(trace) != 2 {
		t.Fatalf("expected thought + answer projections, got %d: %#v", len(trace), trace)
	}
	if trace[0].UpdateType != schema.UpdateAgentThought || trace[0].Text != "internal chain of thought" {
		t.Fatalf("thought trace = %#v, want reasoning thought", trace[0])
	}
	if trace[1].UpdateType != schema.UpdateAgentMessage || trace[1].Text != "the answer" {
		t.Fatalf("answer trace = %#v, want final answer", trace[1])
	}
}

func TestRegressionProjectionGoldenMultiTool(t *testing.T) {
	t.Parallel()

	scripted := evalharness.NewScriptedModel("proj-multi-tool",
		evalharness.ToolCallStep("looking...", model.ToolCall{
			ID:   "call-a",
			Name: "READ",
			Args: `{"path":"main.go"}`,
		}, model.ToolCall{
			ID:   "call-b",
			Name: "SEARCH",
			Args: `{"path":".","pattern":"func main"}`,
		}),
		evalharness.TextStep("done"),
	)
	run, err := evalharness.RunChatScenario(context.Background(), evalharness.ChatScenario{
		Name:      "proj-multi-tool",
		SessionID: "sess-proj-multi-tool",
		Prompt:    "inspect",
		Model:     scripted,
		Tools: []tool.Tool{
			evalharness.EchoTool("READ"),
			evalharness.EchoTool("SEARCH"),
		},
	})
	if err != nil {
		t.Fatalf("RunChatScenario() error = %v", err)
	}

	trace := projectionTrace(projectRunEvents(session.SessionRef{SessionID: "sess-proj-multi-tool"}, run.Events))
	toolCallCount := 0
	toolUpdateCount := 0
	for _, entry := range trace {
		switch entry.UpdateType {
		case schema.UpdateToolCall:
			toolCallCount++
		case schema.UpdateToolCallInfo:
			toolUpdateCount++
		}
	}
	if toolCallCount != 2 {
		t.Fatalf("expected 2 tool_call projections, got %d: %#v", toolCallCount, trace)
	}
	if toolUpdateCount != 2 {
		t.Fatalf("expected 2 tool_call_update projections, got %d: %#v", toolUpdateCount, trace)
	}
}

func TestRegressionProjectionGoldenFullEnvelopes(t *testing.T) {
	t.Parallel()

	envs := projectRunEvents(session.SessionRef{SessionID: "sess-proj-full"}, projectionGoldenFixtureEvents())
	got := evalharness.StableJSON(envs)
	want := `[
  {
    "kind": "session/update",
    "event_id": "event-user",
    "session_id": "sess-proj-full",
    "turn_id": "turn-1",
    "occurred_at": "2026-06-28T12:00:00Z",
    "scope": "main",
    "scope_id": "sess-proj-full",
    "final": true,
    "update": {
      "sessionUpdate": "user_message_chunk",
      "content": {
        "type": "text",
        "text": "inspect the workspace"
      }
    }
  },
  {
    "kind": "session/update",
    "event_id": "event-assistant",
    "session_id": "sess-proj-full",
    "turn_id": "turn-1",
    "occurred_at": "2026-06-28T12:00:01Z",
    "scope": "main",
    "scope_id": "sess-proj-full",
    "final": true,
    "update": {
      "sessionUpdate": "agent_message_chunk",
      "content": {
        "type": "text",
        "text": "I will run tests."
      }
    }
  },
  {
    "kind": "session/update",
    "event_id": "event-tool-call",
    "session_id": "sess-proj-full",
    "turn_id": "turn-1",
    "occurred_at": "2026-06-28T12:00:02Z",
    "scope": "main",
    "scope_id": "sess-proj-full",
    "final": true,
    "update": {
      "sessionUpdate": "tool_call",
      "toolCallId": "call-1",
      "title": "RUN_COMMAND go test ./protocol/acp/projector",
      "kind": "execute",
      "status": "pending",
      "rawInput": {
        "command": "go test ./protocol/acp/projector"
      },
      "content": [
        {
          "type": "terminal",
          "terminalId": "call-1"
        }
      ],
      "_meta": {
        "terminal_info": {
          "terminal_id": "call-1"
        }
      }
    }
  },
  {
    "kind": "session/update",
    "event_id": "event-tool-result",
    "session_id": "sess-proj-full",
    "turn_id": "turn-1",
    "occurred_at": "2026-06-28T12:00:03Z",
    "scope": "main",
    "scope_id": "sess-proj-full",
    "final": true,
    "update": {
      "sessionUpdate": "tool_call_update",
      "toolCallId": "call-1",
      "title": "RUN_COMMAND go test ./protocol/acp/projector",
      "kind": "execute",
      "status": "completed",
      "rawInput": {
        "command": "go test ./protocol/acp/projector"
      },
      "rawOutput": {
        "exit_code": 0
      },
      "content": [
        {
          "type": "terminal",
          "terminalId": "call-1"
        }
      ],
      "_meta": {
        "terminal_exit": {
          "exit_code": 0,
          "signal": null,
          "terminal_id": "call-1"
        },
        "terminal_info": {
          "terminal_id": "call-1"
        },
        "terminal_output": {
          "data": "ok\n",
          "terminal_id": "call-1"
        }
      }
    }
  },
  {
    "kind": "session/update",
    "event_id": "event-plan",
    "session_id": "sess-proj-full",
    "turn_id": "turn-1",
    "occurred_at": "2026-06-28T12:00:04Z",
    "scope": "main",
    "scope_id": "sess-proj-full",
    "final": true,
    "update": {
      "sessionUpdate": "plan",
      "entries": [
        {
          "content": "Run focused tests",
          "status": "completed",
          "priority": "medium"
        },
        {
          "content": "Update GUI protocol",
          "status": "in_progress",
          "priority": "high"
        }
      ]
    }
  },
  {
    "kind": "session/request_permission",
    "event_id": "event-permission",
    "session_id": "sess-proj-full",
    "turn_id": "turn-1",
    "occurred_at": "2026-06-28T12:00:05Z",
    "scope": "main",
    "scope_id": "sess-proj-full",
    "final": true,
    "permission": {
      "sessionId": "sess-proj-full",
      "toolCall": {
        "sessionUpdate": "tool_call_update",
        "toolCallId": "call-2",
        "title": "RUN_COMMAND make arch-lint",
        "kind": "execute",
        "status": "pending",
        "rawInput": {
          "command": "make arch-lint"
        },
        "content": [
          {
            "type": "terminal",
            "terminalId": "call-2"
          }
        ],
        "_meta": {
          "caelis": {
            "runtime": {
              "tool": {
                "name": "RUN_COMMAND"
              }
            },
            "version": 1
          },
          "terminal_info": {
            "terminal_id": "call-2"
          }
        }
      },
      "options": [
        {
          "optionId": "allow_once",
          "name": "Allow once",
          "kind": "allow_once"
        }
      ]
    }
  },
  {
    "kind": "caelis/participant",
    "event_id": "event-participant",
    "session_id": "sess-proj-full",
    "turn_id": "turn-1",
    "occurred_at": "2026-06-28T12:00:06Z",
    "scope": "participant",
    "scope_id": "turn-1",
    "actor": "@reviewer",
    "participant_id": "participant-1",
    "final": true,
    "participant": {
      "state": "attached"
    }
  }
]`
	if got != want {
		t.Fatalf("projection full envelope golden mismatch\nwant:\n%s\n\ngot:\n%s", want, got)
	}
}

func projectRunEvents(ref session.SessionRef, events []*session.Event) []eventstream.Envelope {
	out := make([]eventstream.Envelope, 0, len(events))
	for _, event := range events {
		base := eventstream.Envelope{
			EventID:    event.ID,
			SessionID:  ref.SessionID,
			TurnID:     eventTurnID(event),
			OccurredAt: event.Time,
			Final:      event.Visibility != session.VisibilityUIOnly,
			Scope:      eventstream.ScopeMain,
			ScopeID:    ref.SessionID,
		}
		if actor := eventActor(event); actor != "" {
			base.Actor = actor
		}
		if participantID, participantScope := eventParticipantScope(event); participantID != "" {
			base.ParticipantID = participantID
			base.Scope = participantScope
			base.ScopeID = participantScopeID(ref, event, participantID)
		}
		out = append(out, acpprojector.ProjectSessionEventEnvelope(base, event)...)
	}
	return out
}

func eventTurnID(event *session.Event) string {
	if event == nil || event.Scope == nil {
		return ""
	}
	return event.Scope.TurnID
}

func eventActor(event *session.Event) string {
	if event == nil {
		return ""
	}
	if event.Actor.Name != "" {
		return event.Actor.Name
	}
	return event.Actor.ID
}

func eventParticipantScope(event *session.Event) (string, eventstream.Scope) {
	if event == nil || event.Scope == nil || event.Scope.Participant.ID == "" {
		return "", eventstream.ScopeMain
	}
	if event.Scope.Participant.Kind == session.ParticipantKindSubagent {
		return event.Scope.Participant.ID, eventstream.ScopeSubagent
	}
	return event.Scope.Participant.ID, eventstream.ScopeParticipant
}

func participantScopeID(ref session.SessionRef, event *session.Event, participantID string) string {
	if event == nil || event.Scope == nil {
		return ref.SessionID
	}
	if event.Scope.TurnID != "" {
		return event.Scope.TurnID
	}
	if event.Scope.ACP.SessionID != "" {
		return event.Scope.ACP.SessionID
	}
	if participantID != "" {
		return participantID
	}
	return ref.SessionID
}

func projectionGoldenFixtureEvents() []*session.Event {
	at := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	user := model.NewTextMessage(model.RoleUser, "inspect the workspace")
	assistant := model.NewTextMessage(model.RoleAssistant, "I will run tests.")
	return []*session.Event{
		{
			ID:         "event-user",
			SessionID:  "sess-proj-full",
			Type:       session.EventTypeUser,
			Visibility: session.VisibilityCanonical,
			Time:       at,
			Message:    &user,
			Scope:      &session.EventScope{TurnID: "turn-1"},
		},
		{
			ID:         "event-assistant",
			SessionID:  "sess-proj-full",
			Type:       session.EventTypeAssistant,
			Visibility: session.VisibilityCanonical,
			Time:       at.Add(time.Second),
			Message:    &assistant,
			Scope:      &session.EventScope{TurnID: "turn-1"},
		},
		{
			ID:         "event-tool-call",
			SessionID:  "sess-proj-full",
			Type:       session.EventTypeToolCall,
			Visibility: session.VisibilityCanonical,
			Time:       at.Add(2 * time.Second),
			Tool: &session.EventTool{
				ID:     "call-1",
				Name:   "RUN_COMMAND",
				Status: "pending",
				Input:  map[string]any{"command": "go test ./protocol/acp/projector"},
			},
			Scope: &session.EventScope{TurnID: "turn-1"},
		},
		{
			ID:         "event-tool-result",
			SessionID:  "sess-proj-full",
			Type:       session.EventTypeToolResult,
			Visibility: session.VisibilityCanonical,
			Time:       at.Add(3 * time.Second),
			Tool: &session.EventTool{
				ID:     "call-1",
				Name:   "RUN_COMMAND",
				Status: "completed",
				Input:  map[string]any{"command": "go test ./protocol/acp/projector"},
				Output: map[string]any{"exit_code": 0},
				Content: []session.EventToolContent{{
					Type:       "terminal",
					Text:       "ok\n",
					TerminalID: "terminal-1",
				}},
			},
			Scope: &session.EventScope{TurnID: "turn-1"},
		},
		{
			ID:         "event-plan",
			SessionID:  "sess-proj-full",
			Type:       session.EventTypePlan,
			Visibility: session.VisibilityCanonical,
			Time:       at.Add(4 * time.Second),
			PlanPayload: &session.EventPlanPayload{Entries: []session.EventPlanEntry{{
				Content:  "Run focused tests",
				Status:   "completed",
				Priority: "medium",
			}, {
				Content:  "Update GUI protocol",
				Status:   "in_progress",
				Priority: "high",
			}}},
			Scope: &session.EventScope{TurnID: "turn-1"},
		},
		{
			ID:         "event-permission",
			SessionID:  "sess-proj-full",
			Type:       session.EventTypeLifecycle,
			Visibility: session.VisibilityCanonical,
			Time:       at.Add(5 * time.Second),
			Protocol: &session.EventProtocol{
				Method: session.ProtocolMethodRequestPermission,
				Permission: &session.ProtocolApproval{
					ToolCall: session.ProtocolToolCall{
						ID:       "call-2",
						Name:     "RUN_COMMAND",
						Status:   "pending",
						RawInput: map[string]any{"command": "make arch-lint"},
					},
					Options: []session.ProtocolApprovalOption{{
						ID:   "allow_once",
						Name: "Allow once",
						Kind: "allow_once",
					}},
				},
			},
			Scope: &session.EventScope{TurnID: "turn-1"},
		},
		{
			ID:         "event-participant",
			SessionID:  "sess-proj-full",
			Type:       session.EventTypeParticipant,
			Visibility: session.VisibilityMirror,
			Time:       at.Add(6 * time.Second),
			Actor:      session.ActorRef{Name: "@reviewer"},
			Protocol: &session.EventProtocol{
				Method: session.ProtocolMethodParticipantUpdate,
				Update: &session.ProtocolUpdate{SessionUpdate: "attached"},
			},
			Scope: &session.EventScope{
				TurnID:      "turn-1",
				Participant: session.ParticipantRef{ID: "participant-1", Kind: session.ParticipantKindACP},
				ACP:         session.ACPRef{SessionID: "participant-session-1"},
			},
		},
	}
}

func stringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
