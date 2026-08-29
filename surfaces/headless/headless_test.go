package headless

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestRunSessionOnceDrainsAssistantOutput(t *testing.T) {
	t.Parallel()

	handle := newFakeACPHandle([]eventstream.Envelope{
		{
			Cursor: "e1",
			Kind:   eventstream.KindSessionUpdate,
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage,
				Content:       eventstream.TextContent{Type: "text", Text: "done"},
			},
		},
		{
			Cursor:         "u1",
			Kind:           eventstream.KindSessionUpdate,
			UsageSemantics: eventstream.UsageSemanticsProviderUsage,
			Update:         eventstream.UsageUpdateFromSnapshot(session.UsageSnapshot{PromptTokens: 11, TotalTokens: 17}, nil),
		},
	})
	gw := fakeStarter{
		turn: handle,
	}

	result, err := RunSessionOnce(context.Background(), gw, appserver.SessionTurnStartRequest{SessionID: "session-1", Input: "hello"}, Options{})
	if err != nil {
		t.Fatalf("RunSessionOnce() error = %v", err)
	}
	if result.Output != "done" {
		t.Fatalf("RunSessionOnce() output = %q, want %q", result.Output, "done")
	}
	if result.PromptTokens != 11 {
		t.Fatalf("RunSessionOnce() prompt tokens = %d, want %d", result.PromptTokens, 11)
	}
}

func TestRunSessionOnceAppendsPrefixGrowingACPMessageDeltasExactly(t *testing.T) {
	t.Parallel()

	handle := newFakeACPHandle([]eventstream.Envelope{
		{
			Kind: eventstream.KindSessionUpdate,
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage,
				MessageID:     "message-1",
				Content:       eventstream.TextContent{Type: "text", Text: "a"},
			},
		},
		{
			Kind: eventstream.KindSessionUpdate,
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage,
				MessageID:     "message-1",
				Content:       eventstream.TextContent{Type: "text", Text: "ab"},
			},
		},
	})
	result, err := RunSessionOnce(context.Background(), fakeStarter{turn: handle}, appserver.SessionTurnStartRequest{SessionID: "session-1", Input: "hello"}, Options{})
	if err != nil {
		t.Fatalf("RunSessionOnce() error = %v", err)
	}
	if result.Output != "aab" {
		t.Fatalf("RunSessionOnce() output = %q, want exact ACP deltas aab", result.Output)
	}
}

func TestRunSessionOnceReplacesTransientAssistantWithCanonicalFinal(t *testing.T) {
	t.Parallel()

	handle := newFakeACPHandle([]eventstream.Envelope{
		{
			Kind:     eventstream.KindSessionUpdate,
			Scope:    eventstream.ScopeMain,
			Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage,
				Content:       eventstream.TextContent{Type: "text", Text: "provisional"},
			},
		},
		{
			Kind:         eventstream.KindSessionUpdate,
			EventID:      "assistant-1",
			ProjectionID: "acp-projection:YXNzaXN0YW50LTE:0",
			Scope:        eventstream.ScopeMain,
			Final:        true,
			Delivery:     &eventstream.Delivery{Mode: eventstream.DeliveryCanonical},
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage,
				Content:       eventstream.TextContent{Type: "text", Text: "canonical answer"},
			},
		},
	})
	result, err := RunSessionOnce(context.Background(), fakeStarter{turn: handle}, appserver.SessionTurnStartRequest{SessionID: "session-1", Input: "hello"}, Options{})
	if err != nil {
		t.Fatalf("RunSessionOnce() error = %v", err)
	}
	if result.Output != "canonical answer" {
		t.Fatalf("RunSessionOnce() output = %q, want canonical replacement", result.Output)
	}
}

func TestRunSessionOncePreservesIdenticalAssistantDeltas(t *testing.T) {
	t.Parallel()

	handle := newFakeACPHandle([]eventstream.Envelope{
		{
			Kind: eventstream.KindSessionUpdate,
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage,
				MessageID:     "message-1",
				Content:       eventstream.TextContent{Type: "text", Text: "ha"},
			},
		},
		{
			Kind: eventstream.KindSessionUpdate,
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage,
				MessageID:     "message-1",
				Content:       eventstream.TextContent{Type: "text", Text: "ha"},
			},
		},
	})
	result, err := RunSessionOnce(context.Background(), fakeStarter{turn: handle}, appserver.SessionTurnStartRequest{SessionID: "session-1", Input: "hello"}, Options{})
	if err != nil {
		t.Fatalf("RunSessionOnce() error = %v", err)
	}
	if result.Output != "haha" {
		t.Fatalf("RunSessionOnce() output = %q, want both exact deltas", result.Output)
	}
}

func TestRunSessionOnceKeepsSDKOnlyAssistantWithoutDurableIdentity(t *testing.T) {
	t.Parallel()

	handle := newFakeACPHandle([]eventstream.Envelope{{
		Kind:     eventstream.KindSessionUpdate,
		Scope:    eventstream.ScopeMain,
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentMessage,
			Content:       eventstream.TextContent{Type: "text", Text: "sdk-only"},
		},
	}})
	result, err := RunSessionOnce(context.Background(), fakeStarter{turn: handle}, appserver.SessionTurnStartRequest{SessionID: "session-1", Input: "hello"}, Options{})
	if err != nil {
		t.Fatalf("RunSessionOnce() error = %v", err)
	}
	if result.Output != "sdk-only" {
		t.Fatalf("RunSessionOnce() output = %q, want identity-free SDK output", result.Output)
	}
}

func TestRunSessionOnceIgnoresScopedTraceOutput(t *testing.T) {
	t.Parallel()

	handle := newFakeACPHandle([]eventstream.Envelope{
		{
			Cursor: "main-1",
			Kind:   eventstream.KindSessionUpdate,
			Scope:  eventstream.ScopeMain,
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage,
				Content:       eventstream.TextContent{Type: "text", Text: "main answer"},
			},
		},
		{
			Cursor:         "usage-main",
			Kind:           eventstream.KindSessionUpdate,
			Scope:          eventstream.ScopeMain,
			UsageSemantics: eventstream.UsageSemanticsProviderUsage,
			Update:         eventstream.UsageUpdateFromSnapshot(session.UsageSnapshot{PromptTokens: 11, TotalTokens: 17}, nil),
		},
		{
			Cursor:  "child-1",
			Kind:    eventstream.KindSessionUpdate,
			Scope:   eventstream.ScopeSubagent,
			ScopeID: "task-1",
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage,
				Content:       eventstream.TextContent{Type: "text", Text: "child trace"},
			},
		},
		{
			Cursor:         "usage-child",
			Kind:           eventstream.KindSessionUpdate,
			Scope:          eventstream.ScopeSubagent,
			UsageSemantics: eventstream.UsageSemanticsProviderUsage,
			Update:         eventstream.UsageUpdateFromSnapshot(session.UsageSnapshot{PromptTokens: 99}, nil),
		},
	})
	gw := fakeStarter{
		turn: handle,
	}

	result, err := RunSessionOnce(context.Background(), gw, appserver.SessionTurnStartRequest{SessionID: "session-1", Input: "hello"}, Options{})
	if err != nil {
		t.Fatalf("RunSessionOnce() error = %v", err)
	}
	if result.Output != "main answer" {
		t.Fatalf("RunSessionOnce() output = %q, want main answer", result.Output)
	}
	if result.PromptTokens != 11 {
		t.Fatalf("RunSessionOnce() prompt tokens = %d, want main-scope usage", result.PromptTokens)
	}
}

func TestRunSessionOnceAutoDeniesApprovalByDefault(t *testing.T) {
	t.Parallel()

	title := "RunCommand"
	handle := newFakeACPHandle([]eventstream.Envelope{
		{
			Cursor:            "a1",
			Kind:              eventstream.KindRequestPermission,
			ApprovalRequestID: eventstream.ApprovalRequestID("approval-1"),
			Permission: &eventstream.RequestPermissionRequest{
				SessionID: "s1",
				ToolCall: eventstream.ToolCallUpdate{
					SessionUpdate: eventstream.UpdateToolCallInfo,
					ToolCallID:    "call-1",
					Title:         &title,
				},
			},
		},
	})
	gw := fakeStarter{
		turn: handle,
	}

	if _, err := RunSessionOnce(context.Background(), gw, appserver.SessionTurnStartRequest{SessionID: "session-1", Input: "hello"}, Options{}); err != nil {
		t.Fatalf("RunSessionOnce() error = %v", err)
	}
	if len(handle.submissions) != 1 {
		t.Fatalf("submissions = %d, want 1", len(handle.submissions))
	}
	if got := handle.submissions[0]; got.Approved {
		t.Fatalf("approval submission = %+v, want auto-deny", got)
	}
}

func TestRunSessionOnceApprovalCallbackReceivesPromptFields(t *testing.T) {
	t.Parallel()

	title := "RunCommand"
	permission := &eventstream.RequestPermissionRequest{
		SessionID: "s1",
		ToolCall: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Title:         &title,
			RawInput: map[string]any{
				"command":             "go test ./...",
				"approval_reason":     "needs execution",
				"justification":       "requested by user",
				"sandbox_permissions": "host",
				"nested": map[string]any{
					"value": "original",
				},
			},
		},
		Options: []acpsdk.PermissionOption{{
			OptionId: "allow_once",
			Name:     "Allow once",
			Kind:     acpsdk.PermissionOptionKindAllowOnce,
		}},
	}
	handle := newFakeACPHandle([]eventstream.Envelope{
		{
			Cursor:            "a1",
			Kind:              eventstream.KindRequestPermission,
			ApprovalRequestID: eventstream.ApprovalRequestID("approval-2"),
			Permission:        permission,
		},
	})
	gw := fakeStarter{
		turn: handle,
	}
	called := false
	_, err := RunSessionOnce(context.Background(), gw, appserver.SessionTurnStartRequest{SessionID: "session-1", Input: "hello"}, Options{
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
			if len(req.Payload.Options) != 1 || req.Payload.Options[0] != (approval.Option{ID: "allow_once", Name: "Allow once", Kind: "allow_once"}) {
				t.Fatalf("approval options = %#v, want copied allow_once option", req.Payload.Options)
			}
			nested, ok := req.Payload.RawInput["nested"].(map[string]any)
			if !ok {
				t.Fatalf("approval nested input = %#v, want map", req.Payload.RawInput["nested"])
			}
			nested["value"] = "changed"
			return approval.Decision{Approved: true, Outcome: string(approval.StatusApproved)}, nil
		},
	})
	if err != nil {
		t.Fatalf("RunSessionOnce() error = %v", err)
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
	rawInput, ok := permission.ToolCall.RawInput.(map[string]any)
	if !ok {
		t.Fatalf("source raw input = %#v, want map", permission.ToolCall.RawInput)
	}
	nested, ok := rawInput["nested"].(map[string]any)
	if !ok || nested["value"] != "original" {
		t.Fatalf("source nested input = %#v, want isolated original value", rawInput["nested"])
	}
}

func TestApprovalPayloadFromPermissionNil(t *testing.T) {
	t.Parallel()

	if payload := approvalPayloadFromPermission(nil); payload != nil {
		t.Fatalf("approvalPayloadFromPermission(nil) = %#v, want nil", payload)
	}
}

func TestRunSessionOnceIgnoresAutomaticApprovalReviewEvents(t *testing.T) {
	t.Parallel()

	handle := newFakeACPHandle([]eventstream.Envelope{
		{
			Cursor: "r1",
			Kind:   eventstream.KindApprovalReview,
			ApprovalReview: &eventstream.ApprovalReview{
				ToolName: "RunCommand",
				Status:   string(approval.ReviewStatusInProgress),
			},
		},
		{
			Cursor: "r2",
			Kind:   eventstream.KindSessionUpdate,
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage,
				Content:       eventstream.TextContent{Type: "text", Text: "done"},
			},
		},
	})
	gw := fakeStarter{
		turn: handle,
	}

	result, err := RunSessionOnce(context.Background(), gw, appserver.SessionTurnStartRequest{SessionID: "session-1", Input: "hello"}, Options{})
	if err != nil {
		t.Fatalf("RunSessionOnce() error = %v", err)
	}
	if result.Output != "done" {
		t.Fatalf("RunSessionOnce() output = %q, want done", result.Output)
	}
	if len(handle.submissions) != 0 {
		t.Fatalf("submissions = %d, want no manual decision for auto-review event", len(handle.submissions))
	}
}

func TestRunSessionOnceProjectsTargetedResultAndStructuredObservation(t *testing.T) {
	t.Parallel()

	target := appserver.TurnTarget{
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
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage,
				Content:       eventstream.TextContent{Type: "text", Text: "done"},
			},
		},
		{
			Kind:           eventstream.KindSessionUpdate,
			SessionID:      "session-1",
			HandleID:       target.HandleID,
			RunID:          target.RunID,
			TurnID:         target.TurnID,
			Cursor:         "cursor-usage",
			UsageSemantics: eventstream.UsageSemanticsProviderUsage,
			Update: eventstream.UsageUpdateFromSnapshot(session.UsageSnapshot{
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
		appserver.SessionTurnStartRequest{SessionID: "session-1", Input: "hello"},
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

	target := appserver.TurnTarget{
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
		appserver.SessionTurnStartRequest{SessionID: "session-1", Input: "hello"},
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

	target := appserver.TurnTarget{
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
		appserver.SessionTurnStartRequest{SessionID: "session-1", Input: "hello"},
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
	turn appserver.SessionTurn
	err  error
}

func (f fakeStarter) Start(context.Context, appserver.SessionTurnStartRequest) (appserver.SessionTurn, error) {
	return f.turn, f.err
}

type fakeTurnHandle struct {
	acpEvents   <-chan eventstream.Envelope
	submissions []appserver.ApprovalResolution
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
func (*fakeTurnHandle) SessionID() string                        { return "session-1" }
func (h *fakeTurnHandle) Target() appserver.TurnTarget {
	return appserver.TurnTarget{HandleID: h.HandleID(), RunID: h.RunID(), TurnID: h.TurnID()}
}
func (h *fakeTurnHandle) Events() <-chan eventstream.Envelope { return h.acpEvents }
func (h *fakeTurnHandle) ResolveApproval(_ context.Context, decision appserver.ApprovalResolution) error {
	h.submissions = append(h.submissions, decision)
	return nil
}
func (*fakeTurnHandle) Steer(context.Context, string, string, []model.ContentPart) error { return nil }
func (*fakeTurnHandle) Cancel(context.Context, string) error                             { return nil }
func (*fakeTurnHandle) LastCursor() string                                               { return "" }
func (*fakeTurnHandle) Err() error                                                       { return nil }
func (*fakeTurnHandle) Close() error                                                     { return nil }

type fakeSessionTurnStarter struct {
	turn appserver.SessionTurn
	err  error
}

func (f fakeSessionTurnStarter) Start(
	context.Context,
	appserver.SessionTurnStartRequest,
) (appserver.SessionTurn, error) {
	return f.turn, f.err
}

type fakeSessionTurn struct {
	target    appserver.TurnTarget
	events    <-chan eventstream.Envelope
	last      string
	cancelErr error
	cancelled bool
	closed    bool
}

func newFakeSessionTurn(
	target appserver.TurnTarget,
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
func (t *fakeSessionTurn) Target() appserver.TurnTarget {
	return t.target
}
func (t *fakeSessionTurn) Events() <-chan eventstream.Envelope { return t.events }
func (*fakeSessionTurn) ResolveApproval(context.Context, appserver.ApprovalResolution) error {
	return nil
}
func (*fakeSessionTurn) Steer(context.Context, string, string, []model.ContentPart) error {
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
