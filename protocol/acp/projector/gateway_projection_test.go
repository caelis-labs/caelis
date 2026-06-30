package projector

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/metautil"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestProjectSessionEventEnvelopeProjectsToolUpdate(t *testing.T) {
	events := ProjectSessionEventEnvelope(eventstream.Envelope{
		SessionID: "session-1",
		Scope:     eventstream.ScopeMain,
		ScopeID:   "session-1",
	}, &session.Event{
		ID:        "event-1",
		SessionID: "session-1",
		Type:      session.EventTypeToolResult,
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodSessionUpdate,
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
				ToolCallID:    "call-1",
				Kind:          "RUN_COMMAND",
				Title:         "RUN_COMMAND",
				Status:        "running",
				RawInput:      map[string]any{"command": "echo ok"},
				Content: []session.ProtocolToolCallContent{{
					Type:       "terminal",
					TerminalID: "terminal-1",
					Content:    session.ProtocolTextContent("ok\n"),
				}},
			},
		},
	})
	if len(events) != 1 {
		t.Fatalf("ProjectSessionEventEnvelope() returned %d events, want 1: %#v", len(events), events)
	}
	update, ok := events[0].Update.(schema.ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %#v, want ToolCallUpdate", events[0].Update)
	}
	if update.ToolCallID != "call-1" || stringPtrValue(update.Kind) != "RUN_COMMAND" || stringPtrValue(update.Status) != schema.ToolStatusInProgress {
		t.Fatalf("tool update = %#v, want RUN_COMMAND in_progress call-1", update)
	}
	assertTerminalAnchor(t, update.Content, "call-1")
	if info, ok := metautil.TerminalInfo(update.Meta); !ok || info.TerminalID != "call-1" {
		t.Fatalf("terminal_info = %#v, want call-1", update.Meta)
	}
	if output, ok := metautil.TerminalOutput(update.Meta); !ok || output.TerminalID != "call-1" || output.Data != "ok\n" {
		t.Fatalf("terminal_output = %#v, want ok output", update.Meta)
	}
}

func TestProjectSessionEventEnvelopeProjectsPermission(t *testing.T) {
	events := ProjectSessionEventEnvelope(eventstream.Envelope{
		SessionID: "session-1",
	}, &session.Event{
		ID:        "permission-1",
		SessionID: "session-1",
		Type:      session.EventTypeLifecycle,
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodRequestPermission,
			Permission: &session.ProtocolApproval{
				ToolCall: session.ProtocolToolCall{
					ID:       "call-1",
					Name:     "RUN_COMMAND",
					Kind:     "RUN_COMMAND",
					Title:    "RUN_COMMAND",
					Status:   "pending",
					RawInput: map[string]any{"command": "go test ./..."},
				},
				Options: []session.ProtocolApprovalOption{{
					ID:   "allow_once",
					Name: "Allow once",
					Kind: "allow_once",
				}},
			},
		},
	})
	if len(events) != 1 || events[0].Kind != eventstream.KindRequestPermission || events[0].Permission == nil {
		t.Fatalf("permission projection = %#v, want request_permission", events)
	}
	permission := events[0].Permission
	if permission.ToolCall.ToolCallID != "call-1" || stringPtrValue(permission.ToolCall.Kind) != "RUN_COMMAND" {
		t.Fatalf("permission tool call = %#v, want RUN_COMMAND call-1", permission.ToolCall)
	}
	if len(permission.Options) != 1 || permission.Options[0].OptionID != "allow_once" {
		t.Fatalf("permission options = %#v, want allow_once", permission.Options)
	}
}

func TestProjectSessionEventEnvelopeProjectsUsageAsACPUsageUpdate(t *testing.T) {
	events := ProjectSessionEventEnvelope(eventstream.Envelope{
		SessionID: "session-1",
	}, &session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeAssistant,
		Meta: map[string]any{
			"usage": map[string]any{
				"prompt_tokens":       12,
				"cached_input_tokens": 3,
				"completion_tokens":   5,
				"reasoning_tokens":    2,
				"total_tokens":        17,
			},
		},
	})
	if len(events) != 1 {
		t.Fatalf("ProjectSessionEventEnvelope() returned %d events, want usage update: %#v", len(events), events)
	}
	update, ok := events[0].Update.(schema.UsageUpdate)
	if !ok {
		t.Fatalf("update = %#v, want UsageUpdate", events[0].Update)
	}
	usage := eventstream.UsageSnapshotFromUpdate(update)
	if usage == nil || usage.PromptTokens != 12 || usage.CachedInputTokens != 3 || usage.CompletionTokens != 5 || usage.ReasoningTokens != 2 || usage.TotalTokens != 17 {
		t.Fatalf("usage snapshot = %#v", usage)
	}
}

func TestProjectSessionEventEnvelopeKeepsUserMessagesForGatewayConsumers(t *testing.T) {
	user := model.NewTextMessage(model.RoleUser, "hello")
	events := ProjectSessionEventEnvelope(eventstream.Envelope{
		SessionID: "session-1",
		HandleID:  "handle-1",
		RunID:     "run-1",
		Scope:     eventstream.ScopeMain,
		ScopeID:   "session-1",
	}, &session.Event{
		ID:        "event-user-1",
		SessionID: "session-1",
		Type:      session.EventTypeUser,
		Text:      "hello",
		Message:   &user,
	})
	if len(events) != 1 {
		t.Fatalf("ProjectSessionEventEnvelope(user) returned %d events, want 1: %#v", len(events), events)
	}
	chunk, ok := events[0].Update.(schema.ContentChunk)
	if !ok || chunk.SessionUpdate != schema.UpdateUserMessage {
		t.Fatalf("update = %#v, want user_message_chunk for gateway/TUI consumers", events[0].Update)
	}
	content, ok := chunk.Content.(schema.TextContent)
	if !ok || content.Text != "hello" {
		t.Fatalf("content = %#v, want hello text", chunk.Content)
	}
}

func TestProjectSessionEventEnvelopeUsesUserDisplayTextWhenMessageIsProjected(t *testing.T) {
	modelVisible := model.NewTextMessage(model.RoleUser, "Load and follow the `cmpctl` skill before taking task actions.\n\nUser request:\narchive preflight")
	events := ProjectSessionEventEnvelope(eventstream.Envelope{
		SessionID: "session-1",
		HandleID:  "handle-1",
		RunID:     "run-1",
		Scope:     eventstream.ScopeMain,
		ScopeID:   "session-1",
	}, &session.Event{
		ID:        "event-user-1",
		SessionID: "session-1",
		Type:      session.EventTypeUser,
		Text:      "$cmpctl archive preflight",
		Message:   &modelVisible,
	})
	if len(events) != 1 {
		t.Fatalf("ProjectSessionEventEnvelope(user) returned %d events, want 1: %#v", len(events), events)
	}
	chunk, ok := events[0].Update.(schema.ContentChunk)
	if !ok || chunk.SessionUpdate != schema.UpdateUserMessage {
		t.Fatalf("update = %#v, want user_message_chunk for gateway/TUI consumers", events[0].Update)
	}
	content, ok := chunk.Content.(schema.TextContent)
	if !ok || content.Text != "$cmpctl archive preflight" {
		t.Fatalf("content = %#v, want display text", chunk.Content)
	}
}

func TestProjectSessionEventEnvelopeKeepsLiveAndReplayNarrativeAligned(t *testing.T) {
	message := model.MessageFromAssistantParts("I will run pwd.", "Need inspect cwd.", []model.ToolCall{{
		ID:   "call-1",
		Name: "RUN_COMMAND",
		Args: `{"command":"pwd"}`,
	}})
	event := &session.Event{
		ID:        "event-1",
		SessionID: "session-1",
		Type:      session.EventTypeToolCall,
		Message:   &message,
	}

	live := ProjectSessionEventEnvelope(eventstream.Envelope{
		SessionID: "session-1",
		HandleID:  "handle-1",
		RunID:     "run-1",
		Scope:     eventstream.ScopeMain,
		ScopeID:   "session-1",
	}, event)
	if len(live) != 3 {
		t.Fatalf("live projection produced %d events, want thought, message, and tool call: %#v", len(live), live)
	}
	if eventstream.UpdateType(live[0].Update) != schema.UpdateAgentThought ||
		eventstream.UpdateType(live[1].Update) != schema.UpdateAgentMessage ||
		eventstream.UpdateType(live[2].Update) != schema.UpdateToolCall {
		t.Fatalf("live projection = %#v, want narrative chunks followed by tool call", live)
	}

	replay := ProjectSessionEventEnvelope(eventstream.Envelope{
		SessionID: "session-1",
		Scope:     eventstream.ScopeMain,
		ScopeID:   "session-1",
	}, event)
	if len(replay) != len(live) {
		t.Fatalf("replay projection produced %d events, want %d: %#v", len(replay), len(live), replay)
	}
	for i := range live {
		if eventstream.UpdateType(replay[i].Update) != eventstream.UpdateType(live[i].Update) {
			t.Fatalf("projection[%d] live=%q replay=%q", i, eventstream.UpdateType(live[i].Update), eventstream.UpdateType(replay[i].Update))
		}
	}
}

func TestProjectSessionEventNotificationsPreservesCustomNotificationsAndAppendsUsage(t *testing.T) {
	notifications, err := ProjectSessionEventNotifications(eventstream.Envelope{
		SessionID: "base-session",
	}, &session.Event{
		SessionID: "event-session",
		Type:      session.EventTypeAssistant,
		Meta: map[string]any{
			"usage": map[string]any{
				"prompt_tokens":     3,
				"completion_tokens": 4,
				"total_tokens":      7,
			},
		},
	}, notificationOverrideProjector{})
	if err != nil {
		t.Fatalf("ProjectSessionEventNotifications() error = %v", err)
	}
	if len(notifications) != 2 {
		t.Fatalf("ProjectSessionEventNotifications() produced %d notifications, want custom notification + usage: %#v", len(notifications), notifications)
	}
	chunk, ok := notifications[0].Update.(schema.ContentChunk)
	if !ok || notifications[0].SessionID != "custom-session" || schema.ExtractTextValue(chunk.Content) != "from notifications" {
		t.Fatalf("first notification = %#v, want custom ProjectNotifications output", notifications[0])
	}
	usage, ok := notifications[1].Update.(schema.UsageUpdate)
	if !ok || notifications[1].SessionID != "base-session" || usage.Used != 7 {
		t.Fatalf("usage notification = %#v, want appended usage_update used=7 on base session", notifications[1])
	}
}

func TestProjectSessionEventEnvelopeProjectsParticipantAndLifecycleExtensions(t *testing.T) {
	participant := ProjectSessionEventEnvelope(eventstream.Envelope{
		Cursor:        "participant-1",
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "agent-1",
		ParticipantID: "agent-1",
	}, &session.Event{
		ID:    "participant-1",
		Type:  session.EventTypeParticipant,
		Actor: session.ActorRef{Kind: session.ActorKindParticipant, Name: "@agent"},
		Scope: &session.EventScope{Participant: session.ParticipantRef{ID: "agent-1"}},
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodParticipantUpdate,
			Update: &session.ProtocolUpdate{SessionUpdate: "attached"},
		},
	})
	if len(participant) != 1 || participant[0].Kind != eventstream.KindParticipant || participant[0].Participant == nil || participant[0].Participant.State != "attached" {
		t.Fatalf("participant projection = %#v, want participant attached", participant)
	}
	if participant[0].Actor != "@agent" || participant[0].ParticipantID != "agent-1" {
		t.Fatalf("participant envelope = %#v, want actor and participant id", participant[0])
	}

	lifecycle := ProjectSessionEventEnvelope(eventstream.Envelope{
		Cursor:    "lifecycle-1",
		SessionID: "session-1",
		Scope:     eventstream.ScopeMain,
		Actor:     "codex",
	}, &session.Event{
		ID:        "lifecycle-1",
		Type:      session.EventTypeLifecycle,
		Actor:     session.ActorRef{Kind: session.ActorKindController, Name: "codex"},
		Lifecycle: &session.EventLifecycle{Status: "COMPLETED", Reason: "done"},
	})
	if len(lifecycle) != 1 || lifecycle[0].Kind != eventstream.KindLifecycle || lifecycle[0].Lifecycle == nil || lifecycle[0].Lifecycle.State != "completed" || lifecycle[0].Lifecycle.Reason != "done" {
		t.Fatalf("lifecycle projection = %#v, want lifecycle completed", lifecycle)
	}

	handoff := ProjectSessionEventEnvelope(eventstream.Envelope{
		Cursor:    "handoff-1",
		SessionID: "session-1",
		Scope:     eventstream.ScopeMain,
	}, &session.Event{
		ID:    "handoff-1",
		Type:  session.EventTypeHandoff,
		Actor: session.ActorRef{Kind: session.ActorKindSystem, Name: "runtime"},
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodControllerHandoff,
			Update: &session.ProtocolUpdate{SessionUpdate: "activation"},
		},
	})
	if len(handoff) != 1 || handoff[0].Kind != eventstream.KindLifecycle || handoff[0].Lifecycle == nil || handoff[0].Lifecycle.State != "activation" {
		t.Fatalf("handoff projection = %#v, want lifecycle activation", handoff)
	}
}

type notificationOverrideProjector struct{}

func (notificationOverrideProjector) ProjectEvent(*session.Event) ([]Update, error) {
	return nil, nil
}

func (notificationOverrideProjector) ProjectNotifications(*session.Event) ([]SessionNotification, error) {
	return []SessionNotification{{
		SessionID: "custom-session",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "from notifications"},
		},
	}}, nil
}

func (notificationOverrideProjector) ProjectPermissionRequest(*session.Event) (*RequestPermissionRequest, bool, error) {
	return nil, false, nil
}

func stringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
