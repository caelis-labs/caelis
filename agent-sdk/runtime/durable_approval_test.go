package runtime

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type postCommitApprovalService struct {
	session.Service
	failResolved atomic.Bool
}

type rejectResolvedApprovalService struct {
	session.Service
	rejectResolved atomic.Bool
}

type cancelAfterCommitApprovalService struct {
	session.Service
	armed  atomic.Bool
	cancel context.CancelFunc
}

func (s *rejectResolvedApprovalService) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	if s.rejectResolved.Load() {
		if token := approvalRecordFromEvent(req.Event); token != nil && token.Status == session.ApprovalRecordResolved {
			return nil, errors.New("simulated durable resolve failure")
		}
	}
	return s.Service.AppendEvent(ctx, req)
}

func (s *cancelAfterCommitApprovalService) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	persisted, err := s.Service.AppendEvent(ctx, req)
	if err != nil || !s.armed.CompareAndSwap(true, false) {
		return persisted, err
	}
	if token := approvalRecordFromEvent(req.Event); token == nil || token.Status != session.ApprovalRecordResolved {
		s.armed.Store(true)
		return persisted, nil
	}
	s.cancel()
	return persisted, &session.CommittedError{Err: context.Canceled}
}

func (s *cancelAfterCommitApprovalService) Events(ctx context.Context, req session.EventsRequest) ([]*session.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.Service.Events(ctx, req)
}

func (s *postCommitApprovalService) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	persisted, err := s.Service.AppendEvent(ctx, req)
	if err != nil || !s.failResolved.CompareAndSwap(true, false) {
		return persisted, err
	}
	if token := approvalRecordFromEvent(req.Event); token == nil || token.Status != session.ApprovalRecordResolved {
		s.failResolved.Store(true)
		return persisted, nil
	}
	return persisted, &session.CommittedError{Err: errors.New("simulated file-store post-commit report failure")}
}

func approvalRecordFromEvent(event *session.Event) *session.ApprovalRecord {
	if event == nil || event.Approval == nil {
		return nil
	}
	return event.Approval
}

func TestResolveApprovalDeliversFileStoreCommittedResolution(t *testing.T) {
	t.Parallel()

	base := sessionfile.NewStore(sessionfile.Config{
		RootDir:            t.TempDir(),
		SessionIDGenerator: func() string { return "sess-approval-committed" },
	})
	sessions := &postCommitApprovalService{Service: base}
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	runtime, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	const runID = "run-approval-committed"
	const turnID = "turn-approval-committed"

	decision := approval.Response{Outcome: "selected", OptionID: "allow_once", Approved: true, Reason: "approved by user"}
	result := make(chan struct {
		decision approval.Response
		err      error
	}, 1)
	go func() {
		got, requestErr := runtime.requestDurableApproval(context.Background(), approval.Request{
			SessionRef: activeSession.SessionRef,
			Session:    activeSession,
			RunID:      runID,
			TurnID:     turnID,
			Tool:       tool.Definition{Name: "Write"},
			Call:       tool.Call{ID: "call-approval-committed", Name: "Write"},
		}, nil)
		result <- struct {
			decision approval.Response
			err      error
		}{decision: got, err: requestErr}
	}()

	requestID := waitForPendingApprovalRecord(t, sessions, activeSession.SessionRef, runID)

	sessions.failResolved.Store(true)
	if err := runtime.ResolveApproval(context.Background(), approval.ResolveRequest{
		SessionRef: activeSession.SessionRef,
		RequestID:  requestID,
		Decision:   decision,
	}); err != nil {
		t.Fatalf("ResolveApproval() error = %v, want durable committed resolution delivered", err)
	}

	select {
	case got := <-result:
		if got.err != nil || got.decision != decision {
			t.Fatalf("requestDurableApproval() = %+v, %v; want %+v", got.decision, got.err, decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("requestDurableApproval() remained asleep after durable resolution")
	}
}

func TestRequestDurableApprovalExposesApprovalRequestIDToRequester(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "approval-request-id")
	runtime, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	const runID = "run-approval-request-id"
	const turnID = "turn-approval-request-id"

	var observed approval.Request
	decision := approval.Response{Outcome: "selected", OptionID: "allow_once", Approved: true, ReviewText: "review approved"}
	got, err := runtime.requestDurableApproval(context.Background(), approval.Request{
		SessionRef: activeSession.SessionRef,
		Session:    activeSession,
		RunID:      runID,
		TurnID:     turnID,
		Tool:       tool.Definition{Name: "Write"},
		Call:       tool.Call{ID: "call-approval-request-id", Name: "Write"}, Execution: tool.InvocationContext{ItemID: "item-approval-request-id"},
	}, approvalRequesterFunc(func(_ context.Context, req approval.Request) (approval.Response, error) {
		observed = req
		return decision, nil
	}))
	if err != nil {
		t.Fatalf("requestDurableApproval() error = %v", err)
	}
	if got != decision {
		t.Fatalf("requestDurableApproval() = %+v, want %+v", got, decision)
	}
	if observed.ApprovalRequestID == "" {
		t.Fatal("requester approval request has empty ApprovalRequestID")
	}

	events, err := sessions.Events(context.Background(), session.EventsRequest{
		SessionRef:       activeSession.SessionRef,
		IncludeTransient: true,
	})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	var pending, resolved *session.ApprovalRecord
	for _, event := range events {
		token := approvalRecordFromEvent(event)
		if token == nil || token.RequestID != observed.ApprovalRequestID {
			continue
		}
		switch token.Status {
		case session.ApprovalRecordPending:
			pending = token
		case session.ApprovalRecordResolved:
			resolved = token
		}
	}
	if pending == nil || resolved == nil || pending.ItemID != "item-approval-request-id" || resolved.ItemID != pending.ItemID || resolved.TurnID != turnID || resolved.ToolCallID != "call-approval-request-id" {
		t.Fatalf("persisted pause token correlation = pending %v, resolved %v", pending, resolved)
	}
	if got := session.PageEvents(events, session.EventPageRequest{Visibility: session.EventPageCanonical}); len(got.Events) != 0 {
		t.Fatalf("approval journal entered model context: %#v", got.Events)
	}
	if got := session.PageEvents(events, session.EventPageRequest{Visibility: session.EventPageClientReplay}); len(got.Events) != 1 || session.ResolvedApprovalReview(got.Events[0]) == nil || got.Events[0].Approval.ItemID != pending.ItemID {
		t.Fatalf("resolved decision replay = %#v", got.Events)
	}
}

func TestRequestDurableApprovalRequesterFailureLeavesStartedJournal(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "approval-request-fail")
	runtime, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	const runID = "run-approval-request-fail"
	const turnID = "turn-approval-request-fail"

	wantErr := errors.New("guardian execution failed")
	_, err = runtime.requestDurableApproval(context.Background(), approval.Request{
		SessionRef: activeSession.SessionRef,
		Session:    activeSession,
		RunID:      runID,
		TurnID:     turnID,
		Tool:       tool.Definition{Name: "Write"},
		Call:       tool.Call{ID: "call-approval-request-fail", Name: "Write"},
	}, approvalRequesterFunc(func(context.Context, approval.Request) (approval.Response, error) {
		return approval.Response{}, wantErr
	}))
	if !errors.Is(err, wantErr) {
		t.Fatalf("requestDurableApproval() error = %v, want %v", err, wantErr)
	}

	state, err := runtime.RunState(context.Background(), activeSession.SessionRef)
	if err != nil {
		t.Fatalf("RunState() error = %v", err)
	}
	if state.WaitingApproval {
		t.Fatalf("RunState() = %+v, want running without waiting_approval", state)
	}

	events, err := sessions.Events(context.Background(), session.EventsRequest{
		SessionRef:       activeSession.SessionRef,
		IncludeTransient: true,
	})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}

	var latestPause session.ApprovalRecord
	for _, event := range events {
		token := approvalRecordFromEvent(event)
		if token == nil || token.RunID != runID {
			continue
		}
		if token.Revision >= latestPause.Revision {
			latestPause = session.CloneApprovalRecord(*token)
		}
	}
	if latestPause.Status != session.ApprovalRecordCancelled {
		t.Fatalf("pause token status = %q, want cancelled", latestPause.Status)
	}

	// Turn completion must remain legal after a Guardian/auto-review failure.

}

func TestRequestDurableApprovalResolveFailureLeavesStartedJournal(t *testing.T) {
	t.Parallel()

	base, activeSession := newTestSessionService(t, "approval-resolve-fail")
	sessions := &rejectResolvedApprovalService{Service: base}
	sessions.rejectResolved.Store(true)
	runtime, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	const runID = "run-approval-resolve-fail"
	const turnID = "turn-approval-resolve-fail"

	decision := approval.Response{Outcome: "selected", OptionID: "allow_once", Approved: true}
	_, err = runtime.requestDurableApproval(context.Background(), approval.Request{
		SessionRef: activeSession.SessionRef,
		Session:    activeSession,
		RunID:      runID,
		TurnID:     turnID,
		Tool:       tool.Definition{Name: "Write"},
		Call:       tool.Call{ID: "call-approval-resolve-fail", Name: "Write"},
	}, approvalRequesterFunc(func(context.Context, approval.Request) (approval.Response, error) {
		return decision, nil
	}))
	if err == nil || !strings.Contains(err.Error(), "simulated durable resolve failure") {
		t.Fatalf("requestDurableApproval() error = %v, want resolve failure", err)
	}

	state, err := runtime.RunState(context.Background(), activeSession.SessionRef)
	if err != nil {
		t.Fatalf("RunState() error = %v", err)
	}
	if state.WaitingApproval {
		t.Fatalf("RunState() = %+v, want running without waiting_approval", state)
	}

	events, err := sessions.Events(context.Background(), session.EventsRequest{
		SessionRef:       activeSession.SessionRef,
		IncludeTransient: true,
	})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}

	var latestPause session.ApprovalRecord
	for _, event := range events {
		token := approvalRecordFromEvent(event)
		if token == nil || token.RunID != runID {
			continue
		}
		if token.Revision >= latestPause.Revision {
			latestPause = session.CloneApprovalRecord(*token)
		}
	}
	if latestPause.Status != session.ApprovalRecordCancelled {
		t.Fatalf("pause token status = %q, want cancelled after unresolved resolve failure", latestPause.Status)
	}

}

func TestResolveApprovalRedeliversMatchingDurableDecision(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "approval-redelivery")
	runtime, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	now := time.Unix(500, 0).UTC()
	pending := session.ApprovalRecord{
		Schema: session.ApprovalSchemaVersion, RequestID: "pause-redelivery", SessionID: activeSession.SessionID,
		RunID: "run-redelivery", TurnID: "turn-redelivery", ToolCallID: "call-redelivery", ToolName: "Write",
		Revision: 1, Status: session.ApprovalRecordPending, CreatedAt: now, UpdatedAt: now,
	}
	if err := runtime.appendApprovalRecord(context.Background(), activeSession.SessionRef, pending); err != nil {
		t.Fatalf("appendApprovalRecord(pending) error = %v", err)
	}
	decision := approval.Response{Outcome: "selected", OptionID: "allow_once", Approved: true}
	resolved := pending
	resolved.Revision++
	resolved.Status = session.ApprovalRecordResolved
	resolved.Outcome = decision.Outcome
	resolved.OptionID = decision.OptionID
	resolved.Approved = decision.Approved
	resolved.UpdatedAt = now.Add(time.Second)
	if err := runtime.appendApprovalRecord(context.Background(), activeSession.SessionRef, resolved); err != nil {
		t.Fatalf("appendApprovalRecord(resolved) error = %v", err)
	}

	waiter := make(chan approval.Response, 1)
	runtime.mu.Lock()
	runtime.approvalWaiters[resolved.RequestID] = approvalWaiter{responses: waiter, owner: t.Context()}
	runtime.mu.Unlock()
	defer func() {
		runtime.mu.Lock()
		delete(runtime.approvalWaiters, resolved.RequestID)
		runtime.mu.Unlock()
	}()

	if err := runtime.ResolveApproval(context.Background(), approval.ResolveRequest{
		SessionRef: activeSession.SessionRef,
		RequestID:  resolved.RequestID,
		Decision:   decision,
	}); err != nil {
		t.Fatalf("ResolveApproval(idempotent) error = %v", err)
	}
	select {
	case got := <-waiter:
		if got != decision {
			t.Fatalf("delivered decision = %+v, want %+v", got, decision)
		}
	case <-time.After(time.Second):
		t.Fatal("idempotent ResolveApproval() did not redeliver the durable decision")
	}
}

func TestResolveApprovalRecoversCommittedDecisionAfterResolverCancellation(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"memory", "file"} {
		kind := kind
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			var base session.Service
			switch kind {
			case "memory":
				base = inmemory.NewStore(inmemory.Config{})
			case "file":
				base = sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
			}
			resolverCtx, cancel := context.WithCancel(context.Background())
			sessions := &cancelAfterCommitApprovalService{Service: base, cancel: cancel}
			active, err := sessions.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "resolver-cancel", PreferredSessionID: "approval-" + kind})
			if err != nil {
				t.Fatal(err)
			}
			runtime, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
			if err != nil {
				t.Fatal(err)
			}
			runID, turnID := "run-"+kind, "turn-"+kind

			result := make(chan error, 1)
			go func() {
				_, requestErr := runtime.requestDurableApproval(context.Background(), approval.Request{
					SessionRef: active.SessionRef, Session: active, RunID: runID, TurnID: turnID,
					Tool: tool.Definition{Name: "Write"}, Call: tool.Call{ID: "call-" + kind, Name: "Write"}, Execution: tool.InvocationContext{ItemID: "item-" + kind},
				}, nil)
				result <- requestErr
			}()

			tokenID := waitForPendingApprovalRecord(t, sessions, active.SessionRef, runID)

			decision := approval.Response{Outcome: "selected", OptionID: "allow_once", Approved: true, ReviewText: "review approved"}
			sessions.armed.Store(true)
			if err := runtime.ResolveApproval(resolverCtx, approval.ResolveRequest{SessionRef: active.SessionRef, RequestID: tokenID, Decision: decision}); err != nil {
				t.Fatalf("ResolveApproval() error = %v", err)
			}
			if resolverCtx.Err() != context.Canceled {
				t.Fatalf("resolver context error = %v, want canceled after commit", resolverCtx.Err())
			}
			select {
			case err := <-result:
				if err != nil {
					t.Fatalf("live approval waiter error = %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("live approval waiter remained asleep after committed resolution")
			}
			if err := runtime.ResolveApproval(context.Background(), approval.ResolveRequest{SessionRef: active.SessionRef, RequestID: tokenID, Decision: decision}); err != nil {
				t.Fatalf("matching idempotent retry error = %v", err)
			}
			all, err := sessions.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			var pending, resolved *session.ApprovalRecord
			for _, event := range all {
				if token := approvalRecordFromEvent(event); token != nil && token.RequestID == tokenID {
					switch token.Status {
					case session.ApprovalRecordPending:
						pending = token
					case session.ApprovalRecordResolved:
						resolved = token
					}
				}
			}
			if pending == nil || resolved == nil || pending.ItemID != "item-"+kind || resolved.ItemID != pending.ItemID {
				t.Fatalf("%s persisted correlation: pending %v, resolved %v", kind, pending, resolved)
			}
			page, err := base.(session.PagedReader).EventsPage(context.Background(), session.EventPageRequest{SessionRef: active.SessionRef, Visibility: session.EventPageClientReplay})
			if err != nil || len(page.Events) != 1 || session.ResolvedApprovalReview(page.Events[0]) == nil || page.Events[0].Approval.ItemID != pending.ItemID {
				t.Fatalf("%s replay page = %#v, error %v", kind, page.Events, err)
			}
			canonical, err := base.(session.PagedReader).EventsPage(context.Background(), session.EventPageRequest{SessionRef: active.SessionRef, Visibility: session.EventPageCanonical})
			if err != nil || len(canonical.Events) != 0 {
				t.Fatalf("%s model context page = %#v, error %v", kind, canonical.Events, err)
			}
			conflict := decision
			conflict.OptionID = "reject_once"
			conflict.Approved = false
			if err := runtime.ResolveApproval(context.Background(), approval.ResolveRequest{SessionRef: active.SessionRef, RequestID: tokenID, Decision: conflict}); err == nil {
				t.Fatal("conflicting retry succeeded, want fail closed")
			}
		})
	}
}
