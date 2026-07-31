package tuiapp

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func TestForwardTurnEventStreamPreservesTypedClientClosureError(t *testing.T) {
	events := make(chan eventstream.Envelope)
	close(events)
	want := errors.New("feed gap requires reconnect")
	turn := &errorReportingControlTurn{events: events, err: want}
	var messages []tea.Msg
	result := forwardTurnEventStream(context.Background(), turn, &ProgramSender{
		Send: func(message tea.Msg) { messages = append(messages, message) },
	})
	if !result.queued || len(messages) != 1 {
		t.Fatalf("result/messages = %#v / %#v", result, messages)
	}
	terminal, ok := messages[0].(eventstream.Envelope)
	if !ok ||
		terminal.Lifecycle == nil ||
		terminal.Lifecycle.State != eventstream.LifecycleStateInterrupted ||
		!errors.Is(terminal.Err, want) {
		t.Fatalf("terminal = %#v", messages[0])
	}
}

type errorReportingControlTurn struct {
	events <-chan eventstream.Envelope
	err    error
}

func (*errorReportingControlTurn) HandleID() string { return "handle-1" }
func (*errorReportingControlTurn) RunID() string    { return "run-1" }
func (*errorReportingControlTurn) TurnID() string   { return "turn-1" }
func (t *errorReportingControlTurn) Events() <-chan eventstream.Envelope {
	return t.events
}
func (*errorReportingControlTurn) SubmitApproval(context.Context, controlprompt.ApprovalDecision) error {
	return nil
}
func (*errorReportingControlTurn) Cancel()      {}
func (*errorReportingControlTurn) Close() error { return nil }
func (t *errorReportingControlTurn) Err() error { return t.err }

var _ controlprompt.Turn = (*errorReportingControlTurn)(nil)
