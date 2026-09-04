package tuiapp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/controlprompt"
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

func TestInterruptedLifecycleWithTypedErrorRendersAsFailure(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(120, 0))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Scope:     eventstream.ScopeMain,
		TurnID:    "turn-1",
		Final:     true,
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentMessage,
			Content:       eventstream.TextContent{Type: "text", Text: "Partial response."},
		},
	})

	want := errors.New("feed gap requires reconnect")
	terminal := eventstream.TurnLifecycle(
		"handle-1", "run-1", "turn-1",
		eventstream.LifecycleStateInterrupted, want.Error(), "", time.Unix(121, 0),
	)
	terminal.Err = want
	terminal.Error = want.Error()
	model = applyACPEnvelopeForTest(t, model, terminal)

	block := requireMainACPTurnBlockForTest(t, model)
	if block.Status != eventstream.LifecycleStateFailed {
		t.Fatalf("main turn status = %q, want failure presentation for typed stream error", block.Status)
	}
	model.syncViewportContent()
	plain := strings.Join(model.viewportPlainLines, "\n")
	if strings.Contains(plain, interruptedTurnTitle) || strings.Contains(plain, "not a system error") {
		t.Fatalf("typed stream error rendered as a user interruption:\n%s", plain)
	}
	if !strings.Contains(plain, want.Error()) {
		t.Fatalf("typed stream error missing failure detail:\n%s", plain)
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
