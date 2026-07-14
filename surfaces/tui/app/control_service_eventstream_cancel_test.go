package tuiapp

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func TestForwardTurnEventStreamWaitsForAuthoritativeCancelTerminal(t *testing.T) {
	t.Parallel()

	turn := newCancelBarrierTurn()
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
	case <-turn.cancelRequested:
	case <-time.After(time.Second):
		t.Fatal("TUI context cancellation did not call Turn.Cancel")
	}
	select {
	case message := <-messages:
		t.Fatalf("message before producer barrier = %#v, want no synthetic terminal", message)
	case <-time.After(30 * time.Millisecond):
	}
	select {
	case <-result:
		t.Fatal("forwarder returned before Runtime producer barrier")
	default:
	}

	close(turn.releaseProducer)
	message := receiveCancelBarrierMessage(t, messages)
	terminal, ok := message.(eventstream.Envelope)
	if !ok || !eventstream.IsTerminalLifecycle(terminal) || terminal.Lifecycle.State != eventstream.LifecycleStateCancelled {
		t.Fatalf("post-barrier message = %#v, want authoritative cancelled terminal", message)
	}
	select {
	case <-turn.producerDone:
	default:
		t.Fatal("cancel terminal arrived before producer completion")
	}
	select {
	case got := <-result:
		if !got.queued {
			t.Fatalf("forward result = %#v, want queued", got)
		}
	case <-time.After(time.Second):
		t.Fatal("forwarder did not return after authoritative terminal")
	}
	select {
	case duplicate := <-messages:
		t.Fatalf("duplicate post-barrier message = %#v", duplicate)
	default:
	}
	if calls := turn.cancelCalls.Load(); calls != 1 {
		t.Fatalf("Turn.Cancel calls = %d, want one", calls)
	}
}

func receiveCancelBarrierMessage(t *testing.T, messages <-chan tea.Msg) tea.Msg {
	t.Helper()
	select {
	case message := <-messages:
		return message
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for authoritative terminal")
		return nil
	}
}

type cancelBarrierTurn struct {
	events          chan eventstream.Envelope
	cancelRequested chan struct{}
	releaseProducer chan struct{}
	producerDone    chan struct{}
	cancelOnce      sync.Once
	cancelCalls     atomic.Int32
}

func newCancelBarrierTurn() *cancelBarrierTurn {
	return &cancelBarrierTurn{
		events:          make(chan eventstream.Envelope, 1),
		cancelRequested: make(chan struct{}),
		releaseProducer: make(chan struct{}),
		producerDone:    make(chan struct{}),
	}
}

func (*cancelBarrierTurn) HandleID() string { return "handle-cancel" }
func (*cancelBarrierTurn) RunID() string    { return "run-cancel" }
func (*cancelBarrierTurn) TurnID() string   { return "turn-cancel" }

func (t *cancelBarrierTurn) Events() <-chan eventstream.Envelope { return t.events }

func (*cancelBarrierTurn) SubmitApproval(context.Context, control.ApprovalDecision) error {
	return nil
}

func (t *cancelBarrierTurn) Cancel() {
	t.cancelCalls.Add(1)
	t.cancelOnce.Do(func() {
		close(t.cancelRequested)
		go func() {
			<-t.releaseProducer
			close(t.producerDone)
			t.events <- eventstream.TurnCancelled(t.HandleID(), t.RunID(), t.TurnID(), "turn cancelled", time.Now())
			close(t.events)
		}()
	})
}

func (*cancelBarrierTurn) Close() error { return nil }
