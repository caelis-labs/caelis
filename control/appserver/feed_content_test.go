package appserver

import (
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/projection"
)

func TestFeedBrokerGapFillKeepsLiveContentOwnership(t *testing.T) {
	for _, withUsage := range []bool{false, true} {
		name := "without usage"
		if withUsage {
			name = "with delayed usage projection"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			reader := &checkpointPageReader{}
			broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{})
			result, err := broker.Subscribe(t.Context(), SubscribeRequest{SessionID: "session-1"})
			if err != nil {
				t.Fatal(err)
			}
			defer result.Subscription.Close()
			assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliverySync)

			final := feedIdentifiedNarrative(1, "answer-1", "turn-1", "already streamed answer")
			live := session.CloneEvent(final)
			live.ID, live.Seq, live.Visibility = "", 0, session.VisibilityUIOnly
			publishFeedSessionEvent(t, broker, live)
			assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryAppendPage)
			if withUsage {
				final.Meta = map[string]any{"usage": map[string]any{"total_tokens": 9}}
			}
			base := projection.EnvelopeBaseFromSessionEvent(broker.ref, final, projection.SessionEventTransport{})
			supplement := projection.ProjectSessionEventLiveSupplementEnvelope(base, final, agent.PublishedAssistantMessage)
			if !withUsage && len(supplement) != 0 {
				t.Fatalf("owned final without usage was not suppressed: %#v", supplement)
			}
			// Chat may drain Agent communication after yielding a final and then
			// continue the same Turn. The canonical final either has no live
			// projection, or its accounting projection has not published yet.
			input := &session.Event{
				ID: "event-2", SessionID: "session-1", Seq: 2,
				Type: session.EventTypeContext, Visibility: session.VisibilityCanonical,
				Scope: &session.EventScope{TurnID: "turn-1"},
				Actor: session.ActorRef{Kind: session.ActorKindParticipant, ID: "helper", Name: "helper"},
			}
			protocol := session.NewAgentCommunicationProtocol(session.ProtocolAgentCommunication{Text: "continue"})
			input.Protocol = &protocol
			reader.setEvents(final, input)
			publishFeedSessionEvent(t, broker, input)
			if withUsage {
				usage := assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryAppendPage)
				if len(usage.Events) != 1 || eventstream.UpdateType(usage.Events[0].Update) != eventstream.UpdateUsage {
					t.Fatalf("catch-up repeated content instead of accounting: %#v", usage.Events)
				}
			}
			message := assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryAppendPage)
			if len(message.Events) != 1 || message.Events[0].EventID != input.ID {
				t.Fatalf("catch-up repeated the owned answer before Agent input: %#v", message.Events)
			}
			for _, envelope := range supplement {
				if err := broker.Publish(envelope); err != nil {
					t.Fatal(err)
				}
			}
			terminal := eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Now())
			terminal.SessionID = "session-1"
			if err := broker.Publish(terminal); err != nil {
				t.Fatal(err)
			}
			last := assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryAppendPage)
			if len(last.Events) != 1 || !eventstream.IsTurnTerminalLifecycle(last.Events[0]) {
				t.Fatalf("late projection repeated before terminal: %#v", last.Events)
			}
			if len(broker.liveNarratives) != 0 {
				t.Fatal("completed Turn retained live content identities")
			}

			// A new broker has no retained trace. Canonical replacement must
			// still restore the full answer once, independently of live omission.
			restarted, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{})
			replay, err := restarted.Subscribe(t.Context(), SubscribeRequest{SessionID: "session-1"})
			if err != nil {
				t.Fatal(err)
			}
			defer replay.Subscription.Close()
			replayed := receiveFeedReplacement(t, replay.Subscription)
			answers := 0
			for _, envelope := range replayed {
				if chunk, ok := envelope.Update.(eventstream.ContentChunk); ok && chunk.SessionUpdate == eventstream.UpdateAgentMessage {
					answers++
					if chunk.Content.(eventstream.TextContent).Text != "already streamed answer" {
						t.Fatalf("replay answer = %#v", chunk.Content)
					}
				}
			}
			if answers != 1 {
				t.Fatalf("canonical replay answers = %d, want one", answers)
			}
		})
	}
}

func TestFeedBrokerCatchupSeparatesNarrativeIdentityAndContent(t *testing.T) {
	for _, variant := range []string{"other message", "other Turn", "other participant", "thought only"} {
		t.Run(variant, func(t *testing.T) {
			t.Parallel()
			reader := &checkpointPageReader{}
			broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{})
			result, err := broker.Subscribe(t.Context(), SubscribeRequest{SessionID: "session-1"})
			if err != nil {
				t.Fatal(err)
			}
			defer result.Subscription.Close()
			assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliverySync)
			final := feedIdentifiedNarrative(1, "answer-1", "turn-1", "same text")
			live := session.CloneEvent(final)
			live.ID, live.Seq, live.Visibility = "", 0, session.VisibilityUIOnly
			switch variant {
			case "other message":
				live.MessageID, live.Protocol.Update.MessageID = "answer-2", "answer-2"
			case "other Turn":
				live.Scope.TurnID = "turn-2"
			case "other participant":
				live.Scope.Participant = session.ParticipantRef{ID: "participant-2"}
			case "thought only":
				live.Protocol.Update.SessionUpdate = string(session.ProtocolUpdateTypeAgentThought)
			}
			publishFeedSessionEvent(t, broker, live)
			assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryAppendPage)
			reader.setEvents(final)
			if err := broker.Prime(t.Context()); err != nil {
				t.Fatal(err)
			}
			delivery := assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryAppendPage)
			if len(delivery.Events) != 1 || delivery.Events[0].EventID != final.ID {
				t.Fatalf("unowned answer was suppressed: %#v", delivery.Events)
			}
		})
	}
}

func TestFeedBrokerSuppressedFinalAdvancesCanonicalBoundary(t *testing.T) {
	reader := &checkpointPageReader{}
	broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{})
	final := feedIdentifiedNarrative(1, "answer-1", "turn-1", "answer")
	live := session.CloneEvent(final)
	live.ID, live.Seq, live.Visibility = "", 0, session.VisibilityUIOnly
	publishFeedSessionEvent(t, broker, live)
	reader.setEvents(final)
	if err := broker.Prime(t.Context()); err != nil {
		t.Fatal(err)
	}
	position, _ := broker.Boundary()
	if position == nil || durableAnchor(*position).Seq != final.Seq {
		t.Fatalf("suppressed final boundary = %#v, want sequence %d", position, final.Seq)
	}
	if len(broker.liveNarratives) != 0 {
		t.Fatal("reconciled final retained its live identity")
	}
}

func TestFeedBrokerCatchupUsesCanonicalTurnBehindControlHandle(t *testing.T) {
	reader := &checkpointPageReader{}
	broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{})
	final := feedIdentifiedNarrative(1, "answer-1", "runtime-turn", "answer")
	live := session.CloneEvent(final)
	live.ID, live.Seq, live.Visibility = "", 0, session.VisibilityUIOnly
	base := projection.EnvelopeBaseFromSessionEvent(broker.ref, live, projection.SessionEventTransport{})
	base.HandleID, base.RunID, base.TurnID = "handle-1", "control-run", "control-turn"
	for _, env := range projection.ProjectSessionEventEnvelope(base, live) {
		if err := broker.Publish(env); err != nil {
			t.Fatal(err)
		}
	}
	before, err := broker.writer.Bounds(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reader.setEvents(final)
	if err := broker.Prime(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err := broker.writer.Bounds(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after.High != before.High {
		t.Fatal("canonical catch-up appended the complete value after its owned deltas")
	}
	position, _ := broker.Boundary()
	if position == nil || durableAnchor(*position).Seq != final.Seq {
		t.Fatal("suppressed final did not advance the durable boundary")
	}
	// A terminal can arrive without catch-up (for example after read failure).
	base.Update = eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "answer-1"}
	broker.observeLiveNarrativeLocked(base)
	terminal := eventstream.TurnCompleted("handle-1", "control-run", "control-turn", time.Now())
	terminal.SessionID = broker.ref.SessionID
	broker.observeLiveNarrativeLocked(terminal)
	if len(broker.liveNarratives) != 0 {
		t.Fatal("Control terminal did not release canonical-scope bookkeeping")
	}
}

func feedIdentifiedNarrative(seq uint64, messageID, turnID, text string) *session.Event {
	event := durableProtocolEvent(seq, text)
	event.MessageID, event.Protocol.Update.MessageID = messageID, messageID
	event.Scope = &session.EventScope{TurnID: turnID}
	return event
}

func publishFeedSessionEvent(t *testing.T, broker *FeedBroker, event *session.Event) {
	t.Helper()
	base := projection.EnvelopeBaseFromSessionEvent(broker.ref, event, projection.SessionEventTransport{})
	for _, envelope := range projection.ProjectSessionEventEnvelope(base, event) {
		if err := broker.Publish(envelope); err != nil {
			t.Fatal(err)
		}
	}
}
