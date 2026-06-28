package controladapter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
)

func TestGatewayTurnEventsSynthesizesCompletedForEmptyStream(t *testing.T) {
	events := make(chan eventstream.Envelope)
	close(events)
	turn := &gatewayTurn{handle: &testGatewayTurnHandle{acpEvents: events}}

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
	turn := &gatewayTurn{handle: &testGatewayTurnHandle{acpEvents: events}}

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
	turn := &gatewayTurn{handle: &testGatewayTurnHandle{acpEvents: events}}

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
	turn := &gatewayTurn{handle: &testGatewayTurnHandle{acpEvents: acpEvents}}

	out := collectAdapterTurnEvents(turn.Events())
	if len(out) != 1 {
		t.Fatalf("events = %#v, want first terminal only", out)
	}
	assertAdapterLifecycleState(t, out[0], eventstream.LifecycleStateCompleted)
}

func TestGatewayTurnEventsReturnsSameStream(t *testing.T) {
	events := make(chan eventstream.Envelope)
	close(events)
	turn := &gatewayTurn{handle: &testGatewayTurnHandle{acpEvents: events}}

	first := turn.Events()
	second := turn.Events()
	if first != second {
		t.Fatal("Events() returned different channels; want single-consumer stream")
	}
}

type testGatewayTurnHandle struct {
	acpEvents <-chan eventstream.Envelope
}

func (h *testGatewayTurnHandle) HandleID() string { return "handle-1" }
func (h *testGatewayTurnHandle) RunID() string    { return "run-1" }
func (h *testGatewayTurnHandle) TurnID() string   { return "turn-1" }
func (h *testGatewayTurnHandle) SessionRef() session.SessionRef {
	return session.SessionRef{SessionID: "session-1"}
}
func (h *testGatewayTurnHandle) CreatedAt() time.Time { return time.Time{} }
func (h *testGatewayTurnHandle) Submit(context.Context, gateway.SubmitRequest) error {
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
