package gatewayapp

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestControlTurnFailureDiagnosticsPreserveCorrelationOnly(t *testing.T) {
	var logs bytes.Buffer
	composition := &runtimeComposition{authorities: runtimeHostAuthorities{
		controlFeeds: controlClientFeedRegistry{feed: &controlClientSessionFeed{}},
		diagnostics:  slog.New(slog.NewJSONHandler(&logs, nil)),
	}}
	observer, release := composition.controlTurnObserver(session.SessionRef{SessionID: "session-diagnostic"}, "operation-diagnostic")
	defer release()
	if err := observer.ObserveTurnEvent(t.Context(), eventstream.Envelope{Kind: eventstream.KindNotice, Notice: "PRIVATE_NOTICE"}); err != nil || logs.Len() != 0 {
		t.Fatal("nonblocking notice became an execution failure")
	}
	if err := observer.ObserveTurnEvent(t.Context(), eventstream.Envelope{
		Kind: eventstream.KindLifecycle, TurnID: "turn-diagnostic",
		Lifecycle: &eventstream.Lifecycle{State: "failed", Reason: "PRIVATE_REASON"},
		Err:       errors.New("PRIVATE_ERROR"), Error: "PRIVATE_PAYLOAD",
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"session-diagnostic", "operation-diagnostic", "turn-diagnostic"} {
		if !strings.Contains(logs.String(), id) {
			t.Fatalf("missing failure identity %s", id)
		}
	}
	if strings.Contains(logs.String(), "PRIVATE_") {
		t.Fatal("failure diagnostics leaked private content")
	}
}

func TestControlTurnObserverDoesNotFailProducerWhenFeedIsUnavailable(t *testing.T) {
	t.Parallel()

	feed := &controlClientSessionFeed{publishErr: errors.New("injected feed failure")}
	composition := &runtimeComposition{authorities: runtimeHostAuthorities{
		controlFeeds: controlClientFeedRegistry{feed: feed},
	}}
	observer, release := composition.controlTurnObserver(session.SessionRef{SessionID: "session-1"}, "operation-1")
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
