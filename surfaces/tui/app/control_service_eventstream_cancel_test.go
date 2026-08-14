package tuiapp

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func TestForwardTurnEventStreamDetachesWithoutCancellingOnSurfaceShutdown(t *testing.T) {
	t.Parallel()

	turn := &surfaceDetachTurn{events: make(chan eventstream.Envelope)}
	ctx, cancel := context.WithCancel(context.Background())
	messages := make(chan tea.Msg, 4)
	result := make(chan executeLineResult, 1)
	go func() {
		result <- forwardTurnEventStream(ctx, turn, &ProgramSender{Send: func(message tea.Msg) {
			messages <- message
		}})
	}()

	cancel()
	select {
	case got := <-result:
		if !got.queued {
			t.Fatalf("forward result = %#v, want detached queued result", got)
		}
	case <-time.After(time.Second):
		t.Fatal("forwarder did not detach after surface shutdown")
	}
	select {
	case message := <-messages:
		t.Fatalf("surface shutdown synthesized message %#v", message)
	case <-time.After(30 * time.Millisecond):
	}
	if calls := turn.cancelCalls.Load(); calls != 0 {
		t.Fatalf("Turn.Cancel calls = %d, want zero for detach", calls)
	}
}

type surfaceDetachTurn struct {
	events      chan eventstream.Envelope
	cancelCalls atomic.Int32
}

func (*surfaceDetachTurn) HandleID() string { return "handle-detach" }
func (*surfaceDetachTurn) RunID() string    { return "run-detach" }
func (*surfaceDetachTurn) TurnID() string   { return "turn-detach" }

func (t *surfaceDetachTurn) Events() <-chan eventstream.Envelope { return t.events }

func (*surfaceDetachTurn) SubmitApproval(context.Context, controlprompt.ApprovalDecision) error {
	return nil
}

func (t *surfaceDetachTurn) Cancel() {
	t.cancelCalls.Add(1)
}

func (*surfaceDetachTurn) Close() error { return nil }
