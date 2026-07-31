package headless

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestRunOnceDrainsAssistantOutput(t *testing.T) {
	t.Parallel()

	handle := newFakeACPHandle([]eventstream.Envelope{
		{
			Cursor: "e1",
			Kind:   eventstream.KindSessionUpdate,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				Content:       schema.TextContent{Type: "text", Text: "done"},
			},
		},
		{
			Cursor: "u1",
			Kind:   eventstream.KindSessionUpdate,
			Update: eventstream.UsageUpdateFromSnapshot(eventstream.UsageSnapshot{PromptTokens: 11, TotalTokens: 17}, nil),
		},
	})
	gw := fakeStarter{
		turn: handle,
	}

	result, err := RunOnce(context.Background(), gw, controlprompt.Submission{Text: "hello"}, Options{})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if result.Output != "done" {
		t.Fatalf("RunOnce() output = %q, want %q", result.Output, "done")
	}
	if result.PromptTokens != 11 {
		t.Fatalf("RunOnce() prompt tokens = %d, want %d", result.PromptTokens, 11)
	}
}

func TestRunOnceAppendsPrefixGrowingACPMessageDeltasExactly(t *testing.T) {
	t.Parallel()

	handle := newFakeACPHandle([]eventstream.Envelope{
		{
			Kind: eventstream.KindSessionUpdate,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				MessageID:     "message-1",
				Content:       schema.TextContent{Type: "text", Text: "a"},
			},
		},
		{
			Kind: eventstream.KindSessionUpdate,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				MessageID:     "message-1",
				Content:       schema.TextContent{Type: "text", Text: "ab"},
			},
		},
	})
	result, err := RunOnce(context.Background(), fakeStarter{turn: handle}, controlprompt.Submission{Text: "hello"}, Options{})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if result.Output != "aab" {
		t.Fatalf("RunOnce() output = %q, want exact ACP deltas aab", result.Output)
	}
}

func TestRunOnceReplacesTransientAssistantWithCanonicalFinal(t *testing.T) {
	t.Parallel()

	handle := newFakeACPHandle([]eventstream.Envelope{
		{
			Kind:     eventstream.KindSessionUpdate,
			Scope:    eventstream.ScopeMain,
			Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				Content:       schema.TextContent{Type: "text", Text: "provisional"},
			},
		},
		{
			Kind:         eventstream.KindSessionUpdate,
			EventID:      "assistant-1",
			ProjectionID: eventstream.FormatProjectionID("assistant-1", 0),
			Scope:        eventstream.ScopeMain,
			Final:        true,
			Delivery:     &eventstream.Delivery{Mode: eventstream.DeliveryCanonical},
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				Content:       schema.TextContent{Type: "text", Text: "canonical answer"},
			},
		},
	})
	result, err := RunOnce(context.Background(), fakeStarter{turn: handle}, controlprompt.Submission{Text: "hello"}, Options{})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if result.Output != "canonical answer" {
		t.Fatalf("RunOnce() output = %q, want canonical replacement", result.Output)
	}
}

func TestRunOncePreservesIdenticalAssistantDeltas(t *testing.T) {
	t.Parallel()

	handle := newFakeACPHandle([]eventstream.Envelope{
		{
			Kind: eventstream.KindSessionUpdate,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				MessageID:     "message-1",
				Content:       schema.TextContent{Type: "text", Text: "ha"},
			},
		},
		{
			Kind: eventstream.KindSessionUpdate,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				MessageID:     "message-1",
				Content:       schema.TextContent{Type: "text", Text: "ha"},
			},
		},
	})
	result, err := RunOnce(context.Background(), fakeStarter{turn: handle}, controlprompt.Submission{Text: "hello"}, Options{})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if result.Output != "haha" {
		t.Fatalf("RunOnce() output = %q, want both exact deltas", result.Output)
	}
}

func TestRunOnceKeepsSDKOnlyAssistantWithoutDurableIdentity(t *testing.T) {
	t.Parallel()

	handle := newFakeACPHandle([]eventstream.Envelope{{
		Kind:     eventstream.KindSessionUpdate,
		Scope:    eventstream.ScopeMain,
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "sdk-only"},
		},
	}})
	result, err := RunOnce(context.Background(), fakeStarter{turn: handle}, controlprompt.Submission{Text: "hello"}, Options{})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if result.Output != "sdk-only" {
		t.Fatalf("RunOnce() output = %q, want identity-free SDK output", result.Output)
	}
}

func TestRunOnceIgnoresScopedTraceOutput(t *testing.T) {
	t.Parallel()

	handle := newFakeACPHandle([]eventstream.Envelope{
		{
			Cursor: "main-1",
			Kind:   eventstream.KindSessionUpdate,
			Scope:  eventstream.ScopeMain,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				Content:       schema.TextContent{Type: "text", Text: "main answer"},
			},
		},
		{
			Cursor: "usage-main",
			Kind:   eventstream.KindSessionUpdate,
			Scope:  eventstream.ScopeMain,
			Update: eventstream.UsageUpdateFromSnapshot(eventstream.UsageSnapshot{PromptTokens: 11, TotalTokens: 17}, nil),
		},
		{
			Cursor:  "child-1",
			Kind:    eventstream.KindSessionUpdate,
			Scope:   eventstream.ScopeSubagent,
			ScopeID: "task-1",
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				Content:       schema.TextContent{Type: "text", Text: "child trace"},
			},
		},
		{
			Cursor: "usage-child",
			Kind:   eventstream.KindSessionUpdate,
			Scope:  eventstream.ScopeSubagent,
			Update: eventstream.UsageUpdateFromSnapshot(eventstream.UsageSnapshot{PromptTokens: 99}, nil),
		},
	})
	gw := fakeStarter{
		turn: handle,
	}

	result, err := RunOnce(context.Background(), gw, controlprompt.Submission{Text: "hello"}, Options{})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if result.Output != "main answer" {
		t.Fatalf("RunOnce() output = %q, want main answer", result.Output)
	}
	if result.PromptTokens != 11 {
		t.Fatalf("RunOnce() prompt tokens = %d, want main-scope usage", result.PromptTokens)
	}
}

func TestRunOnceAutoDeniesApprovalByDefault(t *testing.T) {
	t.Parallel()

	title := "RUN_COMMAND"
	handle := newFakeACPHandle([]eventstream.Envelope{
		{
			Cursor:            "a1",
			Kind:              eventstream.KindRequestPermission,
			ApprovalRequestID: eventstream.ApprovalRequestID("approval-1"),
			Permission: &schema.RequestPermissionRequest{
				SessionID: "s1",
				ToolCall: schema.ToolCallUpdate{
					SessionUpdate: schema.UpdateToolCallInfo,
					ToolCallID:    "call-1",
					Title:         &title,
				},
			},
		},
	})
	gw := fakeStarter{
		turn: handle,
	}

	if _, err := RunOnce(context.Background(), gw, controlprompt.Submission{Text: "hello"}, Options{}); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if len(handle.submissions) != 1 {
		t.Fatalf("submissions = %d, want 1", len(handle.submissions))
	}
	if got := handle.submissions[0]; got.Approved {
		t.Fatalf("approval submission = %+v, want auto-deny", got)
	}
}

func TestRunOnceApprovalCallbackReceivesPromptFields(t *testing.T) {
	t.Parallel()

	title := "RUN_COMMAND"
	handle := newFakeACPHandle([]eventstream.Envelope{
		{
			Cursor:            "a1",
			Kind:              eventstream.KindRequestPermission,
			ApprovalRequestID: eventstream.ApprovalRequestID("approval-2"),
			Permission: &schema.RequestPermissionRequest{
				SessionID: "s1",
				ToolCall: schema.ToolCallUpdate{
					SessionUpdate: schema.UpdateToolCallInfo,
					ToolCallID:    "call-1",
					Title:         &title,
					RawInput: map[string]any{
						"command":             "go test ./...",
						"approval_reason":     "needs execution",
						"justification":       "requested by user",
						"sandbox_permissions": "host",
					},
				},
				Options: []schema.PermissionOption{{
					OptionID: "allow_once",
					Name:     "Allow once",
					Kind:     "allow_once",
				}},
			},
		},
	})
	gw := fakeStarter{
		turn: handle,
	}
	called := false
	_, err := RunOnce(context.Background(), gw, controlprompt.Submission{Text: "hello"}, Options{
		ResolveApproval: func(_ context.Context, req ApprovalRequest) (approval.Decision, error) {
			called = true
			if req.Payload == nil {
				t.Fatal("approval payload = nil")
			}
			if req.RequestID != "approval-2" {
				t.Fatalf("approval request id = %q, want approval-2", req.RequestID)
			}
			if req.Payload.Reason != "needs execution" || req.Payload.Justification != "requested by user" || req.Payload.SandboxPermissions != "host" {
				t.Fatalf("approval fields = (%q, %q, %q), want restored prompt fields", req.Payload.Reason, req.Payload.Justification, req.Payload.SandboxPermissions)
			}
			return approval.Decision{Approved: true, Outcome: string(approval.StatusApproved)}, nil
		},
	})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if !called {
		t.Fatal("ResolveApproval was not called")
	}
	if len(handle.submissions) != 1 || !handle.submissions[0].Approved {
		t.Fatalf("submissions = %#v, want approved decision", handle.submissions)
	}
	if handle.submissions[0].RequestID != "approval-2" {
		t.Fatalf("approval request id = %q, want approval-2", handle.submissions[0].RequestID)
	}
}

func TestRunOnceIgnoresAutomaticApprovalReviewEvents(t *testing.T) {
	t.Parallel()

	handle := newFakeACPHandle([]eventstream.Envelope{
		{
			Cursor: "r1",
			Kind:   eventstream.KindApprovalReview,
			ApprovalReview: &eventstream.ApprovalReview{
				ToolName: "RUN_COMMAND",
				Status:   string(approval.ReviewStatusInProgress),
			},
		},
		{
			Cursor: "r2",
			Kind:   eventstream.KindSessionUpdate,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				Content:       schema.TextContent{Type: "text", Text: "done"},
			},
		},
	})
	gw := fakeStarter{
		turn: handle,
	}

	result, err := RunOnce(context.Background(), gw, controlprompt.Submission{Text: "hello"}, Options{})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if result.Output != "done" {
		t.Fatalf("RunOnce() output = %q, want done", result.Output)
	}
	if len(handle.submissions) != 0 {
		t.Fatalf("submissions = %d, want no manual decision for auto-review event", len(handle.submissions))
	}
}

func TestRunSessionOnceProjectsTargetedResultAndStructuredObservation(t *testing.T) {
	t.Parallel()

	target := controlclient.TurnTarget{
		HandleID: "handle-1",
		RunID:    "run-1",
		TurnID:   "turn-1",
	}
	terminal := eventstream.TurnCompleted(target.HandleID, target.RunID, target.TurnID, time.Now())
	terminal.SessionID = "session-1"
	terminal.Cursor = "cursor-terminal"
	terminal.Lifecycle.StopReason = "end_turn"
	turn := newFakeSessionTurn(target, []eventstream.Envelope{
		{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "session-1",
			HandleID:  target.HandleID,
			RunID:     target.RunID,
			TurnID:    target.TurnID,
			Cursor:    "cursor-output",
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				Content:       schema.TextContent{Type: "text", Text: "done"},
			},
		},
		{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "session-1",
			HandleID:  target.HandleID,
			RunID:     target.RunID,
			TurnID:    target.TurnID,
			Cursor:    "cursor-usage",
			Update: eventstream.UsageUpdateFromSnapshot(eventstream.UsageSnapshot{
				PromptTokens: 11,
				TotalTokens:  17,
			}, nil),
		},
		terminal,
	})
	var observed []eventstream.Envelope
	result, err := RunSessionOnce(
		context.Background(),
		fakeSessionTurnStarter{turn: turn},
		controlclient.SessionTurnStartRequest{SessionID: "session-1", Input: "hello"},
		Options{
			ObserveEnvelope: func(envelope eventstream.Envelope) error {
				observed = append(observed, envelope)
				return nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "done" ||
		result.LastCursor != "cursor-terminal" ||
		result.PromptTokens != 11 ||
		result.Usage.TotalTokens != 17 ||
		result.LifecycleState != eventstream.LifecycleStateCompleted ||
		result.StopReason != "end_turn" ||
		result.Target != target {
		t.Fatalf("RunSessionOnce result = %#v", result)
	}
	if len(observed) != 3 || observed[2].Cursor != "cursor-terminal" {
		t.Fatalf("observed Envelopes = %#v", observed)
	}
	if !turn.closed {
		t.Fatal("Session Turn observation was not closed")
	}
	if turn.cancelled {
		t.Fatal("successful Session Turn was cancelled")
	}
}

func TestRunSessionOnceCancelsAndDrainsAfterStructuredOutputFailure(t *testing.T) {
	t.Parallel()

	target := controlclient.TurnTarget{
		HandleID: "handle-1",
		RunID:    "run-1",
		TurnID:   "turn-1",
	}
	terminal := eventstream.TurnCancelled(target.HandleID, target.RunID, target.TurnID, "cancelled", time.Now())
	terminal.SessionID = "session-1"
	turn := newFakeSessionTurn(target, []eventstream.Envelope{
		{
			Kind:      eventstream.KindNotice,
			SessionID: "session-1",
			HandleID:  target.HandleID,
			RunID:     target.RunID,
			TurnID:    target.TurnID,
			Notice:    "first",
		},
		{
			Kind:      eventstream.KindNotice,
			SessionID: "session-1",
			HandleID:  target.HandleID,
			RunID:     target.RunID,
			TurnID:    target.TurnID,
			Notice:    "must still drain",
		},
		terminal,
	})
	outputErr := errors.New("injected output failure")
	observations := 0
	result, err := RunSessionOnce(
		context.Background(),
		fakeSessionTurnStarter{turn: turn},
		controlclient.SessionTurnStartRequest{SessionID: "session-1", Input: "hello"},
		Options{
			ObserveEnvelope: func(eventstream.Envelope) error {
				observations++
				return outputErr
			},
		},
	)
	if !errors.Is(err, outputErr) {
		t.Fatalf("RunSessionOnce() error = %v, want %v", err, outputErr)
	}
	if observations != 1 {
		t.Fatalf("structured observer calls = %d, want one after first failure", observations)
	}
	if !turn.cancelled || !turn.closed {
		t.Fatalf("Turn cancelled=%t closed=%t", turn.cancelled, turn.closed)
	}
	if result.LifecycleState != eventstream.LifecycleStateCancelled {
		t.Fatalf("result lifecycle = %q, want cancelled", result.LifecycleState)
	}
}

func TestRunSessionOnceCancellationTimeoutDetachesWithoutTerminal(t *testing.T) {
	t.Parallel()

	target := controlclient.TurnTarget{
		HandleID: "handle-1",
		RunID:    "run-1",
		TurnID:   "turn-1",
	}
	cancelErr := errors.New("injected cancel failure")
	turn := &fakeSessionTurn{
		target:    target,
		events:    make(chan eventstream.Envelope),
		cancelErr: cancelErr,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	startedAt := time.Now()
	_, err := runSessionOnce(
		ctx,
		fakeSessionTurnStarter{turn: turn},
		controlclient.SessionTurnStartRequest{SessionID: "session-1", Input: "hello"},
		Options{},
		10*time.Millisecond,
	)
	if !errors.Is(err, context.Canceled) ||
		!errors.Is(err, cancelErr) ||
		!strings.Contains(err.Error(), "timed out waiting for cancelled Turn terminal") {
		t.Fatalf("runSessionOnce() error = %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("runSessionOnce() cancellation took %s", elapsed)
	}
	if !turn.cancelled || !turn.closed {
		t.Fatalf("Turn cancelled=%t closed=%t", turn.cancelled, turn.closed)
	}
}

type fakeStarter struct {
	turn controlprompt.Turn
	err  error
}

func (f fakeStarter) Submit(context.Context, controlprompt.Submission) (controlprompt.Turn, error) {
	return f.turn, f.err
}

type fakeTurnHandle struct {
	acpEvents   <-chan eventstream.Envelope
	submissions []controlprompt.ApprovalDecision
}

func newFakeACPHandle(events []eventstream.Envelope) *fakeTurnHandle {
	ch := make(chan eventstream.Envelope, len(events))
	for _, env := range events {
		ch <- env
	}
	close(ch)
	return &fakeTurnHandle{acpEvents: ch}
}

func (h *fakeTurnHandle) HandleID() string                       { return "h1" }
func (h *fakeTurnHandle) RunID() string                          { return "run-1" }
func (h *fakeTurnHandle) TurnID() string                         { return "turn-1" }
func (h *fakeTurnHandle) ACPEvents() <-chan eventstream.Envelope { return h.acpEvents }
func (h *fakeTurnHandle) Events() <-chan eventstream.Envelope    { return h.acpEvents }
func (h *fakeTurnHandle) SubmitApproval(_ context.Context, decision controlprompt.ApprovalDecision) error {
	h.submissions = append(h.submissions, decision)
	return nil
}
func (h *fakeTurnHandle) Cancel()      {}
func (h *fakeTurnHandle) Close() error { return nil }

type fakeSessionTurnStarter struct {
	turn controlclient.SessionTurn
	err  error
}

func (f fakeSessionTurnStarter) Start(
	context.Context,
	controlclient.SessionTurnStartRequest,
) (controlclient.SessionTurn, error) {
	return f.turn, f.err
}

type fakeSessionTurn struct {
	target    controlclient.TurnTarget
	events    <-chan eventstream.Envelope
	last      string
	cancelErr error
	cancelled bool
	closed    bool
}

func newFakeSessionTurn(
	target controlclient.TurnTarget,
	events []eventstream.Envelope,
) *fakeSessionTurn {
	channel := make(chan eventstream.Envelope, len(events))
	last := ""
	for _, envelope := range events {
		channel <- envelope
		if envelope.Cursor != "" {
			last = envelope.Cursor
		}
	}
	close(channel)
	return &fakeSessionTurn{target: target, events: channel, last: last}
}

func (*fakeSessionTurn) SessionID() string { return "session-1" }
func (t *fakeSessionTurn) Target() controlclient.TurnTarget {
	return t.target
}
func (t *fakeSessionTurn) Events() <-chan eventstream.Envelope { return t.events }
func (*fakeSessionTurn) ResolveApproval(context.Context, controlclient.ApprovalResolution) error {
	return nil
}
func (t *fakeSessionTurn) Cancel(context.Context, string) error {
	t.cancelled = true
	return t.cancelErr
}
func (t *fakeSessionTurn) LastCursor() string { return t.last }
func (*fakeSessionTurn) Err() error           { return nil }
func (t *fakeSessionTurn) Close() error {
	t.closed = true
	return nil
}
