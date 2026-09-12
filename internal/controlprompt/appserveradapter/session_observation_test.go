package appserveradapter

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

type observationContextClient struct {
	*sessionClientAdapterTestClient
	feedCtx     context.Context
	inspectHook func()
}

func (c *observationContextClient) Reconnect(ctx context.Context, req appserver.ReconnectRequest) (appserver.ReconnectResult, error) {
	c.feedCtx = ctx
	return c.sessionClientAdapterTestClient.Reconnect(ctx, req)
}

func (c *observationContextClient) InspectSession(ctx context.Context, req appserver.StateRequest) (appserver.SessionState, error) {
	if c.inspectHook != nil {
		c.inspectHook()
	}
	return c.sessionClientAdapterTestClient.InspectSession(ctx, req)
}

func TestSessionObservationSurvivesCommandContextAndTracksLaterTurns(t *testing.T) {
	client := &observationContextClient{sessionClientAdapterTestClient: &sessionClientAdapterTestClient{subscription: newSessionClientAdapterTestSubscription()}}
	adapter := newSessionClientAdapterForTest(t, client.sessionClientAdapterTestClient, &sessionClientAdapterTestParticipantClient{}, "session-1", "cli-tui")
	adapter.sessionClient = client
	ctx, cancel := context.WithCancel(context.Background())
	snapshot, err := adapter.ResumeSession(ctx, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if client.feedCtx.Err() != nil {
		t.Fatal("finishing /resume cancelled the persistent feed")
	}
	r := snapshot.Reconnect.(*clientSessionReconnect)
	for _, turn := range []string{"one", "two"} {
		env := eventstream.Envelope{Kind: eventstream.KindLifecycle, SessionID: "session-1", Scope: eventstream.ScopeMain,
			HandleID: "h-" + turn, RunID: "r-" + turn, TurnID: "t-" + turn,
			Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateRunning}}
		r.ObserveEvent(env)
		if !adapter.CanSubmitRunningPrompt() {
			t.Fatal("later Turn is not addressable by the observer")
		}
		if _, err := adapter.Submit(context.Background(), controlprompt.Submission{Text: "steer", Mode: controlprompt.SubmissionModeActiveTurn}); err != nil {
			t.Fatal(err)
		}
		if got := client.steer.Target; got.HandleID != env.HandleID || got.RunID != env.RunID || got.TurnID != env.TurnID {
			t.Fatalf("steer target = %#v, want the currently observed Turn", got)
		}
		env.Lifecycle = &eventstream.Lifecycle{State: eventstream.LifecycleStateCompleted}
		r.ObserveEvent(env)
		if adapter.CanSubmitRunningPrompt() {
			t.Fatal("completed Turn remains steerable")
		}
	}
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	if client.feedCtx.Err() == nil {
		t.Fatal("surface close left its feed context alive")
	}
}

func TestSessionObservationNeverRetargetsInputWhileRefreshingRevision(t *testing.T) {
	client := &observationContextClient{sessionClientAdapterTestClient: &sessionClientAdapterTestClient{subscription: newSessionClientAdapterTestSubscription()}}
	adapter := newSessionClientAdapterForTest(t, client.sessionClientAdapterTestClient, &sessionClientAdapterTestParticipantClient{}, "session-1", "cli-tui")
	adapter.sessionClient = client
	snapshot, err := adapter.ResumeSession(context.Background(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	r := snapshot.Reconnect.(*clientSessionReconnect)
	running := func(turn string) eventstream.Envelope {
		return eventstream.Envelope{Kind: eventstream.KindLifecycle, SessionID: "session-1", HandleID: "h", RunID: "r-" + turn, TurnID: turn, Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateRunning}}
	}
	r.ObserveEvent(running("old"))
	client.inspectHook = func() { r.ObserveEvent(running("new")) }
	if err := r.steer(context.Background(), "old turn input", "", nil); err != nil {
		t.Fatal(err)
	}
	if client.steer.Target.TurnID != "old" {
		t.Fatalf("input retargeted to %q", client.steer.Target.TurnID)
	}
}

func TestSessionInterruptRejectsQueuedInputFromPreviousView(t *testing.T) {
	client := &sessionClientAdapterTestClient{subscription: newSessionClientAdapterTestSubscription(), state: appserver.SessionState{Run: appserver.RunState{Active: true, HandleID: "h", RunID: "r", TurnID: "t"}}}
	adapter := newSessionClientAdapterForTest(t, client, &sessionClientAdapterTestParticipantClient{}, "b", "cli-tui")
	if _, err := adapter.ResumeSession(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	if err := adapter.InterruptSession(context.Background(), "a"); err == nil {
		t.Fatal("old view interrupt was accepted")
	}
	if client.cancel.Target != (appserver.TurnTarget{}) {
		t.Fatal("old view interrupted the newly selected Session")
	}
	if err := adapter.InterruptSession(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	if client.cancel.Target.TurnID != "t" {
		t.Fatal("current view failed to explicitly interrupt")
	}
}

type lateObservationTurn struct {
	admissionTargetTurn
	cancels atomic.Int32
	done    chan struct{}
}

func (t *lateObservationTurn) Cancel(context.Context, string) error { t.cancels.Add(1); return nil }
func (t *lateObservationTurn) Close() error                         { close(t.done); return nil }

func TestSessionSurfaceCloseDoesNotCancelLateAcceptedTurn(t *testing.T) {
	adapter := &SessionClientAdapter{}
	started, release := make(chan struct{}), make(chan struct{})
	turn := &lateObservationTurn{done: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		_, err := adapter.startAdmittedTurn(context.Background(), func(context.Context) (appserver.TargetTurn, error) {
			close(started)
			<-release
			return turn, nil
		})
		result <- err
	}()
	<-started
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("admission result = %v", err)
	}
	close(release)
	select {
	case <-turn.done:
	case <-time.After(time.Second):
		t.Fatal("late observation was not released")
	}
	if turn.cancels.Load() != 0 {
		t.Fatal("surface exit cancelled an accepted Host Turn")
	}
}

func TestExplicitSurfaceInterruptCancelsLateAcceptedTurn(t *testing.T) {
	adapter := &SessionClientAdapter{}
	ctx, interrupt := context.WithCancelCause(context.Background())
	started, release := make(chan struct{}), make(chan struct{})
	turn := &lateObservationTurn{done: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		_, err := adapter.startAdmittedTurn(ctx, func(context.Context) (appserver.TargetTurn, error) {
			close(started)
			<-release
			return turn, nil
		})
		result <- err
	}()
	<-started
	interrupt(controlprompt.ErrUserInterrupt)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("admission result = %v", err)
	}
	close(release)
	select {
	case <-turn.done:
	case <-time.After(time.Second):
		t.Fatal("late observation was not released")
	}
	if turn.cancels.Load() != 1 {
		t.Fatal("explicit interrupt did not cancel the accepted Host Turn")
	}
}
