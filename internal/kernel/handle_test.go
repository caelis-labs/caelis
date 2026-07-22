package kernel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
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
	pending, err := handle.publishApproval(&agent.ApprovalRequest{
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
	if err != nil {
		t.Fatalf("publishApproval() error = %v", err)
	}
	replayed, _, err := handle.eventsAfter("")
	if err != nil {
		t.Fatalf("eventsAfter() error = %v", err)
	}
	if len(replayed) != 1 || replayed[0].Kind != eventstream.KindRequestPermission || replayed[0].Permission == nil {
		t.Fatalf("approval events = %#v, want request_permission", replayed)
	}
	permission := replayed[0].Permission
	if replayed[0].ApprovalRequestID != pending.id {
		t.Fatalf("approval request id = %q, want %q", replayed[0].ApprovalRequestID, pending.id)
	}
	if permission.ToolCall.ToolCallID != "call-1" || stringPtrValue(permission.ToolCall.Kind) != schema.ToolKindExecute {
		t.Fatalf("permission tool call = %#v, want execute call-1", permission.ToolCall)
	}
	if got := EventMetaString(permission.ToolCall.Meta, EventMetaRoot, EventMetaRuntime, EventMetaRuntimeTool, EventMetaRuntimeToolName); got != "RUN_COMMAND" {
		t.Fatalf("permission tool meta = %#v, want RUN_COMMAND tool name", permission.ToolCall.Meta)
	}
	if len(permission.Options) != 1 || permission.Options[0].OptionID != "allow_once" {
		t.Fatalf("permission options = %#v, want allow_once", permission.Options)
	}
}

func TestTurnHandlePublishApprovalRequiresDurablePersister(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(turnHandleConfig{
		handleID: "h1", runID: "run-1", turnID: "turn-1",
		sessionRef: session.SessionRef{SessionID: "s1"},
	})
	_, err := handle.publishApproval(&agent.ApprovalRequest{
		Tool: tool.Definition{Name: "RUN_COMMAND"},
		Call: tool.Call{ID: "call-1", Name: "RUN_COMMAND"},
	})
	if err == nil || !strings.Contains(err.Error(), "durable approval persistence is unavailable") {
		t.Fatalf("publishApproval() error = %v, want durable persistence failure", err)
	}
	events, _, replayErr := handle.eventsAfter("")
	if replayErr != nil {
		t.Fatal(replayErr)
	}
	if len(events) != 0 {
		t.Fatalf("approval events = %#v, want no transient permission fallback", events)
	}
}

func TestTurnHandleChildApprovalReviewIsTransient(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
	request := testChildApprovalRequest("task-review", "review.txt")
	payload := canonicalApprovalPayload(request)
	payload.ReviewStatus = ApprovalReviewStatusApproved
	payload.ReviewText = "approved by reviewer"
	events := handle.approvalReviewEnvelopes(
		request,
		payload,
		&UsageSnapshot{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
		nil,
	)
	if len(events) != 2 {
		t.Fatalf("approval review events = %#v, want review plus usage", events)
	}
	for _, event := range events {
		if event.Scope != eventstream.ScopeSubagent || event.ScopeID != "task-review" {
			t.Fatalf("approval review scope = %q/%q, want subagent/task-review", event.Scope, event.ScopeID)
		}
		if event.ParentTool == nil || event.ParentTool.ToolCallID != "spawn-call-1" {
			t.Fatalf("approval review parent = %#v, want spawn-call-1", event.ParentTool)
		}
		if event.Delivery == nil || event.Delivery.Mode != eventstream.DeliveryTransient {
			t.Fatalf("approval review delivery = %#v, want transient", event.Delivery)
		}
		if event.Position != nil {
			t.Fatalf("approval review position = %#v, want no invented durable position", event.Position)
		}
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

	pending, err := handle.openPendingApproval(nil)
	if err != nil {
		t.Fatalf("openPendingApproval() error = %v", err)
	}
	if err := handle.Submit(context.Background(), SubmitRequest{
		Kind:     SubmissionKindApproval,
		Approval: &ApprovalDecision{RequestID: pending.id, Approved: true, Outcome: string(ApprovalStatusApproved)},
	}); err != nil {
		t.Fatalf("Submit(approval) error = %v", err)
	}
	resp := <-pending.decisions
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
	if _, err := handle.publishApproval(&agent.ApprovalRequest{
		Tool: tool.Definition{Name: "RUN_COMMAND"},
		Call: tool.Call{ID: "call-1"},
	}); err != nil {
		t.Fatalf("publishApproval() error = %v", err)
	}
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
		Approval: &ApprovalDecision{RequestID: "missing", Approved: true, Outcome: string(ApprovalStatusApproved)},
	})
	if err == nil {
		t.Fatal("Submit(approval) error = nil, want approval-not-pending")
	}
	var gwErr *Error
	if !As(err, &gwErr) || gwErr.Code != CodeApprovalNotPending {
		t.Fatalf("Submit(approval) error = %v, want approval_not_pending", err)
	}
}

func TestTurnHandleQueuesChildApprovalsInFIFOOrder(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
	childA, err := handle.publishApproval(testChildApprovalRequest("task-a", "a.txt"))
	if err != nil {
		t.Fatalf("publish child A approval: %v", err)
	}
	childB, err := handle.publishApproval(testChildApprovalRequest("task-b", "b.txt"))
	if err != nil {
		t.Fatalf("publish child B approval: %v", err)
	}
	if childA.id == childB.id {
		t.Fatalf("child approval ids = %q and %q, want distinct ids", childA.id, childB.id)
	}

	events, _, err := handle.eventsAfter("")
	if err != nil {
		t.Fatalf("eventsAfter() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("permission events = %#v, want only active child A", events)
	}
	assertChildPermissionEnvelope(t, events[0], childA.id, "task-a", "a.txt")

	assertApprovalNotActive(t, handle.Submit(context.Background(), SubmitRequest{
		Kind: SubmissionKindApproval,
		Approval: &ApprovalDecision{
			RequestID: childB.id,
			Outcome:   string(ApprovalStatusApproved),
			Approved:  true,
		},
	}))
	select {
	case got := <-childB.decisions:
		t.Fatalf("queued child B waiter received a decision: %+v", got)
	default:
	}

	if err := handle.Submit(context.Background(), SubmitRequest{
		Kind: SubmissionKindApproval,
		Approval: &ApprovalDecision{
			RequestID: childA.id,
			Outcome:   string(ApprovalStatusRejected),
			Approved:  false,
		},
	}); err != nil {
		t.Fatalf("Submit(child A approval) error = %v", err)
	}
	select {
	case got := <-childA.decisions:
		if got.RequestID != childA.id || got.Approved {
			t.Fatalf("child A decision = %+v, want its rejected decision", got)
		}
	case <-time.After(time.Second):
		t.Fatal("child A waiter did not receive its approval")
	}

	events, _, err = handle.eventsAfter("")
	if err != nil {
		t.Fatalf("eventsAfter() after child A = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("permission events after child A = %#v, want child B published next", events)
	}
	assertChildPermissionEnvelope(t, events[1], childB.id, "task-b", "b.txt")

	if err := handle.Submit(context.Background(), SubmitRequest{
		Kind: SubmissionKindApproval,
		Approval: &ApprovalDecision{
			RequestID: childB.id,
			Outcome:   string(ApprovalStatusApproved),
			Approved:  true,
		},
	}); err != nil {
		t.Fatalf("Submit(child B approval) error = %v", err)
	}
	select {
	case got := <-childB.decisions:
		if got.RequestID != childB.id || !got.Approved {
			t.Fatalf("child B decision = %+v, want its approved decision", got)
		}
	case <-time.After(time.Second):
		t.Fatal("child B waiter did not receive its approval")
	}
}

func TestTurnHandleQueuesMainAndChildApprovalsOnOnePlane(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
	main, err := handle.publishApproval(&agent.ApprovalRequest{
		Tool: tool.Definition{Name: "RUN_COMMAND"},
		Call: tool.Call{ID: "main-call", Name: "RUN_COMMAND"},
		Approval: &session.ProtocolApproval{Options: []session.ProtocolApprovalOption{{
			ID: "allow_once", Name: "Allow once", Kind: "allow_once",
		}}},
	})
	if err != nil {
		t.Fatalf("publish main approval: %v", err)
	}
	child, err := handle.publishApproval(testChildApprovalRequest("task-child", "child.txt"))
	if err != nil {
		t.Fatalf("publish child approval: %v", err)
	}
	events, _, err := handle.eventsAfter("")
	if err != nil {
		t.Fatalf("eventsAfter() error = %v", err)
	}
	if len(events) != 1 || events[0].ApprovalRequestID != main.id || events[0].Scope != eventstream.ScopeMain {
		t.Fatalf("initial approval events = %#v, want only active main request %q", events, main.id)
	}

	assertApprovalNotActive(t, handle.Submit(context.Background(), SubmitRequest{
		Kind:     SubmissionKindApproval,
		Approval: &ApprovalDecision{RequestID: child.id, Approved: true, Outcome: string(ApprovalStatusApproved)},
	}))

	if err := handle.Submit(context.Background(), SubmitRequest{
		Kind:     SubmissionKindApproval,
		Approval: &ApprovalDecision{RequestID: main.id, Approved: false, Outcome: string(ApprovalStatusRejected)},
	}); err != nil {
		t.Fatalf("Submit(main approval) error = %v", err)
	}
	select {
	case got := <-main.decisions:
		if got.RequestID != main.id || got.Approved {
			t.Fatalf("main decision = %+v, want main rejected decision", got)
		}
	case <-time.After(time.Second):
		t.Fatal("main waiter did not receive its decision")
	}

	events, _, err = handle.eventsAfter("")
	if err != nil {
		t.Fatalf("eventsAfter() after main = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("approval events after main = %#v, want child published next", events)
	}
	assertChildPermissionEnvelope(t, events[1], child.id, "task-child", "child.txt")
	if err := handle.Submit(context.Background(), SubmitRequest{
		Kind:     SubmissionKindApproval,
		Approval: &ApprovalDecision{RequestID: child.id, Approved: true, Outcome: string(ApprovalStatusApproved)},
	}); err != nil {
		t.Fatalf("Submit(child approval) error = %v", err)
	}
	select {
	case got := <-child.decisions:
		if got.RequestID != child.id || !got.Approved {
			t.Fatalf("child decision = %+v, want child approved decision", got)
		}
	case <-time.After(time.Second):
		t.Fatal("child waiter did not receive its decision")
	}
}

func TestTurnHandleRejectsMissingUnknownStaleAndDuplicateApprovalIDs(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
	missing := handle.Submit(context.Background(), SubmitRequest{
		Kind:     SubmissionKindApproval,
		Approval: &ApprovalDecision{Approved: true, Outcome: string(ApprovalStatusApproved)},
	})
	var missingErr *Error
	if !As(missing, &missingErr) || missingErr.Kind != KindValidation || missingErr.Code != CodeInvalidRequest {
		t.Fatalf("Submit(missing approval id) error = %v, want validation invalid_request", missing)
	}

	assertApprovalNotPending(t, handle.Submit(context.Background(), SubmitRequest{
		Kind:     SubmissionKindApproval,
		Approval: &ApprovalDecision{RequestID: "unknown", Approved: true, Outcome: string(ApprovalStatusApproved)},
	}))

	pending, err := handle.openPendingApproval(&agent.ApprovalRequest{PauseTokenID: "pause-stable"})
	if err != nil {
		t.Fatalf("open durable approval: %v", err)
	}
	if pending.id != "pause-stable" {
		t.Fatalf("durable pending id = %q, want pause-stable", pending.id)
	}
	if err := handle.Submit(context.Background(), SubmitRequest{
		Kind:     SubmissionKindApproval,
		Approval: &ApprovalDecision{RequestID: pending.id, Approved: true, Outcome: string(ApprovalStatusApproved)},
	}); err != nil {
		t.Fatalf("Submit(durable approval) error = %v", err)
	}
	<-pending.decisions
	assertApprovalNotPending(t, handle.Submit(context.Background(), SubmitRequest{
		Kind:     SubmissionKindApproval,
		Approval: &ApprovalDecision{RequestID: pending.id, Approved: true, Outcome: string(ApprovalStatusApproved)},
	}))
	if _, err := handle.openPendingApproval(&agent.ApprovalRequest{PauseTokenID: "pause-stable"}); err == nil {
		t.Fatal("open reused durable approval id error = nil, want conflict")
	} else {
		assertApprovalNotPending(t, err)
	}

	stale, err := handle.openPendingApproval(nil)
	if err != nil {
		t.Fatalf("open live approval: %v", err)
	}
	handle.releasePendingApproval(stale, "abandoned")
	assertApprovalNotPending(t, handle.Submit(context.Background(), SubmitRequest{
		Kind:     SubmissionKindApproval,
		Approval: &ApprovalDecision{RequestID: stale.id, Approved: true, Outcome: string(ApprovalStatusApproved)},
	}))
}

func TestTurnHandleClearsPendingApprovalsOnCancelCloseAndTerminal(t *testing.T) {
	t.Parallel()

	cleanup := map[string]func(*turnHandle){
		"cancel": func(handle *turnHandle) { handle.Cancel() },
		"close":  func(handle *turnHandle) { _ = handle.Close() },
		"terminal": func(handle *turnHandle) {
			handle.finish()
		},
	}
	for name, stop := range cleanup {
		t.Run(name, func(t *testing.T) {
			handle := newTestTurnHandle()
			pending, err := handle.openPendingApproval(nil)
			if err != nil {
				t.Fatalf("open approval: %v", err)
			}
			stop(handle)
			select {
			case <-pending.done:
			case <-time.After(time.Second):
				t.Fatal("pending approval was not released")
			}
			select {
			case got := <-pending.decisions:
				t.Fatalf("cleanup injected fallback approval decision: %+v", got)
			default:
			}
			remaining := len(handle.approvals.queueSnapshot())
			if remaining != 0 {
				t.Fatalf("pending approvals after %s = %d, want zero", name, remaining)
			}
			queued := len(handle.approvals.queueSnapshot())
			active, _ := handle.approvals.snapshot()
			if queued != 0 || active != nil {
				t.Fatalf("approval queue after %s = %d/%#v, want empty", name, queued, active)
			}
			assertApprovalNotPending(t, handle.Submit(context.Background(), SubmitRequest{
				Kind:     SubmissionKindApproval,
				Approval: &ApprovalDecision{RequestID: pending.id, Approved: true, Outcome: string(ApprovalStatusApproved)},
			}))
		})
	}
}

func TestTurnHandleAdvancesQueueWhenActiveApprovalIsAbandoned(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
	first, err := handle.publishApproval(testChildApprovalRequest("task-a", "a.txt"))
	if err != nil {
		t.Fatalf("publish first child approval: %v", err)
	}
	second, err := handle.publishApproval(testChildApprovalRequest("task-b", "b.txt"))
	if err != nil {
		t.Fatalf("publish second child approval: %v", err)
	}
	handle.releasePendingApproval(first, "delivery_failed")
	select {
	case <-first.done:
	case <-time.After(time.Second):
		t.Fatal("abandoned active approval was not released")
	}

	events, _, err := handle.eventsAfter("")
	if err != nil {
		t.Fatalf("eventsAfter() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("approval events = %#v, want second request published after failure", events)
	}
	assertChildPermissionEnvelope(t, events[1], second.id, "task-b", "b.txt")
	if err := handle.Submit(context.Background(), SubmitRequest{
		Kind:     SubmissionKindApproval,
		Approval: &ApprovalDecision{RequestID: second.id, Approved: true, Outcome: string(ApprovalStatusApproved)},
	}); err != nil {
		t.Fatalf("Submit(second approval) error = %v", err)
	}
	select {
	case got := <-second.decisions:
		if got.RequestID != second.id || !got.Approved {
			t.Fatalf("second decision = %+v, want approved exact response", got)
		}
	case <-time.After(time.Second):
		t.Fatal("second waiter did not receive its decision")
	}
}

func TestSessionApprovalCoordinatorAcceptsDetachedChildAfterParentTerminal(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
	handle.finish()
	request := testChildApprovalRequest("task-detached", "detached.txt")
	request.PauseTokenID = "detached-approval"
	pending, err := handle.openPendingApproval(request)
	if err != nil {
		t.Fatalf("open detached approval after parent terminal: %v", err)
	}
	gateway := &Gateway{
		approvals: map[string]*approvalCoordinator{handle.sessionRef.SessionID: handle.approvals},
	}
	if err := gateway.SubmitActiveTurn(context.Background(), SubmitActiveTurnRequest{
		SessionRef: handle.sessionRef,
		Kind:       SubmissionKindApproval,
		Approval: &ApprovalDecision{
			RequestID: pending.id, Approved: true, Outcome: string(ApprovalStatusApproved),
		},
	}); err != nil {
		t.Fatalf("SubmitActiveTurn(detached approval) error = %v", err)
	}
	select {
	case decision := <-pending.decisions:
		if decision.RequestID != pending.id || !decision.Approved {
			t.Fatalf("detached decision = %#v", decision)
		}
	case <-time.After(time.Second):
		t.Fatal("detached child approval waiter did not resume")
	}
}

func TestSessionApprovalCoordinatorSharesFIFOAcrossTurnOwners(t *testing.T) {
	t.Parallel()

	ref := session.SessionRef{SessionID: "session-shared-approval"}
	coordinator := newApprovalCoordinator(ref)
	firstOwner := newTurnHandle(turnHandleConfig{handleID: "turn-a", sessionRef: ref, approvals: coordinator})
	secondOwner := newTurnHandle(turnHandleConfig{handleID: "turn-b", sessionRef: ref, approvals: coordinator})
	first, err := firstOwner.openPendingApproval(testChildApprovalRequest("task-a", "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := secondOwner.openPendingApproval(testChildApprovalRequest("task-b", "b.txt"))
	if err != nil {
		t.Fatal(err)
	}
	active, queued := coordinator.snapshot()
	if active != first || queued != 1 {
		t.Fatalf("shared FIFO = active %#v queued %d, want first/1", active, queued)
	}
	if err := secondOwner.submitApproval(context.Background(), ApprovalDecision{
		RequestID: first.id, Approved: true, Outcome: string(ApprovalStatusApproved),
	}); err != nil {
		t.Fatalf("cross-owner resolve first = %v", err)
	}
	active, queued = coordinator.snapshot()
	if active != second || queued != 0 {
		t.Fatalf("shared FIFO after resolve = active %#v queued %d, want second/0", active, queued)
	}
	coordinator.release(second, "abandoned")
}

func TestTurnHandleConcurrentApprovalSubmissionsOnlyResolveActiveHead(t *testing.T) {
	t.Parallel()

	handle := newTestTurnHandle()
	pendings := make([]*pendingApproval, 0, 3)
	for range 3 {
		pending, err := handle.openPendingApproval(nil)
		if err != nil {
			t.Fatalf("open approval: %v", err)
		}
		pendings = append(pendings, pending)
	}

	var submits sync.WaitGroup
	errs := make([]error, len(pendings))
	for index, pending := range pendings[1:] {
		index++
		submits.Add(1)
		go func(index int, pending *pendingApproval) {
			defer submits.Done()
			errs[index] = handle.Submit(context.Background(), SubmitRequest{
				Kind: SubmissionKindApproval,
				Approval: &ApprovalDecision{
					RequestID: pending.id,
					Outcome:   fmt.Sprintf("outcome-%d", index),
					Approved:  index%2 == 0,
				},
			})
		}(index, pending)
	}
	submits.Wait()
	assertApprovalNotActive(t, errs[1])
	assertApprovalNotActive(t, errs[2])
	if err := handle.Submit(context.Background(), SubmitRequest{
		Kind: SubmissionKindApproval,
		Approval: &ApprovalDecision{
			RequestID: pendings[0].id,
			Outcome:   "outcome-0",
			Approved:  true,
		},
	}); err != nil {
		t.Fatalf("Submit(active %q) error = %v", pendings[0].id, err)
	}
	select {
	case got := <-pendings[0].decisions:
		if got.RequestID != pendings[0].id || got.Outcome != "outcome-0" || !got.Approved {
			t.Fatalf("active decision = %+v, want request %q exact response", got, pendings[0].id)
		}
	case <-time.After(time.Second):
		t.Fatal("active approval waiter did not receive a decision")
	}
	for index, pending := range pendings[1:] {
		wantIndex := index + 1
		if err := handle.Submit(context.Background(), SubmitRequest{
			Kind: SubmissionKindApproval,
			Approval: &ApprovalDecision{
				RequestID: pending.id,
				Outcome:   fmt.Sprintf("outcome-%d", wantIndex),
				Approved:  wantIndex%2 == 0,
			},
		}); err != nil {
			t.Fatalf("Submit(active %q) error = %v", pending.id, err)
		}
		select {
		case got := <-pending.decisions:
			if got.RequestID != pending.id || got.Outcome != fmt.Sprintf("outcome-%d", wantIndex) || got.Approved != (wantIndex%2 == 0) {
				t.Fatalf("decision %d = %+v, want request %q exact response", wantIndex, got, pending.id)
			}
		case <-time.After(time.Second):
			t.Fatalf("approval waiter %q did not receive a decision", pending.id)
		}
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
	ref := session.SessionRef{
		AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws",
	}
	return newTurnHandle(turnHandleConfig{
		handleID:   "h1",
		runID:      "run-1",
		turnID:     "turn-1",
		sessionRef: ref,
		persistApproval: func(req *agent.ApprovalRequest, requestID eventstream.ApprovalRequestID) (*session.Event, error) {
			permission := approval.ProtocolApprovalFromPayload(canonicalApprovalPayload(req))
			event := &session.Event{
				ID:                "test-" + string(requestID),
				SessionID:         ref.SessionID,
				Seq:               1,
				Type:              session.EventTypeCustom,
				Visibility:        session.VisibilityMirror,
				ApprovalRequestID: string(requestID),
				Scope:             &session.EventScope{TurnID: "turn-1", Source: "approval"},
				Protocol: &session.EventProtocol{
					Method:     session.ProtocolMethodRequestPermission,
					Permission: permission,
				},
			}
			if origin := canonicalOriginFromApproval(req, ref, "turn-1"); origin != nil {
				event.ChildOrigin = approvalChildOrigin(req, origin, requestID)
			}
			return event, nil
		},
		createdAt: time.Unix(100, 0),
	})
}

func testChildApprovalRequest(taskID string, path string) *agent.ApprovalRequest {
	return &agent.ApprovalRequest{
		Tool: tool.Definition{Name: "WRITE"},
		Call: tool.Call{ID: "shared-child-call", Name: "WRITE"},
		Approval: &session.ProtocolApproval{
			ToolCall: session.ProtocolToolCall{
				ID:       "shared-child-call",
				Name:     "WRITE",
				Kind:     "edit",
				Title:    "Write file",
				Status:   "pending",
				RawInput: map[string]any{"path": path},
				Content: []session.ProtocolToolCallContent{{
					Type:    "content",
					Content: session.ProtocolTextContent("child permission detail"),
				}},
			},
			Options: []session.ProtocolApprovalOption{{
				ID: "allow_once", Name: "Allow once", Kind: "allow_once",
			}},
		},
		Metadata: map[string]any{
			"subagent":       true,
			"scope":          "subagent",
			"scope_id":       taskID,
			"task_id":        taskID,
			"parent_call_id": "spawn-call-1",
			"parent_tool":    "SPAWN",
		},
	}
}

func assertChildPermissionEnvelope(t *testing.T, env eventstream.Envelope, requestID eventstream.ApprovalRequestID, taskID string, path string) {
	t.Helper()
	if env.Kind != eventstream.KindRequestPermission || env.ApprovalRequestID != requestID {
		t.Fatalf("child permission envelope = %#v, want request permission id %q", env, requestID)
	}
	if env.Scope != eventstream.ScopeSubagent || env.ScopeID != taskID {
		t.Fatalf("child permission scope = %q/%q, want subagent/%q", env.Scope, env.ScopeID, taskID)
	}
	if env.ParentTool == nil || env.ParentTool.ToolCallID != "spawn-call-1" || env.ParentTool.ToolName != "SPAWN" {
		t.Fatalf("child parent relation = %#v, want SPAWN/spawn-call-1", env.ParentTool)
	}
	if env.Delivery == nil || env.Delivery.Mode != eventstream.DeliveryMirror {
		t.Fatalf("child delivery = %#v, want durable mirror delivery", env.Delivery)
	}
	if env.Position == nil || env.Position.Durable == nil || env.Position.Validate() != nil {
		t.Fatalf("child position = %#v, want valid durable position", env.Position)
	}
	if env.Permission == nil || env.Permission.ToolCall.ToolCallID != "shared-child-call" || len(env.Permission.Options) != 1 || env.Permission.Options[0].OptionID != "allow_once" {
		t.Fatalf("child ACP permission = %#v, want preserved tool call and options", env.Permission)
	}
	raw, ok := env.Permission.ToolCall.RawInput.(map[string]any)
	if !ok || raw["path"] != path {
		t.Fatalf("child ACP raw input = %#v, want path %q", env.Permission.ToolCall.RawInput, path)
	}
	if len(env.Permission.ToolCall.Content) != 1 || env.Permission.ToolCall.Content[0].Type != "content" {
		t.Fatalf("child ACP content = %#v, want preserved content", env.Permission.ToolCall.Content)
	}
}

func assertApprovalNotPending(t *testing.T, err error) {
	t.Helper()
	var gwErr *Error
	if err == nil || !As(err, &gwErr) || gwErr.Kind != KindConflict || gwErr.Code != CodeApprovalNotPending {
		t.Fatalf("approval error = %v, want conflict approval_not_pending", err)
	}
}

func assertApprovalNotActive(t *testing.T, err error) {
	t.Helper()
	var gwErr *Error
	if err == nil || !As(err, &gwErr) || gwErr.Kind != KindConflict || gwErr.Code != CodeApprovalNotActive {
		t.Fatalf("approval error = %v, want conflict approval_not_active", err)
	}
}

func drainACPEvents(events <-chan eventstream.Envelope) []eventstream.Envelope {
	var out []eventstream.Envelope
	for env := range events {
		out = append(out, env)
	}
	return out
}
