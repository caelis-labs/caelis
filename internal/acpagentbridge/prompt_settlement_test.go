package acpagentbridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

type settlementTurn struct {
	events    chan eventstream.Envelope
	cancelled chan struct{}
	closed    chan struct{}
	err       error
}

func (*settlementTurn) HandleID() string                      { return "handle" }
func (*settlementTurn) RunID() string                         { return "run" }
func (*settlementTurn) TurnID() string                        { return "turn" }
func (t *settlementTurn) Events() <-chan eventstream.Envelope { return t.events }
func (*settlementTurn) SubmitApproval(context.Context, controlprompt.ApprovalDecision) error {
	return nil
}
func (t *settlementTurn) Cancel()      { close(t.cancelled) }
func (t *settlementTurn) Close() error { close(t.closed); return nil }
func (t *settlementTurn) Err() error   { return t.err }

func newSettlementTurn() *settlementTurn {
	return &settlementTurn{events: make(chan eventstream.Envelope), cancelled: make(chan struct{}), closed: make(chan struct{})}
}

func TestPromptProjectionFailureCancelsAndWaitsForProducerTerminal(t *testing.T) {
	turn := newSettlementTurn()
	failure := errors.New("projection failed")
	result := make(chan error, 1)
	go func() { result <- settlePromptTurn(t.Context(), turn, nil, failure) }()
	<-turn.cancelled
	// Successful tool output after the projection failed is still part of the
	// same live execution, not a new activity or a terminal failure.
	turn.events <- eventstream.Envelope{Kind: eventstream.KindNotice, Notice: "tool wrote file"}
	select {
	case err := <-result:
		t.Fatalf("prompt returned before producer terminal: %v", err)
	case <-turn.closed:
		t.Fatal("detached the still-running producer")

	default:
	}
	turn.events <- eventstream.TurnCompleted("handle", "run", "turn", time.Now())
	if err := <-result; !errors.Is(err, failure) {
		t.Fatalf("settled error = %v", err)
	}
	<-turn.closed
}

func TestPromptCancellationWaitsForProducerTerminal(t *testing.T) {
	turn := newSettlementTurn()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result := make(chan error, 1)
	go func() { result <- settlePromptTurn(ctx, turn, nil, ctx.Err()) }()
	<-turn.cancelled
	select {
	case err := <-result:
		t.Fatalf("cancel acceptance completed prompt: %v", err)
	default:
	}
	turn.events <- eventstream.TurnCancelled("handle", "run", "turn", "cancelled", time.Now())
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("settled cancellation = %v", err)
	}
}

func TestPromptCompletedTerminalWinsObservationCancellation(t *testing.T) {
	for _, observed := range []bool{false, true} {
		for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
			turn := newSettlementTurn()
			completed := eventstream.TurnCompleted("handle", "run", "turn", time.Now())
			var terminal *eventstream.Envelope
			if observed {
				terminal = &completed
			} else {
				turn.events = make(chan eventstream.Envelope, 1)
				turn.events <- completed
			}
			if err := settlePromptTurn(t.Context(), turn, terminal, cause); err != nil {
				t.Fatalf("observed=%v cause=%v: completed Turn returned %v", observed, cause, err)
			}
		}
	}
}

func TestPromptLostObservationCannotBecomeCancelledOrSuccessful(t *testing.T) {
	for _, cause := range []error{nil, context.Canceled, errors.New("projection failed")} {
		turn := newSettlementTurn()
		turn.err = context.Canceled
		close(turn.events)
		err := settlePromptTurn(t.Context(), turn, nil, cause)
		if err == nil || errors.Is(err, context.Canceled) {
			t.Fatalf("lost execution classified as success/cancelled: %v", err)
		}
	}
}
