package acpbridge

import (
	"context"
	"iter"
	"reflect"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestForwardControllerEventsPublishesPersistedCanonicalWhenACPPresent(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-acp-forward-bundled")
	forwarder := NewControllerForwarder(sessions)
	publisher := newTestPublisher()
	message := model.NewTextMessage(model.RoleAssistant, "done")
	source := scriptedSourceHandle{events: []SourceEvent{{
		Canonical: &session.Event{
			Type:       session.EventTypeAssistant,
			Visibility: session.VisibilityCanonical,
			Text:       "done",
			Message:    &message,
		},
		ACP: &eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate,
			Update: schema.RawUpdate{
				SessionUpdate: "vendor/custom",
				Raw:           []byte(`{"sessionUpdate":"vendor/custom","value":1}`),
			},
		},
	}}}

	if err := forwarder.ForwardControllerEvents(context.Background(), agent.ControllerEventForwardRequest{
		ActiveSession: activeSession,
		SessionRef:    activeSession.SessionRef,
		TurnID:        "turn-1",
		Source:        source,
		Publisher:     publisher,
	}); err != nil {
		t.Fatalf("ForwardControllerEvents() error = %v", err)
	}
	publisher.finish()

	var canonicalCount int
	var acpCount int
	for event, err := range publisher.SourceEvents() {
		if err != nil {
			t.Fatalf("publisher source error = %v", err)
		}
		if event.Canonical != nil && session.EventTypeOf(event.Canonical) == session.EventTypeAssistant && event.Canonical.ID != "" {
			canonicalCount++
		}
		if event.Native != nil {
			acpCount++
		}
	}
	if canonicalCount != 1 {
		t.Fatalf("publisher source canonical events = %d, want persisted assistant event", canonicalCount)
	}
	if acpCount != 1 {
		t.Fatalf("publisher ACP passthrough events = %d, want 1", acpCount)
	}
	stored, err := sessions.Events(context.Background(), session.EventsRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(stored) != 1 || session.EventTypeOf(stored[0]) != session.EventTypeAssistant {
		t.Fatalf("stored events = %#v, want persisted assistant", stored)
	}
}

func TestForwardControllerEventsRequiresDependencies(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-acp-forward-dependencies")
	source := scriptedSourceHandle{}

	if err := (*ControllerForwarder)(nil).ForwardControllerEvents(context.Background(), agent.ControllerEventForwardRequest{
		ActiveSession: activeSession,
		SessionRef:    activeSession.SessionRef,
		Source:        source,
		Publisher:     newTestPublisher(),
	}); err == nil {
		t.Fatal("nil forwarder ForwardControllerEvents() error = nil, want dependency error")
	}
	if err := NewControllerForwarder(nil).ForwardControllerEvents(context.Background(), agent.ControllerEventForwardRequest{
		ActiveSession: activeSession,
		SessionRef:    activeSession.SessionRef,
		Source:        source,
		Publisher:     newTestPublisher(),
	}); err == nil {
		t.Fatal("nil session service ForwardControllerEvents() error = nil, want dependency error")
	}
	if err := NewControllerForwarder(sessions).ForwardControllerEvents(context.Background(), agent.ControllerEventForwardRequest{
		ActiveSession: activeSession,
		SessionRef:    activeSession.SessionRef,
		Source:        source,
	}); err == nil {
		t.Fatal("nil publisher ForwardControllerEvents() error = nil, want dependency error")
	}
}

func TestForwardControllerEventsPublishesNarrativeDeltasWithRepairedNativeACP(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-acp-forward-narrative-delta")
	forwarder := NewControllerForwarder(sessions)
	publisher := newTestPublisher()
	source := scriptedSourceHandle{events: []SourceEvent{
		{
			Canonical: acpNarrativeEvent(session.ProtocolUpdateTypeAgentMessage, "hel"),
			ACP: acpNarrativeEnvelope("hel", map[string]any{
				"vendor": "acp-test",
			}),
		},
		{
			Canonical: acpNarrativeEvent(session.ProtocolUpdateTypeAgentMessage, "hello"),
			ACP: acpNarrativeEnvelope("hello", map[string]any{
				"vendor": "acp-test",
			}),
		},
		{
			Canonical: acpNarrativeEvent(session.ProtocolUpdateTypeAgentMessage, "hello"),
			ACP: acpNarrativeEnvelope("hello", map[string]any{
				"vendor": "acp-test",
			}),
		},
	}}

	if err := forwarder.ForwardControllerEvents(context.Background(), agent.ControllerEventForwardRequest{
		ActiveSession: activeSession,
		SessionRef:    activeSession.SessionRef,
		TurnID:        "turn-1",
		Source:        source,
		Publisher:     publisher,
		IsUserEcho:    isControllerUserEcho,
	}); err != nil {
		t.Fatalf("ForwardControllerEvents() error = %v", err)
	}
	publisher.finish()

	var liveTexts []string
	var nativeTexts []string
	for event, err := range publisher.SourceEvents() {
		if err != nil {
			t.Fatalf("publisher source error = %v", err)
		}
		if event.Canonical != nil && event.Canonical.Visibility == session.VisibilityUIOnly {
			liveTexts = append(liveTexts, session.EventText(event.Canonical))
		}
		if env, ok := event.Native.(*eventstream.Envelope); ok && env != nil {
			chunk, ok := env.Update.(schema.ContentChunk)
			if !ok {
				t.Fatalf("native ACP update = %#v, want ContentChunk", env.Update)
			}
			nativeTexts = append(nativeTexts, schema.ExtractTextValue(chunk.Content))
			if env.Scope != eventstream.ScopeParticipant || env.ScopeID != "emma" {
				t.Fatalf("native ACP scope = %q/%q, want preserved participant/emma", env.Scope, env.ScopeID)
			}
			if got, want := env.Meta["vendor"], "acp-test"; got != want {
				t.Fatalf("native ACP meta vendor = %#v, want %#v", got, want)
			}
		}
	}
	if want := []string{"hel", "lo"}; !reflect.DeepEqual(liveTexts, want) {
		t.Fatalf("live canonical deltas = %#v, want %#v", liveTexts, want)
	}
	if want := []string{"hel", "lo"}; !reflect.DeepEqual(nativeTexts, want) {
		t.Fatalf("native ACP deltas = %#v, want %#v", nativeTexts, want)
	}

	stored, err := sessions.Events(context.Background(), session.EventsRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(stored) != 1 || session.EventText(stored[0]) != "hello" {
		t.Fatalf("stored events = %#v, want final canonical assistant hello", stored)
	}
	if stored[0].Visibility != session.VisibilityCanonical {
		t.Fatalf("stored assistant visibility = %q, want canonical", stored[0].Visibility)
	}
}

func TestForwardControllerEventsSuppressesACPControllerUserEcho(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-acp-forward-user-echo")
	forwarder := NewControllerForwarder(sessions)
	publisher := newTestPublisher()
	message := model.NewTextMessage(model.RoleUser, "echoed prompt")
	source := scriptedSourceHandle{events: []SourceEvent{{
		Canonical: &session.Event{
			Type:       session.EventTypeUser,
			Visibility: session.VisibilityCanonical,
			Text:       "echoed prompt",
			Message:    &message,
			Scope: &session.EventScope{
				Source: "acp",
			},
			Protocol: &session.EventProtocol{
				Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeUserMessage),
				},
			},
		},
		ACP: &eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateUserMessage,
				Content:       schema.TextContent{Type: "text", Text: "echoed prompt"},
			},
		},
	}}}

	if err := forwarder.ForwardControllerEvents(context.Background(), agent.ControllerEventForwardRequest{
		ActiveSession: activeSession,
		SessionRef:    activeSession.SessionRef,
		TurnID:        "turn-1",
		Source:        source,
		Publisher:     publisher,
		IsUserEcho:    isControllerUserEcho,
	}); err != nil {
		t.Fatalf("ForwardControllerEvents() error = %v", err)
	}
	publisher.finish()

	for event, err := range publisher.SourceEvents() {
		if err != nil {
			t.Fatalf("publisher source error = %v", err)
		}
		if event.Canonical != nil || event.Native != nil {
			t.Fatalf("publisher source event = %#v, want user echo suppressed", event)
		}
	}
	for event, err := range publisher.Events() {
		if err != nil {
			t.Fatalf("publisher event error = %v", err)
		}
		if event != nil {
			t.Fatalf("publisher legacy event = %#v, want user echo suppressed", event)
		}
	}
	stored, err := sessions.Events(context.Background(), session.EventsRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("stored events = %#v, want no persisted user echo", stored)
	}
}

func TestForwardControllerEventsMaterializesFinalAssistantAfterThoughtBarrier(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-acp-forward-final-thought")
	forwarder := NewControllerForwarder(sessions)
	publisher := newTestPublisher()
	source := scriptedSourceHandle{events: []SourceEvent{
		{Canonical: acpNarrativeEvent(session.ProtocolUpdateTypeAgentMessage, "draft")},
		{Canonical: acpNarrativeEvent(session.ProtocolUpdateTypeAgentThought, "thinking")},
		{Canonical: acpNarrativeEvent(session.ProtocolUpdateTypeAgentMessage, "final")},
	}}

	if err := forwarder.ForwardControllerEvents(context.Background(), agent.ControllerEventForwardRequest{
		ActiveSession: activeSession,
		SessionRef:    activeSession.SessionRef,
		TurnID:        "turn-1",
		Source:        source,
		Publisher:     publisher,
		IsUserEcho:    isControllerUserEcho,
	}); err != nil {
		t.Fatalf("ForwardControllerEvents() error = %v", err)
	}
	publisher.finish()

	var persistedAssistants []string
	for event, err := range publisher.Events() {
		if err != nil {
			t.Fatalf("publisher event error = %v", err)
		}
		if event != nil && session.EventTypeOf(event) == session.EventTypeAssistant && event.Visibility == session.VisibilityCanonical {
			persistedAssistants = append(persistedAssistants, session.EventText(event))
		}
	}
	if want := []string{"final"}; !reflect.DeepEqual(persistedAssistants, want) {
		t.Fatalf("persisted assistant texts = %#v, want %#v", persistedAssistants, want)
	}
}

func TestForwardControllerEventsPreservesLegacyUIOnlyWhenACPPresent(t *testing.T) {
	t.Parallel()

	for _, consumeSource := range []bool{false, true} {
		t.Run(map[bool]string{false: "events", true: "source"}[consumeSource], func(t *testing.T) {
			t.Parallel()
			sessions, activeSession := newTestSessionService(t, "sess-acp-forward-ui-only")
			forwarder := NewControllerForwarder(sessions)
			publisher := newTestPublisher()
			source := scriptedSourceHandle{events: []SourceEvent{{
				Canonical: &session.Event{
					Type:       session.EventTypeToolCall,
					Visibility: session.VisibilityUIOnly,
					Text:       "external search",
					Protocol: &session.EventProtocol{
						Update: &session.ProtocolUpdate{
							SessionUpdate: string(session.ProtocolUpdateTypeToolCall),
							ToolCallID:    "call-1",
							Kind:          "Search",
							Status:        "in_progress",
						},
					},
				},
				ACP: &eventstream.Envelope{
					Kind: eventstream.KindSessionUpdate,
					Update: schema.ToolCall{
						SessionUpdate: schema.UpdateToolCall,
						ToolCallID:    "call-1",
						Title:         "Search",
						Status:        schema.ToolStatusInProgress,
					},
				},
			}}}

			if err := forwarder.ForwardControllerEvents(context.Background(), agent.ControllerEventForwardRequest{
				ActiveSession: activeSession,
				SessionRef:    activeSession.SessionRef,
				TurnID:        "turn-1",
				Source:        source,
				Publisher:     publisher,
			}); err != nil {
				t.Fatalf("ForwardControllerEvents() error = %v", err)
			}
			publisher.finish()

			if consumeSource {
				var acpCount int
				for event, err := range publisher.SourceEvents() {
					if err != nil {
						t.Fatalf("publisher source error = %v", err)
					}
					if event.Native != nil {
						acpCount++
					}
				}
				if acpCount != 1 {
					t.Fatalf("publisher source ACP events = %d, want 1", acpCount)
				}
				return
			}

			var toolCount int
			for event, err := range publisher.Events() {
				if err != nil {
					t.Fatalf("publisher event error = %v", err)
				}
				if event != nil && session.EventTypeOf(event) == session.EventTypeToolCall {
					toolCount++
				}
			}
			if toolCount != 1 {
				t.Fatalf("publisher legacy tool events = %d, want 1", toolCount)
			}
		})
	}
}

func isControllerUserEcho(event *session.Event) bool {
	if event == nil || event.Scope == nil {
		return false
	}
	if session.EventTypeOf(event) != session.EventTypeUser {
		return false
	}
	if event.Scope.Participant.ID != "" {
		return false
	}
	return event.Scope.Source == "acp"
}

type scriptedSourceHandle struct {
	events []SourceEvent
}

func (h scriptedSourceHandle) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		for _, event := range h.events {
			if !yield(event.Canonical, nil) {
				return
			}
		}
	}
}

func (h scriptedSourceHandle) SourceEvents() iter.Seq2[SourceEvent, error] {
	return func(yield func(SourceEvent, error) bool) {
		for _, event := range h.events {
			if !yield(CloneSourceEvent(event), nil) {
				return
			}
		}
	}
}

func (scriptedSourceHandle) Cancel() controller.CancelResult {
	return controller.CancelResult{Status: controller.CancelStatusCancelled}
}

func (scriptedSourceHandle) Close() error { return nil }

func acpNarrativeEnvelope(text string, meta map[string]any) *eventstream.Envelope {
	return &eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "emma",
		ParticipantID: "emma",
		Meta:          meta,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: text},
		},
	}
}

func newTestSessionService(t *testing.T, sessionID string) (session.Service, session.Session) {
	t.Helper()
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{
		SessionIDGenerator: func() string { return sessionID },
	}))
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-1",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	return sessions, activeSession
}

type testPublisher struct {
	events []agent.SourceEvent
	closed bool
}

func newTestPublisher() *testPublisher {
	return &testPublisher{}
}

func (p *testPublisher) PublishEvent(event *session.Event) {
	if p == nil || event == nil {
		return
	}
	p.events = append(p.events, agent.SourceEvent{Canonical: session.CloneEvent(event)})
}

func (p *testPublisher) PublishSourceEvent(event agent.SourceEvent) {
	if p == nil || (event.Canonical == nil && event.Native == nil) {
		return
	}
	p.events = append(p.events, agent.CloneSourceEvent(event))
}

func (p *testPublisher) finish() {
	p.closed = true
}

func (p *testPublisher) SourceEvents() iter.Seq2[agent.SourceEvent, error] {
	return func(yield func(agent.SourceEvent, error) bool) {
		for _, event := range p.events {
			if !yield(event, nil) {
				return
			}
		}
	}
}

func (p *testPublisher) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		for _, event := range p.events {
			if !yield(event.Canonical, nil) {
				return
			}
		}
	}
}
