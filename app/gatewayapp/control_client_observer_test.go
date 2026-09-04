package gatewayapp

import (
	"context"
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestControlTurnObserverDoesNotFailProducerWhenFeedIsUnavailable(t *testing.T) {
	t.Parallel()

	feed := &controlClientSessionFeed{publishErr: errors.New("injected feed failure")}
	composition := &runtimeComposition{authorities: runtimeHostAuthorities{
		controlFeeds: controlClientFeedRegistry{feed: feed},
	}}
	observer, release := composition.controlTurnObserver(session.SessionRef{SessionID: "session-1"})
	defer release()
	if observer == nil {
		t.Fatal("controlTurnObserver() returned nil")
	}
	if err := observer.ObserveTurnEvent(context.Background(), eventstream.Envelope{
		Kind: eventstream.KindNotice, SessionID: "session-1", Notice: "progress",
	}); err != nil {
		t.Fatalf("ObserveTurnEvent() error = %v, want lossy feed failure isolated", err)
	}
	if got := feed.publishCalls.Load(); got != 1 {
		t.Fatalf("Publish calls = %d, want 1", got)
	}
}
