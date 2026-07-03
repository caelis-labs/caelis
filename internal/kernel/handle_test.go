package kernel

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/ports/agent"
	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/ports/model"
	"github.com/caelis-labs/caelis/ports/session"
	"github.com/caelis-labs/caelis/ports/tool"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestTurnHandleReplaysEventstreamAfterCursor(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
	handle.publishACP(eventstream.Envelope{Kind: eventstream.KindNotice, Notice: "one"}, "")
	handle.publishACP(eventstream.Envelope{Kind: eventstream.KindNotice, Notice: "two"}, "")

	all, next, err := handle.eventsAfter("")
	if err != nil {
		t.Fatalf("eventsAfter() error = %v", err)
	}
	if len(all) != 2 || next != all[1].Cursor {
		t.Fatalf("eventsAfter() = %#v, %q, want both events and latest cursor", all, next)
	}
	replayed, next, err := handle.eventsAfter(all[0].Cursor)
	if err != nil {
		t.Fatalf("eventsAfter(cursor) error = %v", err)
	}
	if len(replayed) != 1 || replayed[0].Notice != "two" || next != replayed[0].Cursor {
		t.Fatalf("eventsAfter(cursor) = %#v, %q, want second event", replayed, next)
	}
}

func TestTurnHandleACPEventsProjectsCanonicalAndPassesThroughTransient(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
	acpEvents := handle.ACPEvents()
	msg := model.NewTextMessage(model.RoleAssistant, "done")
	handle.publishSessionEvent(&session.Event{ID: "e1", Type: session.EventTypeAssistant, Message: &msg})
	handle.publishACP(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: schema.RawUpdate{
			SessionUpdate: "vendor/custom",
		},
	}, "acp_passthrough")
	handle.finish()

	got := drainACPEvents(acpEvents)
	if len(got) != 2 {
		t.Fatalf("ACPEvents() produced %d events, want 2: %#v", len(got), got)
	}
	if update, ok := got[0].Update.(schema.ContentChunk); !ok || update.SessionUpdate != schema.UpdateAgentMessage {
		t.Fatalf("first ACP update = %#v, want projected assistant chunk", got[0].Update)
	}
	if update, ok := got[1].Update.(schema.RawUpdate); !ok || update.SessionUpdate != "vendor/custom" {
		t.Fatalf("second ACP update = %#v, want passthrough raw update", got[1].Update)
	}
	if got[0].Cursor != "h1-acp-000001" || got[1].Cursor != "h1-acp-000002" {
		t.Fatalf("ACP cursors = %q, %q, want per-handle monotonic cursors", got[0].Cursor, got[1].Cursor)
	}
	if got[0].EventID != "e1" || got[0].ProjectionID != "acp-projection:ZTE:0" {
		t.Fatalf("projected ACP source ids = event:%q projection:%q, want e1 projection cursor", got[0].EventID, got[0].ProjectionID)
	}
	if got[1].EventID != "" || got[1].ProjectionID != "" {
		t.Fatalf("passthrough source ids = event:%q projection:%q, want empty", got[1].EventID, got[1].ProjectionID)
	}
}

func TestTurnHandleCanSuppressCanonicalProjectionForNativePassthrough(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
	msg := model.NewTextMessage(model.RoleAssistant, "done")
	handle.publishSessionEventWithACPProjection(&session.Event{ID: "e1", Type: session.EventTypeAssistant, Message: &msg}, false)
	handle.publishACP(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: schema.RawUpdate{
			SessionUpdate: "vendor/custom",
		},
	}, "acp_passthrough")

	replayed, _, err := handle.eventsAfter("")
	if err != nil {
		t.Fatalf("eventsAfter() error = %v", err)
	}
	if len(replayed) != 1 {
		t.Fatalf("eventsAfter() = %#v, want only native passthrough", replayed)
	}
	if update, ok := replayed[0].Update.(schema.RawUpdate); !ok || update.SessionUpdate != "vendor/custom" {
		t.Fatalf("ACP update = %#v, want native raw passthrough", replayed[0].Update)
	}
}

func TestTurnHandlePublishesApprovalAsACPPermission(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
	handle.publishApproval(&agent.ApprovalRequest{
		Tool: tool.Definition{Name: "RUN_COMMAND"},
		Call: tool.Call{ID: "call-1", Input: []byte(`{"command":"go test ./..."}`)},
		Approval: &session.ProtocolApproval{
			Options: []session.ProtocolApprovalOption{{
				ID:   "allow_once",
				Name: "Allow once",
				Kind: "allow_once",
			}},
		},
	})
	replayed, _, err := handle.eventsAfter("")
	if err != nil {
		t.Fatalf("eventsAfter() error = %v", err)
	}
	if len(replayed) != 1 || replayed[0].Kind != eventstream.KindRequestPermission || replayed[0].Permission == nil {
		t.Fatalf("approval events = %#v, want request_permission", replayed)
	}
	permission := replayed[0].Permission
	if permission.ToolCall.ToolCallID != "call-1" || stringPtrValue(permission.ToolCall.Kind) != schema.ToolKindExecute {
		t.Fatalf("permission tool call = %#v, want execute call-1", permission.ToolCall)
	}
	if got := gateway.EventMetaString(permission.ToolCall.Meta, gateway.EventMetaRoot, gateway.EventMetaRuntime, gateway.EventMetaRuntimeTool, gateway.EventMetaRuntimeToolName); got != "RUN_COMMAND" {
		t.Fatalf("permission tool meta = %#v, want RUN_COMMAND tool name", permission.ToolCall.Meta)
	}
	if len(permission.Options) != 1 || permission.Options[0].OptionID != "allow_once" {
		t.Fatalf("permission options = %#v, want allow_once", permission.Options)
	}
}

func TestTurnHandlePublishErrorUsesEventstreamError(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
	handle.publishError(errors.New("provider failed"))
	replayed, _, err := handle.eventsAfter("")
	if err != nil {
		t.Fatalf("eventsAfter() error = %v", err)
	}
	if len(replayed) != 1 || replayed[0].Kind != eventstream.KindError || replayed[0].Error != "provider failed" {
		t.Fatalf("error event = %#v, want eventstream error", replayed)
	}
	if replayed[0].SessionID != "s1" || replayed[0].HandleID != "h1" || replayed[0].RunID != "run-1" || replayed[0].TurnID != "turn-1" {
		t.Fatalf("error IDs = %#v", replayed[0])
	}
}

func stringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func TestTurnHandleSubmitRoutesApprovalAndContinuation(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
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

func TestTurnHandleSubmitNormalizesConversationSubmissions(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(turnHandleConfig{
		handleID:   "h1",
		runID:      "run-1",
		turnID:     "turn-1",
		sessionRef: session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws"},
		createdAt:  time.Unix(100, 0),
		prepareSubmission: func(_ context.Context, req SubmitRequest) (SubmitRequest, error) {
			req.Text = "projected follow up"
			req.DisplayText = "$cmpctl follow up"
			return req, nil
		},
	})
	runner := &recordingRunner{}
	handle.setRunner(runner)

	if err := handle.Submit(context.Background(), SubmitRequest{
		Kind: SubmissionKindConversation,
		Text: "$cmpctl follow up",
	}); err != nil {
		t.Fatalf("Submit(conversation) error = %v", err)
	}
	if got := len(runner.submissions); got != 1 {
		t.Fatalf("runner submissions = %#v, want one", runner.submissions)
	}
	if got := runner.submissions[0]; got.Text != "projected follow up" || got.DisplayInput != "$cmpctl follow up" {
		t.Fatalf("runner submission = %#v, want normalized text/display", got)
	}
}

func TestTurnHandleSubmitRejectsUnknownSubmissionKind(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
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

func TestTurnHandlePublishDoesNotBlockWithoutSubscriber(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 96; i++ {
			handle.publishACP(eventstream.Envelope{Kind: eventstream.KindNotice, Notice: fmt.Sprintf("event-%d", i)}, "")
		}
		handle.finish()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publish blocked without a live subscriber")
	}

	replayed, next, err := handle.eventsAfter("")
	if err != nil {
		t.Fatalf("eventsAfter() error = %v", err)
	}
	if len(replayed) != 96 || next != replayed[len(replayed)-1].Cursor {
		t.Fatalf("replayed len/next = %d/%q, want 96/latest", len(replayed), next)
	}
}

func TestTurnHandleLiveStreamDoesNotDropApprovalWhenConsumerIsSlow(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
	for i := 0; i < 96; i++ {
		handle.publishACP(eventstream.Envelope{Kind: eventstream.KindNotice, Notice: fmt.Sprintf("event-%d", i)}, "")
	}
	handle.publishApproval(&agent.ApprovalRequest{
		Tool: tool.Definition{Name: "RUN_COMMAND"},
		Call: tool.Call{ID: "call-1"},
	})
	handle.finish()

	deadline := time.After(time.Second)
	events := handle.ACPEvents()
	for {
		select {
		case env, ok := <-events:
			if !ok {
				t.Fatal("live events closed before approval request was delivered")
			}
			if env.Kind == eventstream.KindRequestPermission {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for approval request from slow-consumer live stream")
		}
	}
}

func TestTurnHandleSubmitRejectsUnsupportedWithoutRunner(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
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

func TestTurnHandleApprovalSubmitRejectsWithoutPendingRequest(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
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

	handle := newTestTurnHandle()
	handle.publishACP(eventstream.Envelope{Kind: eventstream.KindNotice, Notice: "one"}, "")

	_, _, err := handle.eventsAfter("missing")
	if err == nil {
		t.Fatal("eventsAfter() error = nil, want cursor_not_found")
	}
	var gwErr *Error
	if !As(err, &gwErr) || gwErr.Code != CodeCursorNotFound {
		t.Fatalf("eventsAfter() error = %v, want cursor_not_found", err)
	}
}

func newTestTurnHandle() *turnHandle {
	return newTurnHandle(turnHandleConfig{
		handleID: "h1",
		runID:    "run-1",
		turnID:   "turn-1",
		sessionRef: session.SessionRef{
			AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
		},
		createdAt: time.Unix(100, 0),
	})
}

func drainACPEvents(events <-chan eventstream.Envelope) []eventstream.Envelope {
	var out []eventstream.Envelope
	for env := range events {
		out = append(out, env)
	}
	return out
}
