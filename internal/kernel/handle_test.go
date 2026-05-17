package kernel

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func TestTurnHandleReplaysEventsAfterCursor(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(turnHandleConfig{
		handleID: "h1",
		runID:    "run-1",
		turnID:   "turn-1",
		sessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		createdAt: time.Unix(100, 0),
	})
	handle.publishSessionEvent(&session.Event{ID: "e1", Type: session.EventTypeUser})
	handle.publishSessionEvent(&session.Event{ID: "e2", Type: session.EventTypeAssistant})

	replayed, next, err := handle.EventsAfter("e1")
	if err != nil {
		t.Fatalf("EventsAfter() error = %v", err)
	}
	if len(replayed) != 1 || replayed[0].Cursor != "e2" || next != "e2" {
		t.Fatalf("EventsAfter() = %#v, %q, want only e2", replayed, next)
	}
}

func TestTurnHandleCanonicalizesAssistantEventAndUsage(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(turnHandleConfig{
		handleID: "h1",
		runID:    "run-1",
		turnID:   "turn-1",
		sessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		createdAt: time.Unix(100, 0),
	})
	handle.publishSessionEvent(&session.Event{
		ID:   "e1",
		Type: session.EventTypeAssistant,
		Text: "done",
		Meta: map[string]any{
			"usage": map[string]any{
				"prompt_tokens":       12,
				"cached_input_tokens": 7,
				"completion_tokens":   5,
				"total_tokens":        17,
			},
		},
	})

	replayed, next, err := handle.EventsAfter("")
	if err != nil {
		t.Fatalf("EventsAfter() error = %v", err)
	}
	if len(replayed) != 1 || next != "e1" {
		t.Fatalf("EventsAfter() = %#v, %q", replayed, next)
	}
	if replayed[0].Event.Kind != EventKindAssistantMessage {
		t.Fatalf("event kind = %q, want %q", replayed[0].Event.Kind, EventKindAssistantMessage)
	}
	if got := AssistantText(replayed[0].Event); got != "done" {
		t.Fatalf("AssistantText() = %q, want %q", got, "done")
	}
	if replayed[0].Event.Usage == nil || replayed[0].Event.Usage.PromptTokens != 12 || replayed[0].Event.Usage.CachedInputTokens != 7 || replayed[0].Event.Usage.CompletionTokens != 5 || replayed[0].Event.Usage.TotalTokens != 17 {
		t.Fatalf("usage = %+v", replayed[0].Event.Usage)
	}
	if replayed[0].Event.Narrative == nil {
		t.Fatal("event narrative = nil, want canonical narrative payload")
	}
	if replayed[0].Event.Narrative.Role != NarrativeRoleAssistant {
		t.Fatalf("event narrative role = %q, want %q", replayed[0].Event.Narrative.Role, NarrativeRoleAssistant)
	}
	if replayed[0].Event.Narrative.Text != "done" {
		t.Fatalf("event narrative text = %q, want %q", replayed[0].Event.Narrative.Text, "done")
	}
}

func TestTurnHandleCanonicalizesApprovalEvent(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(turnHandleConfig{
		handleID: "h1",
		runID:    "run-1",
		turnID:   "turn-1",
		sessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		createdAt: time.Unix(100, 0),
	})
	wait := handle.publishApproval(&agent.ApprovalRequest{
		Tool: tool.Definition{Name: "bash"},
	})
	if wait == nil {
		t.Fatal("publishApproval() returned nil wait channel")
	}

	replayed, _, err := handle.EventsAfter("")
	if err != nil {
		t.Fatalf("EventsAfter() error = %v", err)
	}
	if len(replayed) != 1 {
		t.Fatalf("EventsAfter() len = %d, want 1", len(replayed))
	}
	if replayed[0].Event.ApprovalPayload == nil {
		t.Fatal("approval payload = nil, want canonical approval payload")
	}
	if replayed[0].Event.ApprovalPayload.ToolName != "bash" {
		t.Fatalf("approval payload tool name = %q, want %q", replayed[0].Event.ApprovalPayload.ToolName, "bash")
	}
	if replayed[0].Event.Origin == nil || replayed[0].Event.Origin.Scope != EventScopeMain || replayed[0].Event.Origin.ScopeID != "s1" {
		t.Fatalf("approval origin = %+v, want main session scope", replayed[0].Event.Origin)
	}
}

func TestTurnHandleAnchorsSubagentApprovalToParentTool(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(turnHandleConfig{
		handleID: "h1",
		runID:    "run-1",
		turnID:   "turn-1",
		sessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		createdAt: time.Unix(100, 0),
	})
	handle.publishApprovalReviewPayload(&agent.ApprovalRequest{
		Tool: tool.Definition{Name: "request_permissions"},
		Metadata: map[string]any{
			"subagent":       true,
			"scope_id":       "task-1",
			"parent_call_id": "spawn-1",
			"parent_tool":    "SPAWN",
		},
	}, &ApprovalPayload{
		ToolName:     "request_permissions",
		ReviewStatus: ApprovalReviewStatusApproved,
		ReviewText:   "Automatic approval review approved",
	})

	replayed, _, err := handle.EventsAfter("")
	if err != nil {
		t.Fatalf("EventsAfter() error = %v", err)
	}
	if len(replayed) != 1 {
		t.Fatalf("EventsAfter() len = %d, want 1", len(replayed))
	}
	event := replayed[0].Event
	if event.Origin == nil || event.Origin.Scope != EventScopeSubagent || event.Origin.ScopeID != "task-1" {
		t.Fatalf("approval origin = %+v, want subagent task scope", event.Origin)
	}
	if got := EventMetaString(event.Meta, EventMetaRoot, EventMetaRuntime, EventMetaRuntimeStream, EventMetaRuntimeStreamParentCallID); got != "spawn-1" {
		t.Fatalf("parent_call_id meta = %q, want spawn-1", got)
	}
	if got := EventMetaString(event.Meta, EventMetaRoot, EventMetaRuntime, EventMetaRuntimeStream, EventMetaRuntimeStreamParentTool); got != "SPAWN" {
		t.Fatalf("parent_tool meta = %q, want SPAWN", got)
	}
}

func TestTurnHandleSubmitRoutesApprovalAndContinuation(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(turnHandleConfig{
		handleID: "h1",
		runID:    "run-1",
		turnID:   "turn-1",
		sessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		createdAt: time.Unix(100, 0),
	})
	runner := &recordingRunner{}
	handle.setRunner(runner)

	if err := handle.Submit(context.Background(), SubmitRequest{
		Kind: SubmissionKindConversation,
		Text: "follow up",
	}); err != nil {
		t.Fatalf("Submit(conversation) error = %v", err)
	}
	if got := len(runner.submissions); got != 1 || runner.submissions[0].Text != "follow up" {
		t.Fatalf("runner submissions = %#v", runner.submissions)
	}

	wait := handle.setPendingApproval()
	if err := handle.Submit(context.Background(), SubmitRequest{
		Kind:     SubmissionKindApproval,
		Approval: &ApprovalDecision{Approved: true, Outcome: string(ApprovalStatusApproved)},
	}); err != nil {
		t.Fatalf("Submit(approval) error = %v", err)
	}
	resp := <-wait
	if !resp.Approved || resp.Outcome != string(ApprovalStatusApproved) {
		t.Fatalf("approval response = %+v", resp)
	}
}

func TestTurnHandleSubmitRejectsUnknownSubmissionKind(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(turnHandleConfig{
		handleID: "h1",
		runID:    "run-1",
		turnID:   "turn-1",
		sessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		createdAt: time.Unix(100, 0),
	})
	handle.setRunner(&recordingRunner{})

	err := handle.Submit(context.Background(), SubmitRequest{
		Kind: SubmissionKind("debug"),
		Text: "follow up",
	})
	if err == nil {
		t.Fatal("Submit() error = nil, want invalid request")
	}
	var gwErr *Error
	if !As(err, &gwErr) || gwErr.Code != CodeInvalidRequest {
		t.Fatalf("Submit() error = %v, want invalid_request", err)
	}
}

func TestTurnHandleCancelCancelsContextAndRunner(t *testing.T) {
	t.Parallel()

	var contextCancelled bool
	handle := newTurnHandle(turnHandleConfig{
		handleID: "h1",
		runID:    "run-1",
		turnID:   "turn-1",
		sessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		createdAt: time.Unix(100, 0),
		cancel: func() bool {
			contextCancelled = true
			return true
		},
	})
	runner := &recordingRunner{}
	handle.setRunner(runner)

	result := handle.Cancel()
	if result.Status != agent.CancelStatusCancelled {
		t.Fatalf("Cancel().Status = %q, want %q", result.Status, agent.CancelStatusCancelled)
	}
	if !contextCancelled {
		t.Fatal("gateway context was not cancelled")
	}
	if !runner.cancelled {
		t.Fatal("runner was not cancelled")
	}
	result = handle.Cancel()
	if result.Status != agent.CancelStatusAlreadyCancelled {
		t.Fatalf("Cancel(second).Status = %q, want %q", result.Status, agent.CancelStatusAlreadyCancelled)
	}
}

func TestTurnHandleSetRunnerAfterCancelCancelsRunner(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(turnHandleConfig{
		handleID: "h1",
		runID:    "run-1",
		turnID:   "turn-1",
		sessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		createdAt: time.Unix(100, 0),
	})
	result := handle.Cancel()
	if result.Status != agent.CancelStatusCancelled {
		t.Fatalf("Cancel().Status = %q, want %q", result.Status, agent.CancelStatusCancelled)
	}
	runner := &recordingRunner{}
	handle.setRunner(runner)
	if !runner.cancelled {
		t.Fatal("late runner was not cancelled")
	}
}

func TestTurnHandleCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(turnHandleConfig{
		handleID: "h1",
		runID:    "run-1",
		turnID:   "turn-1",
		sessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		createdAt: time.Unix(100, 0),
	})
	if err := handle.Close(); err != nil {
		t.Fatalf("Close(first) error = %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("Close(second) error = %v", err)
	}
}

func TestTurnHandleCloseAfterFinishDoesNotDoubleClose(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(turnHandleConfig{
		handleID: "h1",
		runID:    "run-1",
		turnID:   "turn-1",
		sessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		createdAt: time.Unix(100, 0),
	})

	handle.finish()
	if err := handle.Close(); err != nil {
		t.Fatalf("Close(after finish) error = %v", err)
	}
}

func TestTurnHandlePublishDoesNotBlockWhenEventChannelIsFull(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(turnHandleConfig{
		handleID: "h1",
		runID:    "run-1",
		turnID:   "turn-1",
		sessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		createdAt: time.Unix(100, 0),
	})

	done := make(chan struct{})
	go func() {
		for i := 0; i < 64; i++ {
			handle.publishSessionEvent(&session.Event{ID: fmt.Sprintf("e%d", i), Type: session.EventTypeAssistant})
		}
		handle.finish()
		handle.publishSessionEvent(&session.Event{ID: "late", Type: session.EventTypeAssistant})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publish blocked with a full event channel")
	}

	replayed, next, err := handle.EventsAfter("")
	if err != nil {
		t.Fatalf("EventsAfter() error = %v", err)
	}
	if len(replayed) != 65 || next != "late" {
		t.Fatalf("replayed len/next = %d/%q, want 65/late", len(replayed), next)
	}
}

func TestTurnHandleDoesNotStartLiveDispatcherWithoutSubscriber(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(turnHandleConfig{
		handleID: "h1",
		runID:    "run-1",
		turnID:   "turn-1",
		sessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		createdAt: time.Unix(100, 0),
	})
	for i := 0; i < 96; i++ {
		handle.publishSessionEvent(&session.Event{ID: fmt.Sprintf("e%d", i), Type: session.EventTypeAssistant})
	}
	handle.finish()

	handle.mu.Lock()
	started := handle.eventsStarted
	closed := handle.eventsClosed
	handle.mu.Unlock()
	if started || closed {
		t.Fatalf("live dispatcher state = started:%t closed:%t, want unopened lazy stream", started, closed)
	}
	replayed, next, err := handle.EventsAfter("")
	if err != nil {
		t.Fatalf("EventsAfter() error = %v", err)
	}
	if len(replayed) != 96 || next != "e95" {
		t.Fatalf("replayed len/next = %d/%q, want 96/e95", len(replayed), next)
	}
}

func TestTurnHandleLiveStreamDoesNotDropApprovalWhenConsumerIsSlow(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(turnHandleConfig{
		handleID: "h1",
		runID:    "run-1",
		turnID:   "turn-1",
		sessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		createdAt: time.Unix(100, 0),
	})
	for i := 0; i < 96; i++ {
		handle.publishSessionEvent(&session.Event{ID: fmt.Sprintf("e%d", i), Type: session.EventTypeAssistant})
	}
	handle.publishApproval(&agent.ApprovalRequest{Tool: tool.Definition{Name: "bash"}})
	handle.finish()

	deadline := time.After(time.Second)
	for {
		select {
		case env, ok := <-handle.Events():
			if !ok {
				t.Fatal("live events closed before approval request was delivered")
			}
			if env.Event.Kind == EventKindApprovalRequested {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for approval request from slow-consumer live stream")
		}
	}
}

func TestTurnHandleSubmitRejectsUnsupportedWithoutRunner(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(turnHandleConfig{
		handleID: "h1",
		runID:    "run-1",
		turnID:   "turn-1",
		sessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		createdAt: time.Unix(100, 0),
	})
	err := handle.Submit(context.Background(), SubmitRequest{
		Kind: SubmissionKindConversation,
		Text: "follow up",
	})
	if err == nil {
		t.Fatal("Submit() error = nil, want unsupported")
	}
	var gwErr *Error
	if !As(err, &gwErr) || gwErr.Code != CodeSubmissionUnsupported {
		t.Fatalf("Submit() error = %v, want submission unsupported", err)
	}
}

func TestTurnHandlePendingSubmissionRespectsCancellation(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(turnHandleConfig{
		handleID:                "h1",
		runID:                   "run-1",
		turnID:                  "turn-1",
		activeKind:              ActiveTurnKindKernel,
		sessionRef:              session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws"},
		createdAt:               time.Unix(100, 0),
		allowPendingSubmissions: true,
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := handle.Submit(ctx, SubmitRequest{
		Kind: SubmissionKindConversation,
		Text: "stale follow up",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Submit() error = %v, want context canceled", err)
	}
	if len(handle.pendingSubmissions) != 0 {
		t.Fatalf("pendingSubmissions = %#v, want empty after canceled submit", handle.pendingSubmissions)
	}
}

func TestTurnHandleCancelledBeforeRunnerDropsPendingSubmissions(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(turnHandleConfig{
		handleID:                "h1",
		runID:                   "run-1",
		turnID:                  "turn-1",
		activeKind:              ActiveTurnKindKernel,
		sessionRef:              session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws"},
		createdAt:               time.Unix(100, 0),
		allowPendingSubmissions: true,
	})
	if err := handle.Submit(context.Background(), SubmitRequest{
		Kind: SubmissionKindConversation,
		Text: "queued follow up",
	}); err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	handle.Cancel()
	runner := &recordingRunner{}
	handle.setRunner(runner)

	if got := len(runner.submissions); got != 0 {
		t.Fatalf("runner submissions = %#v, want canceled pending submission dropped", runner.submissions)
	}
}

func TestTurnHandleApprovalSubmitRejectsWithoutPendingRequest(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(turnHandleConfig{
		handleID: "h1",
		runID:    "run-1",
		turnID:   "turn-1",
		sessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		createdAt: time.Unix(100, 0),
	})

	err := handle.Submit(context.Background(), SubmitRequest{
		Kind:     SubmissionKindApproval,
		Approval: &ApprovalDecision{Approved: true, Outcome: string(ApprovalStatusApproved)},
	})
	if err == nil {
		t.Fatal("Submit(approval) error = nil, want approval-not-pending")
	}
	var gwErr *Error
	if !As(err, &gwErr) || gwErr.Code != CodeApprovalNotPending {
		t.Fatalf("Submit(approval) error = %v, want approval_not_pending", err)
	}
}

func TestTurnHandleEventsAfterReturnsCursorNotFound(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(turnHandleConfig{
		handleID: "h1",
		runID:    "run-1",
		turnID:   "turn-1",
		sessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		createdAt: time.Unix(100, 0),
	})
	handle.publishSessionEvent(&session.Event{ID: "e1", Type: session.EventTypeUser})

	_, _, err := handle.EventsAfter("missing")
	if err == nil {
		t.Fatal("EventsAfter() error = nil, want cursor_not_found")
	}
	var gwErr *Error
	if !As(err, &gwErr) || gwErr.Code != CodeCursorNotFound {
		t.Fatalf("EventsAfter() error = %v, want cursor_not_found", err)
	}
}

var _ agent.Runner = (*recordingRunner)(nil)
