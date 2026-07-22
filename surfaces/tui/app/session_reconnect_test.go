package tuiapp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestExecuteReconnectTreatsHistoryAsTranscriptAndRestoresApproval(t *testing.T) {
	backfill := make(chan eventstream.Envelope, 1)
	backfill <- eventstream.TurnCompleted("old-handle", "old-run", "old-turn", time.Unix(10, 0))
	close(backfill)
	live := make(chan eventstream.Envelope, 1)
	live <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(20, 0))
	close(live)
	reconnect := &tuiReconnect{
		state: controlclient.SessionState{
			SessionID: "session-1", ResumeMode: controlclient.ResumeModeExact,
			Run: controlclient.RunState{Active: true, HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"},
			Approval: controlclient.ApprovalState{Active: &controlclient.ActiveApproval{
				RequestID:  "approval-original",
				Permission: &session.ProtocolApproval{ToolCall: session.ProtocolToolCall{ID: "call-1", Name: "Bash"}},
			}},
		},
		backfill: backfill,
		live:     live,
		bootstrap: []eventstream.Envelope{{
			Kind: eventstream.KindRequestPermission, SessionID: "session-1",
			HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1",
			ApprovalRequestID: "approval-original",
			Permission: &schema.RequestPermissionRequest{
				SessionID: "session-1", ToolCall: schema.ToolCallUpdate{ToolCallID: "call-1"},
				Options: []schema.PermissionOption{{OptionID: "allow_once", Name: "Allow once", Kind: "allow_once"}},
			},
		}},
	}
	var mu sync.Mutex
	var messages []any
	sender := &ProgramSender{Send: func(message tea.Msg) {
		mu.Lock()
		messages = append(messages, message)
		mu.Unlock()
		if prompt, ok := message.(PromptRequestMsg); ok {
			prompt.Response <- PromptResponse{Line: "allow_once"}
		}
	}}
	result := executeControlPromptResult(context.Background(), nil, sender, controlprompt.Result{
		Handled: true, ClearHistory: true, Reconnect: reconnect, SuppressTurnDivider: true,
	})
	if !result.queued {
		t.Fatalf("execute reconnect result = %#v, want live terminal queued", result)
	}
	deadline := time.Now().Add(time.Second)
	for {
		reconnect.mu.Lock()
		decisions := append([]control.ApprovalDecision(nil), reconnect.decisions...)
		reconnect.mu.Unlock()
		if len(decisions) > 0 {
			if decisions[0].RequestID != "approval-original" || decisions[0].OptionID != "allow_once" {
				t.Fatalf("approval decisions = %#v", decisions)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for resumed approval submission")
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(messages) == 0 {
		t.Fatal("no reconnect messages were forwarded")
	}
	if _, ok := messages[0].(SessionReconnectMsg); !ok {
		t.Fatalf("first reconnect message = %T, want atomic SessionReconnectMsg", messages[0])
	}
}

func TestForwardSessionReconnectPreservesFeedGapAndDoesNotCompleteTurn(t *testing.T) {
	t.Parallel()

	live := make(chan eventstream.Envelope)
	close(live)
	gap := &controlclient.FeedGapError{
		Cause:        controlclient.ErrSlowConsumer,
		RetryCursor:  "retry-cursor",
		Mode:         controlclient.ResumeModeDurableFallback,
		TransientGap: true,
	}
	reconnect := &tuiReconnect{
		state: controlclient.SessionState{
			SessionID: "session-1",
			Run: controlclient.RunState{
				Active: true, HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1",
			},
		},
		live: live,
		err:  gap,
	}
	var messages []tea.Msg
	result := forwardSessionReconnectEventStream(context.Background(), reconnect, &ProgramSender{Send: func(message tea.Msg) {
		messages = append(messages, message)
	}})
	if !result.queued || len(messages) != 1 {
		t.Fatalf("result = %#v, messages = %#v, want one queued interrupted terminal", result, messages)
	}
	terminal, ok := messages[0].(eventstream.Envelope)
	if !ok || !eventstream.IsTerminalLifecycle(terminal) || terminal.Lifecycle.State != eventstream.LifecycleStateInterrupted {
		t.Fatalf("terminal = %#v, want interrupted lifecycle", messages[0])
	}
	var gotGap *controlclient.FeedGapError
	if !errors.As(terminal.Err, &gotGap) || gotGap.RetryCursor != gap.RetryCursor || gotGap.Mode != gap.Mode || !gotGap.TransientGap {
		t.Fatalf("terminal error = %#v, want typed feed gap with retry cursor", terminal.Err)
	}
}

type tuiReconnect struct {
	mu        sync.Mutex
	state     controlclient.SessionState
	backfill  <-chan eventstream.Envelope
	live      <-chan eventstream.Envelope
	bootstrap []eventstream.Envelope
	decisions []control.ApprovalDecision
	err       error
}

func (r *tuiReconnect) State() controlclient.SessionState { return r.state }
func (r *tuiReconnect) HandleID() string                  { return r.state.Run.HandleID }
func (r *tuiReconnect) RunID() string                     { return r.state.Run.RunID }
func (r *tuiReconnect) TurnID() string                    { return r.state.Run.TurnID }
func (r *tuiReconnect) Backfill() <-chan eventstream.Envelope {
	return r.backfill
}
func (r *tuiReconnect) Events() <-chan eventstream.Envelope { return r.live }
func (r *tuiReconnect) BackfillDone() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}
func (r *tuiReconnect) BootstrapEvents() []eventstream.Envelope {
	return eventstream.CloneEnvelopes(r.bootstrap)
}
func (r *tuiReconnect) SubmitApproval(_ context.Context, decision control.ApprovalDecision) error {
	r.mu.Lock()
	r.decisions = append(r.decisions, decision)
	r.mu.Unlock()
	return nil
}
func (*tuiReconnect) Cancel()      {}
func (*tuiReconnect) Close() error { return nil }
func (r *tuiReconnect) Err() error { return r.err }
