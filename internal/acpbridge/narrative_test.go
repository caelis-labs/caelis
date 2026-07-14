package acpbridge

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestNarrativeAccumulatorMessageChunksEmitDeltasOnly(t *testing.T) {
	t.Parallel()

	acc := &narrativeAccumulator{}
	deltas := applyNarrativeSequence(acc, []*session.Event{
		acpNarrativeEvent(session.ProtocolUpdateTypeAgentMessage, "a"),
		acpNarrativeEvent(session.ProtocolUpdateTypeAgentMessage, "ab"),
	})
	if want := []string{"a", "ab"}; !reflect.DeepEqual(deltas, want) {
		t.Fatalf("live message deltas = %#v, want %#v", deltas, want)
	}
	final := acc.finalAssistantEvent()
	if final == nil {
		t.Fatal("finalAssistantEvent() = nil, want canonical assistant")
	}
	if got, want := session.EventText(final), "aab"; got != want {
		t.Fatalf("final assistant text = %q, want %q", got, want)
	}
	if final.Visibility != session.VisibilityCanonical {
		t.Fatalf("final assistant visibility = %q, want canonical", final.Visibility)
	}
	if strings.TrimSpace(final.ID) != "" {
		t.Fatalf("final assistant ID = %q, want empty live materialization ID", final.ID)
	}
}

func TestNarrativeAccumulatorIgnoresAuditSource(t *testing.T) {
	t.Parallel()

	for _, source := range []string{"acp", "slash", "renamed-product-source"} {
		acc := &narrativeAccumulator{}
		event := acpNarrativeEvent(session.ProtocolUpdateTypeAgentMessage, "hello")
		event.Scope.Source = source
		_, live, ok := acc.normalize(event)
		if !ok || live == nil || session.EventText(live) != "hello" {
			t.Fatalf("source %q changed narrative classification: ok=%v live=%#v", source, ok, live)
		}
	}
}

func TestNarrativeAccumulatorThoughtChunksEmitDeltasAndResetFinal(t *testing.T) {
	t.Parallel()

	acc := &narrativeAccumulator{}
	deltas := applyNarrativeSequence(acc, []*session.Event{
		acpNarrativeEvent(session.ProtocolUpdateTypeAgentMessage, "progress"),
		acpNarrativeEvent(session.ProtocolUpdateTypeAgentThought, "think"),
		acpNarrativeEvent(session.ProtocolUpdateTypeAgentMessage, "answer"),
	})
	if want := []string{"progress", "think", "answer"}; !reflect.DeepEqual(deltas, want) {
		t.Fatalf("live narrative deltas = %#v, want %#v", deltas, want)
	}
	final := acc.finalAssistantEvent()
	if final == nil {
		t.Fatal("finalAssistantEvent() = nil, want post-thought assistant")
	}
	if got, want := session.EventText(final), "answer"; got != want {
		t.Fatalf("final assistant text = %q, want %q after thought barrier", got, want)
	}
}

func TestNarrativeAccumulatorThoughtUsesExactACPDeltaSemantics(t *testing.T) {
	t.Parallel()

	t.Run("repeated deltas", func(t *testing.T) {
		t.Parallel()

		acc := &narrativeAccumulator{}
		deltas := applyNarrativeSequence(acc, []*session.Event{
			acpNarrativeEvent(session.ProtocolUpdateTypeAgentThought, "ha"),
			acpNarrativeEvent(session.ProtocolUpdateTypeAgentThought, "ha"),
		})
		if want := []string{"ha", "ha"}; !reflect.DeepEqual(deltas, want) {
			t.Fatalf("repeated thought deltas = %#v, want %#v", deltas, want)
		}
	})

	t.Run("prefix growing deltas", func(t *testing.T) {
		t.Parallel()

		acc := &narrativeAccumulator{}
		deltas := applyNarrativeSequence(acc, []*session.Event{
			acpNarrativeEvent(session.ProtocolUpdateTypeAgentThought, "hel"),
			acpNarrativeEvent(session.ProtocolUpdateTypeAgentThought, "hello"),
			acpNarrativeEvent(session.ProtocolUpdateTypeAgentThought, "hello"),
		})
		if want := []string{"hel", "hello", "hello"}; !reflect.DeepEqual(deltas, want) {
			t.Fatalf("prefix-growing thought deltas = %#v, want %#v", deltas, want)
		}
	})
}

func TestNarrativeAccumulatorResetsReasoningAtSegmentBarriers(t *testing.T) {
	t.Parallel()

	acc := &narrativeAccumulator{}
	applyNarrativeSequence(acc, []*session.Event{
		acpNarrativeEvent(session.ProtocolUpdateTypeAgentThought, "thinking"),
	})
	if got := acc.reasoning.FinalText(); got != "thinking" {
		t.Fatalf("reasoning before assistant = %q, want thinking", got)
	}
	applyNarrativeSequence(acc, []*session.Event{
		acpNarrativeEvent(session.ProtocolUpdateTypeAgentMessage, "answer"),
	})
	if got := acc.reasoning.FinalText(); got != "" {
		t.Fatalf("reasoning after assistant = %q, want reset", got)
	}

	for _, barrier := range []session.ProtocolUpdateType{
		session.ProtocolUpdateTypeToolCall,
		session.ProtocolUpdateTypePlan,
	} {
		applyNarrativeSequence(acc, []*session.Event{
			acpNarrativeEvent(session.ProtocolUpdateTypeAgentThought, "next thought"),
			acpBarrierEvent(barrier),
		})
		if got := acc.reasoning.FinalText(); got != "" {
			t.Fatalf("reasoning after %s barrier = %q, want reset", barrier, got)
		}
	}
}

func TestNarrativeAccumulatorToolBarrierResetsFinalAssistant(t *testing.T) {
	t.Parallel()

	acc := &narrativeAccumulator{}
	deltas := applyNarrativeSequence(acc, []*session.Event{
		acpNarrativeEvent(session.ProtocolUpdateTypeAgentMessage, "before tool"),
		acpBarrierEvent(session.ProtocolUpdateTypeToolCall),
		acpNarrativeEvent(session.ProtocolUpdateTypeAgentMessage, "after tool"),
	})
	if want := []string{"before tool", "after tool"}; !reflect.DeepEqual(deltas, want) {
		t.Fatalf("live message deltas = %#v, want %#v", deltas, want)
	}
	final := acc.finalAssistantEvent()
	if final == nil {
		t.Fatal("finalAssistantEvent() = nil, want post-tool assistant")
	}
	if got, want := session.EventText(final), "after tool"; got != want {
		t.Fatalf("final assistant text = %q, want %q after tool barrier", got, want)
	}
}

func TestNarrativeAccumulatorPlanBarrierResetsFinalAssistant(t *testing.T) {
	t.Parallel()

	acc := &narrativeAccumulator{}
	deltas := applyNarrativeSequence(acc, []*session.Event{
		acpNarrativeEvent(session.ProtocolUpdateTypeAgentMessage, "before plan"),
		acpBarrierEvent(session.ProtocolUpdateTypePlan),
		acpNarrativeEvent(session.ProtocolUpdateTypeAgentMessage, "after plan"),
	})
	if want := []string{"before plan", "after plan"}; !reflect.DeepEqual(deltas, want) {
		t.Fatalf("live message deltas = %#v, want %#v", deltas, want)
	}
	final := acc.finalAssistantEvent()
	if final == nil {
		t.Fatal("finalAssistantEvent() = nil, want post-plan assistant")
	}
	if got, want := session.EventText(final), "after plan"; got != want {
		t.Fatalf("final assistant text = %q, want %q after plan barrier", got, want)
	}
}

func TestAppendNarrativeTextAppendsExactACPDeltas(t *testing.T) {
	t.Parallel()

	appended, delta := appendNarrativeText("hel", "lo")
	if appended != "hello" || delta != "lo" {
		t.Fatalf("append delta = (%q, %q), want (hello, lo)", appended, delta)
	}

	appended, delta = appendNarrativeText("hel", "hello")
	if appended != "helhello" || delta != "hello" {
		t.Fatalf("append prefix-growing delta = (%q, %q), want (helhello, hello)", appended, delta)
	}

	appended, delta = appendNarrativeText("hello", "hel")
	if appended != "hellohel" || delta != "hel" {
		t.Fatalf("append short-prefix delta = (%q, %q), want (hellohel, hel)", appended, delta)
	}
}

func TestNarrativeAccumulatorSeparatesAssistantMessageIDs(t *testing.T) {
	t.Parallel()

	first := acpNarrativeEvent(session.ProtocolUpdateTypeAgentMessage, "first")
	first.Protocol.Update.MessageID = "m1"
	second := acpNarrativeEvent(session.ProtocolUpdateTypeAgentMessage, "second")
	second.Protocol.Update.MessageID = "m2"
	acc := &narrativeAccumulator{}
	deltas := applyNarrativeSequence(acc, []*session.Event{first, second})
	if want := []string{"first", "second"}; !reflect.DeepEqual(deltas, want) {
		t.Fatalf("message-id deltas = %#v, want %#v", deltas, want)
	}
	final := acc.finalAssistantEvent()
	if final == nil || session.EventText(final) != "second" {
		t.Fatalf("final assistant = %#v, want latest message-id segment", final)
	}
}

func TestNarrativeAccumulatorPreservesRepeatedACPDeltaChunks(t *testing.T) {
	t.Parallel()

	acc := &narrativeAccumulator{}
	deltas := applyNarrativeSequence(acc, []*session.Event{
		acpNarrativeEvent(session.ProtocolUpdateTypeAgentMessage, "hello"),
		acpNarrativeEvent(session.ProtocolUpdateTypeAgentMessage, "hello"),
	})
	if want := []string{"hello", "hello"}; !reflect.DeepEqual(deltas, want) {
		t.Fatalf("repeated ACP chunk deltas = %#v, want %#v", deltas, want)
	}
	final := acc.finalAssistantEvent()
	if final == nil || session.EventText(final) != "hellohello" {
		t.Fatalf("final assistant = %#v, want exact repeated deltas", final)
	}
}

func TestEnvelopeWithNarrativeTextPreservesEnvelopeShape(t *testing.T) {
	t.Parallel()

	occurredAt := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	original := &eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		Cursor:        "cursor-1",
		EventID:       "event-1",
		ProjectionID:  "projection-1",
		SessionID:     "sess-1",
		HandleID:      "handle-1",
		RunID:         "run-1",
		TurnID:        "turn-1",
		OccurredAt:    occurredAt,
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "emma",
		Actor:         "assistant",
		ParticipantID: "emma",
		Final:         true,
		Meta: map[string]any{
			"vendor": "acp-test",
			"nested": map[string]any{"key": "value"},
		},
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			MessageID:     "msg-1",
			Content:       schema.TextContent{Type: "text", Text: "hello world"},
			Meta: map[string]any{
				"chunk_meta": true,
			},
		},
	}

	repaired := envelopeWithNarrativeText(original, string(session.ProtocolUpdateTypeAgentMessage), "lo")
	if repaired == nil {
		t.Fatal("envelopeWithNarrativeText() = nil")
	}
	if repaired.Kind != eventstream.KindSessionUpdate {
		t.Fatalf("repaired kind = %q, want session update", repaired.Kind)
	}
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{name: "cursor", got: repaired.Cursor, want: original.Cursor},
		{name: "event_id", got: repaired.EventID, want: original.EventID},
		{name: "projection_id", got: repaired.ProjectionID, want: original.ProjectionID},
		{name: "session_id", got: repaired.SessionID, want: original.SessionID},
		{name: "handle_id", got: repaired.HandleID, want: original.HandleID},
		{name: "run_id", got: repaired.RunID, want: original.RunID},
		{name: "turn_id", got: repaired.TurnID, want: original.TurnID},
		{name: "scope_id", got: repaired.ScopeID, want: original.ScopeID},
		{name: "actor", got: repaired.Actor, want: original.Actor},
		{name: "participant_id", got: repaired.ParticipantID, want: original.ParticipantID},
	} {
		if tc.got != tc.want {
			t.Fatalf("repaired %s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
	if repaired.Scope != original.Scope {
		t.Fatalf("repaired scope = %q, want %q", repaired.Scope, original.Scope)
	}
	if !repaired.Final {
		t.Fatal("repaired final = false, want preserved true")
	}
	if !repaired.OccurredAt.Equal(original.OccurredAt) {
		t.Fatalf("repaired occurred_at = %v, want %v", repaired.OccurredAt, original.OccurredAt)
	}
	if !reflect.DeepEqual(repaired.Meta, original.Meta) {
		t.Fatalf("repaired meta = %#v, want %#v", repaired.Meta, original.Meta)
	}
	chunk, ok := repaired.Update.(schema.ContentChunk)
	if !ok {
		t.Fatalf("repaired update = %#v, want ContentChunk", repaired.Update)
	}
	if chunk.SessionUpdate != schema.UpdateAgentMessage {
		t.Fatalf("repaired session_update = %q, want agent_message_chunk", chunk.SessionUpdate)
	}
	if got, want := schema.ExtractTextValue(chunk.Content), "lo"; got != want {
		t.Fatalf("repaired narrative text = %q, want delta %q", got, want)
	}
}

func TestEnvelopeWithNarrativeTextDerivesMetaFromUpdateWhenAbsent(t *testing.T) {
	t.Parallel()

	original := &eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "hello"},
			Meta: map[string]any{
				"from_update": true,
			},
		},
	}
	repaired := envelopeWithNarrativeText(original, string(session.ProtocolUpdateTypeAgentMessage), "lo")
	if repaired == nil {
		t.Fatal("envelopeWithNarrativeText() = nil")
	}
	if !reflect.DeepEqual(repaired.Meta, map[string]any{"from_update": true}) {
		t.Fatalf("repaired meta = %#v, want derived from update", repaired.Meta)
	}
}

func applyNarrativeSequence(acc *narrativeAccumulator, events []*session.Event) []string {
	var deltas []string
	for _, event := range events {
		if _, live, ok := acc.normalize(event); ok {
			if live != nil {
				deltas = append(deltas, narrativeEventText(live, eventUpdateType(live)))
			}
			continue
		}
		acc.observeBarrier(event)
	}
	return deltas
}

func acpNarrativeEvent(updateType session.ProtocolUpdateType, text string) *session.Event {
	var event session.Event
	switch updateType {
	case session.ProtocolUpdateTypeAgentThought:
		message := model.NewReasoningMessage(model.RoleAssistant, text, model.ReasoningVisibilityVisible)
		event = session.Event{
			Type:       session.EventTypeAssistant,
			Visibility: session.VisibilityCanonical,
			Message:    &message,
			Text:       text,
		}
	default:
		message := model.NewTextMessage(model.RoleAssistant, text)
		event = session.Event{
			Type:       session.EventTypeAssistant,
			Visibility: session.VisibilityCanonical,
			Message:    &message,
			Text:       text,
		}
	}
	event.Scope = &session.EventScope{Source: "acp"}
	event.Protocol = &session.EventProtocol{
		Update: &session.ProtocolUpdate{
			SessionUpdate: string(updateType),
			Content:       session.ProtocolTextContent(text),
		},
	}
	return &event
}

func acpBarrierEvent(updateType session.ProtocolUpdateType) *session.Event {
	event := &session.Event{
		Scope: &session.EventScope{Source: "acp"},
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(updateType),
			},
		},
	}
	switch updateType {
	case session.ProtocolUpdateTypeToolCall:
		event.Type = session.EventTypeToolCall
		event.Visibility = session.VisibilityUIOnly
		event.Protocol.Update.ToolCallID = "call-1"
	case session.ProtocolUpdateTypePlan:
		event.Type = session.EventTypePlan
		event.Visibility = session.VisibilityUIOnly
	default:
	}
	return event
}
