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
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
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

func TestStreamReconnectBackfillCarriesNormalizedObservedSpawnResult(t *testing.T) {
	t.Parallel()

	events := canonicalOutputFidelityEvents()
	backfill := make(chan eventstream.Envelope, len(events))
	for _, event := range events {
		event = roundTripCanonicalOutputFidelityEvent(t, event)
		base := acpprojector.EnvelopeBaseFromSessionEvent(
			session.SessionRef{SessionID: "session-1"},
			event,
			acpprojector.SessionEventTransport{},
		)
		projected := acpprojector.ProjectSessionEventEnvelope(base, event)
		if len(projected) != 1 {
			t.Fatalf("projection for %s = %#v, want one envelope", event.ID, projected)
		}
		backfill <- projected[0]
	}
	close(backfill)

	var messages []TranscriptEventsMsg
	err := streamReconnectBackfill(
		context.Background(),
		&tuiReconnect{backfill: backfill},
		func(message tea.Msg) {
			transcript, ok := message.(TranscriptEventsMsg)
			if !ok {
				t.Fatalf("backfill message = %T, want TranscriptEventsMsg", message)
			}
			messages = append(messages, transcript)
		},
	)
	if err != nil {
		t.Fatalf("stream reconnect backfill: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("backfill messages = %#v, want one ordered terminal-observation batch", messages)
	}
	message := messages[0]
	if len(message.Events) != 2 {
		t.Fatalf("transcript events = %#v, want Spawn call plus hidden Task observation", message.Events)
	}
	if len(message.ObservedSpawnResults) != 1 {
		t.Fatalf("observed Spawn results = %#v, want one normalized terminal child", message.ObservedSpawnResults)
	}
	result := message.ObservedSpawnResults[0]
	if result.ParentCallID != "spawn-call-1" ||
		result.Status != schema.ToolStatusCompleted ||
		result.RawOutput["final_message"] != structuredFinalMessageForFidelityTest {
		t.Fatalf("observed Spawn result = %#v, want exact durable terminal child payload", result)
	}
}

func TestApplySessionReconnectStateAtomicallyResetsTaskStreamSession(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name              string
		reconnectSession  string
		staleBatchSession string
	}{
		{name: "same session", reconnectSession: "session-old", staleBatchSession: "session-old"},
		{name: "different session", reconnectSession: "session-new", staleBatchSession: "session-old"},
	} {
		t.Run(test.name, func(t *testing.T) {
			subscription := newTUIProtocolTaskSubscription()
			model := NewModel(Config{NoColor: true, NoAnimation: true})
			model.currentSessionID = "session-old"
			model.taskStreamWanted["task-old"] = true
			model.taskStreamTokens["task-old"] = 7
			model.taskStreamSubscriptions["task-old"] = subscription
			model.taskStreamCursors["task-old"] = "cursor-old"
			model.taskStreamIDsByHandle["handle-old"] = "task-old"
			model.taskStreamHandlesByID["task-old"] = "handle-old"
			model.taskStreamNextToken = 7
			model.runningActivityTracker.observeOwner("handle-old", runningActivityOwner{
				Key: "owner-old", CallID: "call-old", Target: runningTargetSubagent,
			})

			model.applySessionReconnectState(controlclient.SessionState{SessionID: test.reconnectSession})

			if model.currentSessionID != test.reconnectSession {
				t.Fatalf("current Session = %q, want %q", model.currentSessionID, test.reconnectSession)
			}
			if _, open := <-subscription.events; open {
				t.Fatal("old Task-stream subscription remains open after reconnect")
			}
			if len(model.taskStreamWanted) != 0 ||
				len(model.taskStreamTokens) != 0 ||
				len(model.taskStreamSubscriptions) != 0 ||
				len(model.taskStreamCursors) != 0 ||
				len(model.taskStreamIDsByHandle) != 0 ||
				len(model.taskStreamHandlesByID) != 0 {
				t.Fatalf("Task-stream state survived reconnect: wanted=%v tokens=%v subscriptions=%v cursors=%v ids=%v handles=%v",
					model.taskStreamWanted,
					model.taskStreamTokens,
					model.taskStreamSubscriptions,
					model.taskStreamCursors,
					model.taskStreamIDsByHandle,
					model.taskStreamHandlesByID,
				)
			}
			if model.taskStreamNextToken != 7 {
				t.Fatalf("Task-stream token generation = %d, want monotonic 7", model.taskStreamNextToken)
			}
			if len(model.runningActivityTracker.ownersByHandle) != 0 ||
				len(model.runningActivityTracker.ownersByCallID) != 0 {
				t.Fatalf("old activity owner index survived reconnect: handles=%v calls=%v",
					model.runningActivityTracker.ownersByHandle,
					model.runningActivityTracker.ownersByCallID,
				)
			}

			next, _ := model.handleTaskStreamBatch(taskStreamBatchMsg{
				sessionID: test.staleBatchSession,
				taskID:    "task-old",
				token:     7,
				events: []eventstream.Envelope{{
					Kind: eventstream.KindSessionUpdate, SessionID: test.staleBatchSession,
					Update: schema.ContentChunk{SessionUpdate: schema.UpdateAgentMessage},
				}},
			})
			model = next.(*Model)
			if len(model.doc.Blocks()) != 0 {
				t.Fatalf("stale Task-stream batch rendered %d blocks after reconnect", len(model.doc.Blocks()))
			}

			model.runningActivityTracker.observeOwner("handle-backfill", runningActivityOwner{
				Key: "owner-backfill", CallID: "call-backfill", Target: runningTargetSubagent,
			})
			model.observeTaskStreamSession(eventstream.Envelope{
				SessionID: test.reconnectSession,
				Scope:     eventstream.ScopeMain,
			})
			if got := model.runningActivityTracker.ownersByHandle["handle-backfill"].Key; got != "owner-backfill" {
				t.Fatalf("first live Envelope reset backfill activity owner = %q, want owner-backfill", got)
			}
		})
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
