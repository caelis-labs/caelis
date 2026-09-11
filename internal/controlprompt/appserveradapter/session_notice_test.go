package appserveradapter

import (
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestSessionPresenceDeliversNoticeAfterTurnObservationCloses(t *testing.T) {
	replay := newSessionClientAdapterTestSubscription()
	presence := newSessionClientAdapterTestSubscription()
	client := &sessionClientAdapterTestClient{reconnectSubscriptions: []*sessionClientAdapterTestSubscription{replay, presence}}
	adapter := newSessionClientAdapterForTest(t, client, &sessionClientAdapterTestParticipantClient{}, "session-1", "cli-tui")
	notices := make(chan eventstream.Envelope, 3)
	adapter.onSessionNotice = func(envelope eventstream.Envelope) { notices <- envelope }
	resumed, err := adapter.ResumeSession(t.Context(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := resumed.Reconnect.Close(); err != nil {
		t.Fatal(err)
	}
	notice := eventstream.Envelope{Kind: eventstream.KindNotice, SessionID: "session-1", Notice: "MCP unavailable", Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient}}
	foreign := eventstream.CloneEnvelope(notice)
	foreign.SessionID = "other-session"
	owned := eventstream.CloneEnvelope(notice)
	owned.TurnID = "other-turn"
	presence.events <- foreign
	presence.events <- owned
	presence.events <- notice
	select {
	case received := <-notices:
		if received.Notice != notice.Notice || received.SessionID != notice.SessionID || received.TurnID != "" {
			t.Fatalf("wrong notice: %#v", received)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("idle notice was not delivered")
	}
	if err := adapter.ResetSession(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(notices) != 0 || !presence.closed {
		t.Fatalf("extra notices=%d presence closed=%v", len(notices), presence.closed)
	}
}
