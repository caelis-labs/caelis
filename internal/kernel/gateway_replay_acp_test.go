package kernel

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestReplayEventsMatchesLiveACPProjectionSemantics(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
	}
	assistant := model.NewMessage(
		model.RoleAssistant,
		model.NewReasoningPart("thinking", model.ReasoningVisibilityVisible),
		model.NewTextPart("done"),
	)
	toolCall := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
		ID:   "call-1",
		Name: "RUN_COMMAND",
		Args: `{"command":"pwd"}`,
	}}, "")
	events := []*session.Event{
		{
			ID:         "assistant-1",
			SessionID:  "s1",
			Type:       session.EventTypeAssistant,
			Visibility: session.VisibilityCanonical,
			Time:       time.Unix(10, 0),
			Message:    &assistant,
			Scope:      &session.EventScope{Source: "acp", TurnID: "turn-1"},
			Meta: map[string]any{
				"usage": map[string]any{
					"prompt_tokens":       12,
					"cached_input_tokens": 3,
					"completion_tokens":   5,
					"reasoning_tokens":    2,
					"total_tokens":        17,
				},
			},
		},
		{
			ID:         "tool-call-1",
			SessionID:  "s1",
			Type:       session.EventTypeToolCall,
			Visibility: session.VisibilityCanonical,
			Time:       time.Unix(11, 0),
			Message:    &toolCall,
			Scope:      &session.EventScope{Source: "acp", TurnID: "turn-1"},
			Tool: &session.EventTool{
				ID:     "call-1",
				Name:   "RUN_COMMAND",
				Kind:   schema.ToolKindExecute,
				Title:  "pwd",
				Status: "pending",
				Input:  map[string]any{"command": "pwd"},
			},
		},
		{
			ID:         "tool-result-1",
			SessionID:  "s1",
			Type:       session.EventTypeToolResult,
			Visibility: session.VisibilityCanonical,
			Time:       time.Unix(12, 0),
			Scope:      &session.EventScope{Source: "acp", TurnID: "turn-1"},
			Tool: &session.EventTool{
				ID:     "call-1",
				Name:   "RUN_COMMAND",
				Kind:   schema.ToolKindExecute,
				Title:  "pwd",
				Status: "completed",
				Input:  map[string]any{"command": "pwd"},
				Output: map[string]any{"stdout": "/tmp\n"},
			},
		},
		{
			ID:         "plan-1",
			SessionID:  "s1",
			Type:       session.EventTypePlan,
			Visibility: session.VisibilityCanonical,
			Time:       time.Unix(13, 0),
			Scope:      &session.EventScope{Source: "acp", TurnID: "turn-1"},
			PlanPayload: &session.EventPlanPayload{Entries: []session.EventPlanEntry{{
				Content:  "Run tests",
				Status:   "completed",
				Priority: "high",
			}}},
		},
		{
			ID:         "participant-1",
			SessionID:  "s1",
			Type:       session.EventTypeParticipant,
			Visibility: session.VisibilityCanonical,
			Time:       time.Unix(14, 0),
			Actor:      session.ActorRef{Kind: session.ActorKindParticipant, ID: "agent-1", Name: "@agent"},
			Scope: &session.EventScope{
				Source:      "acp_participant",
				TurnID:      "turn-1",
				Participant: session.ParticipantRef{ID: "agent-1", Kind: session.ParticipantKindACP, Role: session.ParticipantRoleSidecar},
			},
			Protocol: &session.EventProtocol{
				Participant: &session.ProtocolParticipant{Action: "attached"},
			},
		},
		{
			ID:         "lifecycle-1",
			SessionID:  "s1",
			Type:       session.EventTypeLifecycle,
			Visibility: session.VisibilityCanonical,
			Time:       time.Unix(15, 0),
			Actor:      session.ActorRef{Kind: session.ActorKindController, Name: "codex"},
			Scope:      &session.EventScope{Source: "acp", TurnID: "turn-1"},
			Lifecycle:  &session.EventLifecycle{Status: "completed", Reason: "done"},
		},
	}

	handle := newTurnHandle(turnHandleConfig{
		handleID:   "h1",
		runID:      "run-1",
		turnID:     "turn-1",
		sessionRef: activeSession.SessionRef,
		createdAt:  time.Unix(9, 0),
	})
	liveCh := handle.ACPEvents()
	for _, event := range events {
		handle.publishSessionEvent(event)
	}
	handle.finish()
	var live []eventstream.Envelope
	for env := range liveCh {
		live = append(live, env)
	}

	gw, err := New(Config{
		Sessions: &recordingSessionService{
			sessionResult: activeSession,
			eventsResult:  events,
		},
		Runtime:  mockRuntime{},
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	replayed, err := gw.ReplayEvents(context.Background(), ReplayEventsRequest{
		SessionRef:       activeSession.SessionRef,
		IncludeTransient: true,
	})
	if err != nil {
		t.Fatalf("ReplayEvents() error = %v", err)
	}

	got := semanticACPEnvelopes(replayed.Events)
	want := semanticACPEnvelopes(live)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReplayEvents semantic envelopes differ from live ACPEvents\ngot:  %#v\nwant: %#v", got, want)
	}
}

type semanticACPEnvelope struct {
	Kind          eventstream.Kind
	Cursor        string
	SessionID     string
	TurnID        string
	Scope         eventstream.Scope
	ScopeID       string
	Actor         string
	ParticipantID string
	Final         bool
	Update        semanticACPUpdate
	Usage         *eventstream.UsageSnapshot
	Participant   *eventstream.Participant
	Lifecycle     *eventstream.Lifecycle
	Meta          map[string]any
}

type semanticACPUpdate struct {
	Type       string
	Content    string
	MessageID  string
	ToolCallID string
	Kind       string
	Title      string
	Status     string
	RawInput   map[string]any
	RawOutput  map[string]any
	Entries    []schema.PlanEntry
	Meta       map[string]any
}

func semanticACPEnvelopes(events []eventstream.Envelope) []semanticACPEnvelope {
	out := make([]semanticACPEnvelope, 0, len(events))
	for _, env := range events {
		next := semanticACPEnvelope{
			Kind:          env.Kind,
			Cursor:        env.Cursor,
			SessionID:     env.SessionID,
			TurnID:        env.TurnID,
			Scope:         env.Scope,
			ScopeID:       env.ScopeID,
			Actor:         env.Actor,
			ParticipantID: env.ParticipantID,
			Final:         env.Final,
			Update:        semanticACPUpdateOf(env.Update),
			Usage:         env.Usage,
			Participant:   env.Participant,
			Lifecycle:     env.Lifecycle,
			Meta:          semanticEnvelopeMeta(env.Meta),
		}
		out = append(out, next)
	}
	return out
}

func semanticEnvelopeMeta(meta map[string]any) map[string]any {
	if len(meta) == 0 {
		return nil
	}
	out := make(map[string]any, len(meta))
	for key, value := range meta {
		if key != "caelis" {
			out[key] = value
			continue
		}
		caelis, ok := value.(map[string]any)
		if !ok {
			out[key] = value
			continue
		}
		cleaned := make(map[string]any, len(caelis))
		for caKey, caValue := range caelis {
			switch caKey {
			case "version", "bridge":
				continue
			default:
				cleaned[caKey] = caValue
			}
		}
		if len(cleaned) > 0 {
			out[key] = cleaned
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func semanticACPUpdateOf(update schema.Update) semanticACPUpdate {
	out := semanticACPUpdate{Type: eventstream.UpdateType(update)}
	switch typed := update.(type) {
	case schema.ContentChunk:
		out.Content = schema.ExtractTextValue(typed.Content)
		out.MessageID = typed.MessageID
		out.Meta = typed.Meta
	case schema.ToolCall:
		out.ToolCallID = typed.ToolCallID
		out.Kind = typed.Kind
		out.Title = typed.Title
		out.Status = typed.Status
		if raw, ok := typed.RawInput.(map[string]any); ok {
			out.RawInput = raw
		}
		if raw, ok := typed.RawOutput.(map[string]any); ok {
			out.RawOutput = raw
		}
		out.Meta = typed.Meta
	case schema.ToolCallUpdate:
		out.ToolCallID = typed.ToolCallID
		out.Kind = testDerefString(typed.Kind)
		out.Title = testDerefString(typed.Title)
		out.Status = testDerefString(typed.Status)
		if raw, ok := typed.RawInput.(map[string]any); ok {
			out.RawInput = raw
		}
		if raw, ok := typed.RawOutput.(map[string]any); ok {
			out.RawOutput = raw
		}
		out.Meta = typed.Meta
	case schema.PlanUpdate:
		out.Entries = typed.Entries
	case schema.UsageUpdate:
		out.RawInput = map[string]any{"size": typed.Size, "used": typed.Used}
		out.Meta = typed.Meta
	}
	return out
}

func testDerefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
