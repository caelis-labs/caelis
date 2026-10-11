package acpbridge

import (
	"context"
	"reflect"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/projection"
)

func replacementEvent(kind session.ProtocolUpdateType, id, text string, replace bool) *session.Event {
	e := acpNarrativeEvent(kind, text)
	e.Protocol.Update.MessageID = id
	e.Protocol.Update.Replacement = replace
	if replace && text == "" {
		e.Protocol.Update.Content = map[string]any{"type": "text", "text": ""}
	}
	return e
}

func TestNarrativeReplacementAndClearPreserveIdentityAndFinal(t *testing.T) {
	for _, kind := range []session.ProtocolUpdateType{session.ProtocolUpdateTypeAgentMessage, session.ProtocolUpdateTypeAgentThought} {
		t.Run(string(kind), func(t *testing.T) {
			var acc narrativeAccumulator
			for _, frame := range []struct {
				text    string
				replace bool
			}{{"stale", false}, {"updated", true}, {"", true}, {"current", false}} {
				_, live, ok := acc.normalize(replacementEvent(kind, "message-1", frame.text, frame.replace))
				if !ok || live == nil || narrativeMessageID(live) != "message-1" || session.EventText(live) != frame.text || live.Protocol.Update.Replacement != frame.replace {
					t.Fatalf("frame %+v lost content, identity or presence: %#v", frame, live)
				}
				updates, err := projection.ProjectEvent(live)
				if err != nil || len(updates) != 1 {
					t.Fatalf("live projection: %v, %#v", err, updates)
				}
			}
			if kind == session.ProtocolUpdateTypeAgentMessage {
				final := acc.finalAssistantEvent()
				if final == nil || session.EventText(final) != "current" || narrativeMessageID(final) != "message-1" {
					t.Fatalf("final = %#v", final)
				}
				acc.normalize(replacementEvent(kind, "message-1", "", true))
				if final := acc.finalAssistantEvent(); final != nil {
					t.Fatalf("clear retained stale final: %#v", final)
				}
			}
		})
	}
}

func TestForwarderUserReplacementRemainsObservation(t *testing.T) {
	sessions, active := newTestSessionService(t, "replacement-native-input")
	nativeMessage := model.NewTextMessage(model.RoleUser, "executed input")
	_, err := sessions.AppendEvent(context.Background(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{Type: session.EventTypeUser, Message: &nativeMessage, Visibility: session.VisibilityCanonical}})
	if err != nil {
		t.Fatal(err)
	}
	pub := newTestPublisher()
	stream, err := NewControllerForwarder(sessions).BeginControllerEvents(context.Background(), agent.ControllerEventForwardRequest{ActiveSession: active, SessionRef: active.SessionRef, TurnID: "turn", Publisher: pub})
	if err != nil {
		t.Fatal(err)
	}
	metaPatch := replacementEvent(session.ProtocolUpdateTypeUserMessage, "unknown-user-observation", "", false)
	metaPatch.Type = session.EventTypeUser
	metaPatch.Protocol.Update.Content = map[string]any{"type": "text", "text": ""}
	metaPatch.Protocol.Update.ReplacedFields = []string{"_meta"}
	if err := stream.ObserveSourceEvent(context.Background(), agent.SourceEvent{Canonical: metaPatch}); err != nil {
		t.Fatal(err)
	}
	for _, frame := range []struct {
		text    string
		replace bool
	}{{"edited", true}, {" suffix", false}, {"", true}} {
		e := replacementEvent(session.ProtocolUpdateTypeUserMessage, "peer-user-1", frame.text, frame.replace)
		msg := model.NewTextMessage(model.RoleUser, frame.text)
		e.Type, e.Message = session.EventTypeUser, &msg
		if err := stream.ObserveSourceEvent(context.Background(), agent.SourceEvent{Canonical: e}); err != nil {
			t.Fatal(err)
		}
	}
	if err := stream.Complete(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := sessions.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || session.EventText(stored[0]) != "executed input" {
		t.Fatalf("native input changed: %#v", stored)
	}
	replay, err := sessions.(session.PagedReader).EventsPage(context.Background(), session.EventPageRequest{SessionRef: active.SessionRef, Visibility: session.EventPageClientReplay})
	if err != nil || len(replay.Events) != 5 {
		t.Fatalf("observation replay: %v %#v", err, replay)
	}
	for _, e := range replay.Events[1:] {
		if !session.IsMirror(e) || session.IsCanonicalHistoryEvent(e) {
			t.Fatalf("observation acquired input authority: %#v", e)
		}
	}
}

func TestForwarderReplacementPersistsCurrentFinalWithoutStaleModelText(t *testing.T) {
	sessions, active := newTestSessionService(t, "replacement-final")
	stream, err := NewControllerForwarder(sessions).BeginControllerEvents(t.Context(), agent.ControllerEventForwardRequest{ActiveSession: active, SessionRef: active.SessionRef, TurnID: "turn", Publisher: newTestPublisher()})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct {
		text    string
		replace bool
	}{{"stale", false}, {"snapshot", true}, {"", true}, {"current", false}} {
		if err := stream.ObserveSourceEvent(t.Context(), agent.SourceEvent{Canonical: replacementEvent(session.ProtocolUpdateTypeAgentMessage, "assistant", f.text, f.replace)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := stream.Complete(t.Context()); err != nil {
		t.Fatal(err)
	}
	stored, err := sessions.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil || len(stored) != 1 || session.EventText(stored[0]) != "current" || session.EventMessageID(stored[0]) != "assistant" {
		t.Fatalf("canonical model-history round trip: %v %#v", err, stored)
	}
	message, ok := session.ModelMessageOf(stored[0])
	if !ok || message.TextContent() != "current" {
		t.Fatalf("model message retains stale content: %#v", message)
	}
}

func TestOlderMessageReplacementDoesNotTakeFinalAnswer(t *testing.T) {
	var acc narrativeAccumulator
	acc.normalize(replacementEvent(session.ProtocolUpdateTypeAgentMessage, "earlier", "progress", true))
	acc.observeBarrier(acpBarrierEvent(session.ProtocolUpdateTypeToolCall))
	acc.normalize(replacementEvent(session.ProtocolUpdateTypeAgentMessage, "latest", "final answer", true))
	for _, frame := range []struct {
		text    string
		replace bool
	}{{" late progress delta", false}, {"revised progress", true}, {" suffix", false}, {"", true}} {
		_, live, _ := acc.normalize(replacementEvent(session.ProtocolUpdateTypeAgentMessage, "earlier", frame.text, frame.replace))
		if live == nil || narrativeMessageID(live) != "earlier" {
			t.Fatalf("old message repair lost: %#v", live)
		}
		final := acc.finalAssistantEvent()
		if final == nil || narrativeMessageID(final) != "latest" || session.EventText(final) != "final answer" {
			t.Fatalf("old message stole final: %#v", final)
		}
	}
}

func TestForwarderLateOldMessageDeltaKeepsCurrentCanonicalFinal(t *testing.T) {
	sessions, active := newTestSessionService(t, "replacement-late-old-message")
	stream, err := NewControllerForwarder(sessions).BeginControllerEvents(t.Context(), agent.ControllerEventForwardRequest{ActiveSession: active, SessionRef: active.SessionRef, TurnID: "turn", Publisher: newTestPublisher()})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []*session.Event{
		replacementEvent(session.ProtocolUpdateTypeAgentMessage, "old", "progress", true),
		acpBarrierEvent(session.ProtocolUpdateTypeToolCall),
		replacementEvent(session.ProtocolUpdateTypeAgentMessage, "current", "final answer", true),
		replacementEvent(session.ProtocolUpdateTypeAgentMessage, "old", " late delta", false),
		replacementEvent(session.ProtocolUpdateTypeAgentMessage, "old", "", true),
	} {
		if err := stream.ObserveSourceEvent(t.Context(), agent.SourceEvent{Canonical: event}); err != nil {
			t.Fatal(err)
		}
	}
	if err := stream.Complete(t.Context()); err != nil {
		t.Fatal(err)
	}
	stored, err := sessions.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil || len(stored) != 1 || session.EventMessageID(stored[0]) != "current" || session.EventText(stored[0]) != "final answer" {
		t.Fatalf("older display frame changed canonical final: %v %#v", err, stored)
	}
	message, ok := session.ModelMessageOf(stored[0])
	if !ok || message.TextContent() != "final answer" {
		t.Fatalf("older display frame changed model context: %#v", message)
	}
}

func TestForwarderLatestIdentifiedMessageResumesCanonicalFinalAfterTool(t *testing.T) {
	for _, test := range []struct {
		name, text, want string
		replace          bool
	}{{"delta", " suffix", "progress suffix", false}, {"snapshot", "final answer", "final answer", true}} {
		t.Run(test.name, func(t *testing.T) {
			sessions, active := newTestSessionService(t, "replacement-resume-"+test.name)
			stream, err := NewControllerForwarder(sessions).BeginControllerEvents(t.Context(), agent.ControllerEventForwardRequest{ActiveSession: active, SessionRef: active.SessionRef, TurnID: "turn", Publisher: newTestPublisher()})
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range []*session.Event{
				replacementEvent(session.ProtocolUpdateTypeAgentMessage, "old", "old progress", true),
				replacementEvent(session.ProtocolUpdateTypeAgentMessage, "latest", "progress", true),
				acpBarrierEvent(session.ProtocolUpdateTypeToolCall),
				replacementEvent(session.ProtocolUpdateTypeAgentMessage, "old", " late delta", false),
				replacementEvent(session.ProtocolUpdateTypeAgentMessage, "latest", test.text, test.replace),
			} {
				if err := stream.ObserveSourceEvent(t.Context(), agent.SourceEvent{Canonical: event}); err != nil {
					t.Fatal(err)
				}
			}
			if err := stream.Complete(t.Context()); err != nil {
				t.Fatal(err)
			}
			stored, err := sessions.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
			if err != nil || len(stored) != 1 || session.EventMessageID(stored[0]) != "latest" || session.EventText(stored[0]) != test.want {
				t.Fatalf("latest post-tool final lost: %v %#v", err, stored)
			}
			message, ok := session.ModelMessageOf(stored[0])
			if !ok || message.TextContent() != test.want {
				t.Fatalf("latest post-tool model context lost: %#v", message)
			}
		})
	}
}

func TestNarrativeMetadataOnlyPatchPreservesBodyAndFinalEligibility(t *testing.T) {
	var acc narrativeAccumulator
	acc.normalize(replacementEvent(session.ProtocolUpdateTypeAgentMessage, "latest", "progress", true))
	patch := replacementEvent(session.ProtocolUpdateTypeAgentThought, "thought", "", false)
	patch.Protocol.Update.Content = map[string]any{"type": "text", "text": ""}
	patch.Protocol.Update.ReplacedFields = []string{"_meta"}
	_, live, ok := acc.normalize(patch)
	if !ok || live == nil || acc.final.FinalText() != "progress" {
		t.Fatalf("metadata-only thought disrupted body or was dropped: %#v", live)
	}
	acc.observeBarrier(acpBarrierEvent(session.ProtocolUpdateTypeToolCall))
	patch = replacementEvent(session.ProtocolUpdateTypeAgentMessage, "latest", "", false)
	patch.Protocol.Update.Content = map[string]any{"type": "text", "text": ""}
	patch.Protocol.Update.ReplacedFields = []string{"_meta"}
	_, live, ok = acc.normalize(patch)
	if !ok || live == nil || acc.finalAssistantEvent() != nil {
		t.Fatalf("metadata-only latest update restored final eligibility or was dropped: %#v", live)
	}
	updates, err := projection.ProjectEvent(live)
	if err != nil || len(updates) != 1 {
		t.Fatalf("metadata clear projection: %v %#v", err, updates)
	}
	chunk, ok := updates[0].(eventstream.ContentChunk)
	if !ok || chunk.Replacement || chunk.Meta != nil || !reflect.DeepEqual(chunk.ReplacedFields, []string{"_meta"}) || session.ExtractProtocolText(chunk.Content) != "" {
		t.Fatalf("metadata clear lost explicit native presence: %#v", updates)
	}
	acc.normalize(replacementEvent(session.ProtocolUpdateTypeAgentMessage, "latest", " suffix", false))
	if final := acc.finalAssistantEvent(); final == nil || session.EventText(final) != "progress suffix" {
		t.Fatalf("metadata clear erased retained current body: %#v", final)
	}
}
