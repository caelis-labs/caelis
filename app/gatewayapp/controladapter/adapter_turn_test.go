package controladapter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlclientport "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func TestGatewayTurnEventsSynthesizesCompletedForEmptyStream(t *testing.T) {
	events := make(chan eventstream.Envelope)
	close(events)
	turn := newGatewayTurn(&testGatewayTurnHandle{acpEvents: events}, nil)

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
	turn := newGatewayTurn(&testGatewayTurnHandle{acpEvents: events}, nil)

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
	turn := newGatewayTurn(&testGatewayTurnHandle{acpEvents: events}, nil)

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
	turn := newGatewayTurn(&testGatewayTurnHandle{acpEvents: acpEvents}, nil)

	out := collectAdapterTurnEvents(turn.Events())
	if len(out) != 1 {
		t.Fatalf("events = %#v, want first terminal only", out)
	}
	assertAdapterLifecycleState(t, out[0], eventstream.LifecycleStateCompleted)
}

func TestGatewayTurnEventsReturnsSameStream(t *testing.T) {
	events := make(chan eventstream.Envelope)
	close(events)
	turn := newGatewayTurn(&testGatewayTurnHandle{acpEvents: events}, nil)

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
		subscription: &errorFeedSubscription{events: events, err: controlclientport.ErrSlowConsumer},
	}

	out := collectAdapterTurnEvents(turn.Events())
	if len(out) != 3 {
		t.Fatalf("events = %#v, want child envelope, delivery error, interrupted terminal", out)
	}
	if out[0].Scope != eventstream.ScopeSubagent || out[1].Kind != eventstream.KindError || !errors.Is(out[1].Err, controlclientport.ErrSlowConsumer) {
		t.Fatalf("subscription failure sequence = %#v", out)
	}
	assertAdapterLifecycleState(t, out[2], eventstream.LifecycleStateInterrupted)
}

func TestGatewayTurnSubmitApprovalForwardsRequestID(t *testing.T) {
	handle := &testGatewayTurnHandle{}
	turn := newGatewayTurn(handle, nil)

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
	if got.Kind != gateway.SubmissionKindApproval || got.Approval == nil {
		t.Fatalf("gateway submission = %#v, want approval", got)
	}
	if got.Approval.RequestID != "approval-child-1" || got.Approval.OptionID != "allow_once" || got.Approval.Reason != "approved" || got.Approval.ReviewText != "reviewed" {
		t.Fatalf("gateway approval = %#v, want exact request id and normalized decision", got.Approval)
	}
}

type testGatewayTurnHandle struct {
	acpEvents <-chan eventstream.Envelope
	submitted []gateway.SubmitRequest
}

type errorFeedSubscription struct {
	events <-chan eventstream.Envelope
	err    error
}

func (s *errorFeedSubscription) Events() <-chan eventstream.Envelope { return s.events }
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
func (h *testGatewayTurnHandle) Submit(_ context.Context, req gateway.SubmitRequest) error {
	h.submitted = append(h.submitted, req)
	return nil
}
func (h *testGatewayTurnHandle) Cancel() gateway.CancelResult { return gateway.CancelResult{} }
func (h *testGatewayTurnHandle) Close() error                 { return nil }
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
