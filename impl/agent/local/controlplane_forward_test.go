package local

import (
	"context"
	"iter"
	"testing"

	"github.com/OnslaughtSnail/caelis/internal/acpbridge"
	"github.com/OnslaughtSnail/caelis/ports/controller"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestForwardACPControllerEventsPublishesPersistedCanonicalWhenACPPresent(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-acp-forward-bundled")
	runtime := &Runtime{sessions: sessions}
	runner := newRunner("run-1", func() {})
	message := model.NewTextMessage(model.RoleAssistant, "done")
	source := scriptedSourceHandle{events: []acpbridge.SourceEvent{{
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

	if err := runtime.forwardACPControllerEvents(context.Background(), acpForwardRequest{
		activeSession: activeSession,
		ref:           activeSession.SessionRef,
		turnID:        "turn-1",
		source:        source,
		handle:        runner,
	}); err != nil {
		t.Fatalf("forwardACPControllerEvents() error = %v", err)
	}
	runner.finish()

	var canonicalCount int
	var acpCount int
	for event, err := range runner.SourceEvents() {
		if err != nil {
			t.Fatalf("runner source error = %v", err)
		}
		if event.Canonical != nil && session.EventTypeOf(event.Canonical) == session.EventTypeAssistant && event.Canonical.ID != "" {
			canonicalCount++
		}
		if event.ACP != nil {
			acpCount++
		}
	}
	if canonicalCount != 1 {
		t.Fatalf("runner source canonical events = %d, want persisted assistant event", canonicalCount)
	}
	if acpCount != 1 {
		t.Fatalf("runner ACP passthrough events = %d, want 1", acpCount)
	}
	stored, err := sessions.Events(context.Background(), session.EventsRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(stored) != 1 || session.EventTypeOf(stored[0]) != session.EventTypeAssistant {
		t.Fatalf("stored events = %#v, want persisted assistant", stored)
	}
}

func TestForwardACPControllerEventsPreservesLegacyUIOnlyWhenACPPresent(t *testing.T) {
	t.Parallel()

	for _, consumeSource := range []bool{false, true} {
		t.Run(map[bool]string{false: "events", true: "source"}[consumeSource], func(t *testing.T) {
			t.Parallel()
			sessions, activeSession := newTestSessionService(t, "sess-acp-forward-ui-only")
			runtime := &Runtime{sessions: sessions}
			runner := newRunner("run-1", func() {})
			source := scriptedSourceHandle{events: []acpbridge.SourceEvent{{
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

			if err := runtime.forwardACPControllerEvents(context.Background(), acpForwardRequest{
				activeSession: activeSession,
				ref:           activeSession.SessionRef,
				turnID:        "turn-1",
				source:        source,
				handle:        runner,
			}); err != nil {
				t.Fatalf("forwardACPControllerEvents() error = %v", err)
			}
			runner.finish()

			if consumeSource {
				var acpCount int
				for event, err := range runner.SourceEvents() {
					if err != nil {
						t.Fatalf("runner source error = %v", err)
					}
					if event.ACP != nil {
						acpCount++
					}
				}
				if acpCount != 1 {
					t.Fatalf("runner source ACP events = %d, want 1", acpCount)
				}
				return
			}

			var toolCount int
			for event, err := range runner.Events() {
				if err != nil {
					t.Fatalf("runner event error = %v", err)
				}
				if event != nil && session.EventTypeOf(event) == session.EventTypeToolCall {
					toolCount++
				}
			}
			if toolCount != 1 {
				t.Fatalf("runner legacy tool events = %d, want 1", toolCount)
			}
		})
	}
}

type scriptedSourceHandle struct {
	events []acpbridge.SourceEvent
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

func (h scriptedSourceHandle) SourceEvents() iter.Seq2[acpbridge.SourceEvent, error] {
	return func(yield func(acpbridge.SourceEvent, error) bool) {
		for _, event := range h.events {
			if !yield(acpbridge.CloneSourceEvent(event), nil) {
				return
			}
		}
	}
}

func (scriptedSourceHandle) Cancel() controller.CancelResult {
	return controller.CancelResult{Status: controller.CancelStatusCancelled}
}

func (scriptedSourceHandle) Close() error { return nil }
