package controladapter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/internal/kernel"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func TestGatewayTurnEventsSynthesizesCompletedForEmptyStream(t *testing.T) {
	events := make(chan eventstream.Envelope)
	close(events)
	turn := newGatewayTurn(&testGatewayTurnHandle{acpEvents: events})

	out := collectAdapterTurnEvents(turn.Events())
	if len(out) != 1 {
		t.Fatalf("events = %#v, want synthesized completion only", out)
	}
	assertAdapterLifecycleState(t, out[0], eventstream.LifecycleStateCompleted)
}

func TestGatewayTurnEventsSynthesizesFailedAfterError(t *testing.T) {
	events := make(chan eventstream.Envelope, 1)
	events <- eventstream.Error(errors.New("provider failed"))
	close(events)
	turn := newGatewayTurn(&testGatewayTurnHandle{acpEvents: events})

	out := collectAdapterTurnEvents(turn.Events())
	if len(out) != 2 {
		t.Fatalf("events = %#v, want error plus failed lifecycle", out)
	}
	if out[0].Kind != eventstream.KindError {
		t.Fatalf("first event = %#v, want error", out[0])
	}
	assertAdapterLifecycleState(t, out[1], eventstream.LifecycleStateFailed)
}

func TestGatewayTurnEventsSynthesizesCancelledAfterCancelError(t *testing.T) {
	events := make(chan eventstream.Envelope, 1)
	events <- eventstream.Error(errors.New("providers: context canceled"))
	close(events)
	turn := newGatewayTurn(&testGatewayTurnHandle{acpEvents: events})

	out := collectAdapterTurnEvents(turn.Events())
	if len(out) != 2 {
		t.Fatalf("events = %#v, want error plus cancelled lifecycle", out)
	}
	assertAdapterLifecycleState(t, out[1], eventstream.LifecycleStateCancelled)
}

func TestGatewayTurnEventsForwardsExplicitTerminalOnce(t *testing.T) {
	acpEvents := make(chan eventstream.Envelope, 2)
	acpEvents <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Time{})
	acpEvents <- eventstream.TurnFailed("handle-1", "run-1", "turn-1", "late", time.Time{})
	close(acpEvents)
	turn := newGatewayTurn(&testGatewayTurnHandle{acpEvents: acpEvents})

	out := collectAdapterTurnEvents(turn.Events())
	if len(out) != 1 {
		t.Fatalf("events = %#v, want first terminal only", out)
	}
	assertAdapterLifecycleState(t, out[0], eventstream.LifecycleStateCompleted)
}

func TestGatewayTurnEventsReturnsSameStream(t *testing.T) {
	events := make(chan eventstream.Envelope)
	close(events)
	turn := newGatewayTurn(&testGatewayTurnHandle{acpEvents: events})

	first := turn.Events()
	second := turn.Events()
	if first != second {
		t.Fatal("Events() returned different channels; want single-consumer stream")
	}
}

func TestGatewayTurnSubscriptionFailureEmitsErrorAndInterruptedTerminal(t *testing.T) {
	events := make(chan eventstream.Envelope, 1)
	events <- eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Scope:     eventstream.ScopeSubagent,
		ScopeID:   "task-1",
	}
	close(events)
	turn := &gatewayTurn{
		handle:       &testGatewayTurnHandle{},
		subscription: &errorFeedSubscription{events: events, err: controlclient.ErrSlowConsumer},
	}

	out := collectAdapterTurnEvents(turn.Events())
	if len(out) != 3 {
		t.Fatalf("events = %#v, want child envelope, delivery error, interrupted terminal", out)
	}
	if out[0].Scope != eventstream.ScopeSubagent || out[1].Kind != eventstream.KindError || !errors.Is(out[1].Err, controlclient.ErrSlowConsumer) {
		t.Fatalf("subscription failure sequence = %#v", out)
	}
	assertAdapterLifecycleState(t, out[2], eventstream.LifecycleStateInterrupted)
}

func TestGatewayTurnAttachmentFailureEmitsFailedTerminal(t *testing.T) {
	events := make(chan eventstream.Envelope)
	attachment := make(chan error, 1)
	attachment <- errors.New("feed publish rejected")
	close(attachment)
	turn := &gatewayTurn{
		handle:       &testGatewayTurnHandle{},
		subscription: &errorFeedSubscription{events: events},
		attachment:   attachment,
	}

	out := collectAdapterTurnEvents(turn.Events())
	if len(out) != 2 || out[0].Kind != eventstream.KindError {
		t.Fatalf("attachment failure sequence = %#v, want error plus terminal", out)
	}
	assertAdapterLifecycleState(t, out[1], eventstream.LifecycleStateFailed)
	if out[1].HandleID != "handle-1" || out[1].RunID != "run-1" || out[1].TurnID != "turn-1" {
		t.Fatalf("attachment terminal identity = %#v", out[1])
	}
}

func TestGatewayTurnHistoricalUnstampedTerminalCannotEndCurrentTurn(t *testing.T) {
	events := make(chan eventstream.Envelope, 3)
	old := eventstream.TurnCompleted("", "", "", time.Unix(1, 0))
	old.Scope = eventstream.ScopeMain
	events <- old
	events <- eventstream.Envelope{
		Kind: eventstream.KindNotice, SessionID: "session-1",
		HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1",
		Scope: eventstream.ScopeMain, Notice: "current turn continued",
	}
	events <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(2, 0))
	close(events)
	turn := &gatewayTurn{
		handle:       &testGatewayTurnHandle{},
		subscription: &errorFeedSubscription{events: events},
	}

	out := collectAdapterTurnEvents(turn.Events())
	if len(out) != 2 || out[0].Notice != "current turn continued" {
		t.Fatalf("current turn was ended by historical terminal: %#v", out)
	}
	assertAdapterLifecycleState(t, out[1], eventstream.LifecycleStateCompleted)
}

func TestGatewayTurnApprovalSettlementCannotEndCurrentTurn(t *testing.T) {
	events := make(chan eventstream.Envelope, 3)
	events <- eventstream.Envelope{
		Kind:              eventstream.KindLifecycle,
		SessionID:         "session-1",
		HandleID:          "handle-1",
		RunID:             "run-1",
		TurnID:            "turn-1",
		Scope:             eventstream.ScopeMain,
		ApprovalRequestID: "approval-1",
		Lifecycle:         &eventstream.Lifecycle{State: eventstream.LifecycleStateCompleted, Reason: "resolved"},
	}
	events <- eventstream.Envelope{
		Kind: eventstream.KindNotice, SessionID: "session-1",
		HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1",
		Scope: eventstream.ScopeMain, Notice: "current turn continued",
	}
	events <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(2, 0))
	close(events)
	turn := &gatewayTurn{
		handle:       &testGatewayTurnHandle{},
		subscription: &errorFeedSubscription{events: events},
	}

	out := collectAdapterTurnEvents(turn.Events())
	if len(out) != 3 || out[0].ApprovalRequestID != "approval-1" || out[1].Notice != "current turn continued" {
		t.Fatalf("approval settlement ended current turn: %#v", out)
	}
	if !eventstream.IsTurnTerminalLifecycle(out[2]) {
		t.Fatalf("last event = %#v, want Turn terminal", out[2])
	}
}

func TestGatewayTurnCloseUnblocksUnreadSubscriptionDelivery(t *testing.T) {
	input := make(chan eventstream.Envelope)
	handle := newBrokerTestHandle(nil)
	turn := &gatewayTurn{
		handle:       handle,
		subscription: &errorFeedSubscription{events: input},
	}
	out := turn.Events()
	accepted := make(chan struct{})
	go func() {
		input <- eventstream.Envelope{Kind: eventstream.KindNotice, Notice: "unread"}
		close(accepted)
	}()
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("subscription relay did not accept unread event")
	}

	closed := make(chan error, 1)
	go func() { closed <- turn.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close blocked behind unread output")
	}
	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("Close delivered an unread event")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not terminate unread output")
	}
	if calls := handle.cancelCalls.Load(); calls != 0 {
		t.Fatalf("Close cancelled Runtime %d times, want zero", calls)
	}
}

func TestGatewayTurnCloseUnblocksHalfReadSubscriptionDelivery(t *testing.T) {
	input := make(chan eventstream.Envelope, 2)
	input <- eventstream.Envelope{Kind: eventstream.KindNotice, Notice: "first"}
	input <- eventstream.Envelope{Kind: eventstream.KindNotice, Notice: "second"}
	close(input)
	handle := newBrokerTestHandle(nil)
	turn := &gatewayTurn{
		handle:       handle,
		subscription: &errorFeedSubscription{events: input},
	}
	out := turn.Events()
	if got := <-out; got.Notice != "first" {
		t.Fatalf("first output = %#v", got)
	}
	deadline := time.Now().Add(time.Second)
	for len(input) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(input) != 0 {
		t.Fatal("relay did not enter blocked second delivery")
	}

	if err := turn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("Close delivered the blocked second event")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not terminate half-read output")
	}
	if calls := handle.cancelCalls.Load(); calls != 0 {
		t.Fatalf("Close cancelled Runtime %d times, want zero", calls)
	}
}

func TestGatewayTurnSubmitApprovalForwardsRequestID(t *testing.T) {
	handle := &testGatewayTurnHandle{}
	turn := newGatewayTurn(handle)

	err := turn.SubmitApproval(context.Background(), ApprovalDecision{
		RequestID:  "approval-child-1",
		Outcome:    "selected",
		OptionID:   "allow_once",
		Approved:   true,
		Reason:     " approved ",
		ReviewText: " reviewed ",
	})
	if err != nil {
		t.Fatalf("SubmitApproval() error = %v", err)
	}
	if len(handle.submitted) != 1 {
		t.Fatalf("gateway submissions = %#v, want one", handle.submitted)
	}
	got := handle.submitted[0]
	if got.Kind != kernel.SubmissionKindApproval || got.Approval == nil {
		t.Fatalf("gateway submission = %#v, want approval", got)
	}
	if got.Approval.RequestID != "approval-child-1" || got.Approval.OptionID != "allow_once" || got.Approval.Reason != "approved" || got.Approval.ReviewText != "reviewed" {
		t.Fatalf("gateway approval = %#v, want exact request id and normalized decision", got.Approval)
	}
}

type testGatewayTurnHandle struct {
	acpEvents <-chan eventstream.Envelope
	submitted []kernel.SubmitRequest
}

type errorFeedSubscription struct {
	events <-chan eventstream.Envelope
	err    error
}

func (s *errorFeedSubscription) Events() <-chan eventstream.Envelope { return s.events }
func (*errorFeedSubscription) Backfill() <-chan eventstream.Envelope {
	done := make(chan eventstream.Envelope)
	close(done)
	return done
}
func (*errorFeedSubscription) BackfillDone() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}
func (*errorFeedSubscription) Close() error       { return nil }
func (s *errorFeedSubscription) Err() error       { return s.err }
func (*errorFeedSubscription) LastCursor() string { return "" }

func (h *testGatewayTurnHandle) HandleID() string { return "handle-1" }
func (h *testGatewayTurnHandle) RunID() string    { return "run-1" }
func (h *testGatewayTurnHandle) TurnID() string   { return "turn-1" }
func (h *testGatewayTurnHandle) SessionRef() session.SessionRef {
	return session.SessionRef{SessionID: "session-1"}
}
func (h *testGatewayTurnHandle) CreatedAt() time.Time { return time.Time{} }
func (h *testGatewayTurnHandle) Submit(_ context.Context, req kernel.SubmitRequest) error {
	h.submitted = append(h.submitted, req)
	return nil
}
func (h *testGatewayTurnHandle) Cancel() kernel.CancelResult { return kernel.CancelResult{} }
func (h *testGatewayTurnHandle) Close() error                { return nil }
func (h *testGatewayTurnHandle) ACPEvents() <-chan eventstream.Envelope {
	return h.acpEvents
}

func collectAdapterTurnEvents(events <-chan eventstream.Envelope) []eventstream.Envelope {
	var out []eventstream.Envelope
	for env := range events {
		out = append(out, env)
	}
	return out
}

func assertAdapterLifecycleState(t *testing.T, env eventstream.Envelope, state string) {
	t.Helper()
	if !eventstream.IsTerminalLifecycle(env) {
		t.Fatalf("env = %#v, want terminal lifecycle", env)
	}
	if env.Lifecycle == nil || env.Lifecycle.State != state {
		t.Fatalf("lifecycle = %#v, want state %q", env.Lifecycle, state)
	}
}
