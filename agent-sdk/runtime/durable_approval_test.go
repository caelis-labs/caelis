package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
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

type cancelAfterCommitApprovalService struct {
	session.Service
	armed  atomic.Bool
	cancel context.CancelFunc
}

func (s *cancelAfterCommitApprovalService) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	persisted, err := s.Service.AppendEvent(ctx, req)
	if err != nil || !s.armed.CompareAndSwap(true, false) {
		return persisted, err
	}
	if token := pauseTokenFromEvent(req.Event); token == nil || token.Status != session.PauseTokenResolved {
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
	if token := pauseTokenFromEvent(req.Event); token == nil || token.Status != session.PauseTokenResolved {
		s.failResolved.Store(true)
		return persisted, nil
	}
	return persisted, &session.CommittedError{Err: errors.New("simulated file-store post-commit report failure")}
}

func pauseTokenFromEvent(event *session.Event) *session.PauseToken {
	if event == nil || event.Journal == nil {
		return nil
	}
	return event.Journal.PauseToken
}

func TestResolveApprovalDeliversFileStoreCommittedResolution(t *testing.T) {
	t.Parallel()

	base := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{
		RootDir:            t.TempDir(),
		SessionIDGenerator: func() string { return "sess-approval-committed" },
	}))
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
	if err := runtime.startRunTurnJournal(context.Background(), activeSession.SessionRef, runID, turnID); err != nil {
		t.Fatalf("startRunTurnJournal() error = %v", err)
	}

	decision := agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true, Reason: "approved by user"}
	result := make(chan struct {
		decision agent.ApprovalResponse
		err      error
	}, 1)
	go func() {
		got, requestErr := runtime.requestDurableApproval(context.Background(), agent.ApprovalRequest{
			SessionRef: activeSession.SessionRef,
			Session:    activeSession,
			RunID:      runID,
			TurnID:     turnID,
			Tool:       tool.Definition{Name: "WRITE"},
			Call:       tool.Call{ID: "call-approval-committed", Name: "WRITE"},
		}, nil)
		result <- struct {
			decision agent.ApprovalResponse
			err      error
		}{decision: got, err: requestErr}
	}()

	var waiting agent.RunState
	deadline := time.After(2 * time.Second)
	for waiting.PauseTokenID == "" {
		waiting, err = runtime.RunState(context.Background(), activeSession.SessionRef)
		if err != nil {
			t.Fatalf("RunState() error = %v", err)
		}
		select {
		case <-deadline:
			t.Fatalf("RunState() = %+v, want live approval waiter", waiting)
		default:
			time.Sleep(time.Millisecond)
		}
	}

	sessions.failResolved.Store(true)
	if err := runtime.ResolveApproval(context.Background(), agent.ResolveApprovalRequest{
		SessionRef: activeSession.SessionRef,
		TokenID:    waiting.PauseTokenID,
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

func TestRequestDurableApprovalExposesPauseTokenIDToRequester(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "approval-request-id")
	runtime, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	const runID = "run-approval-request-id"
	const turnID = "turn-approval-request-id"
	if err := runtime.startRunTurnJournal(context.Background(), activeSession.SessionRef, runID, turnID); err != nil {
		t.Fatalf("startRunTurnJournal() error = %v", err)
	}

	var observed agent.ApprovalRequest
	decision := agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true}
	got, err := runtime.requestDurableApproval(context.Background(), agent.ApprovalRequest{
		SessionRef: activeSession.SessionRef,
		Session:    activeSession,
		RunID:      runID,
		TurnID:     turnID,
		Tool:       tool.Definition{Name: "WRITE"},
		Call:       tool.Call{ID: "call-approval-request-id", Name: "WRITE"},
	}, approvalRequesterFunc(func(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		observed = req
		return decision, nil
	}))
	if err != nil {
		t.Fatalf("requestDurableApproval() error = %v", err)
	}
	if got != decision {
		t.Fatalf("requestDurableApproval() = %+v, want %+v", got, decision)
	}
	if observed.PauseTokenID == "" {
		t.Fatal("requester approval request has empty PauseTokenID")
	}

	events, err := sessions.Events(context.Background(), session.EventsRequest{
		SessionRef:       activeSession.SessionRef,
		IncludeTransient: true,
	})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	for _, event := range events {
		token := pauseTokenFromEvent(event)
		if token != nil && token.TokenID == observed.PauseTokenID {
			return
		}
	}
	t.Fatalf("requester pause token id %q was not persisted", observed.PauseTokenID)
}

func TestResolveApprovalRedeliversMatchingDurableDecision(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "approval-redelivery")
	runtime, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	now := time.Unix(500, 0).UTC()
	pending := session.PauseToken{
		Schema: session.ExecutionJournalSchemaVersion, TokenID: "pause-redelivery", SessionID: activeSession.SessionID,
		RunID: "run-redelivery", TurnID: "turn-redelivery", ToolCallID: "call-redelivery", ToolName: "WRITE",
		Revision: 1, Status: session.PauseTokenPending, CreatedAt: now, UpdatedAt: now,
	}
	if err := runtime.appendPauseToken(context.Background(), activeSession.SessionRef, pending); err != nil {
		t.Fatalf("appendPauseToken(pending) error = %v", err)
	}
	decision := agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true}
	resolved := pending
	resolved.Revision++
	resolved.Status = session.PauseTokenResolved
	resolved.Outcome = decision.Outcome
	resolved.OptionID = decision.OptionID
	resolved.Approved = decision.Approved
	resolved.UpdatedAt = now.Add(time.Second)
	if err := runtime.appendPauseToken(context.Background(), activeSession.SessionRef, resolved); err != nil {
		t.Fatalf("appendPauseToken(resolved) error = %v", err)
	}

	waiter := make(chan agent.ApprovalResponse, 1)
	runtime.mu.Lock()
	runtime.approvalWaiters[resolved.TokenID] = waiter
	runtime.mu.Unlock()
	defer func() {
		runtime.mu.Lock()
		delete(runtime.approvalWaiters, resolved.TokenID)
		runtime.mu.Unlock()
	}()

	if err := runtime.ResolveApproval(context.Background(), agent.ResolveApprovalRequest{
		SessionRef: activeSession.SessionRef,
		TokenID:    resolved.TokenID,
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
				base = inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
			case "file":
				base = sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()}))
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
			if err := runtime.startRunTurnJournal(context.Background(), active.SessionRef, runID, turnID); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				_, requestErr := runtime.requestDurableApproval(context.Background(), agent.ApprovalRequest{
					SessionRef: active.SessionRef, Session: active, RunID: runID, TurnID: turnID,
					Tool: tool.Definition{Name: "WRITE"}, Call: tool.Call{ID: "call-" + kind, Name: "WRITE"},
				}, nil)
				result <- requestErr
			}()

			var tokenID string
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) && tokenID == "" {
				state, stateErr := runtime.RunState(context.Background(), active.SessionRef)
				if stateErr != nil {
					t.Fatal(stateErr)
				}
				tokenID = state.PauseTokenID
				if tokenID == "" {
					time.Sleep(time.Millisecond)
				}
			}
			if tokenID == "" {
				t.Fatal("approval waiter did not persist a pause token")
			}

			decision := agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_once", Approved: true}
			sessions.armed.Store(true)
			if err := runtime.ResolveApproval(resolverCtx, agent.ResolveApprovalRequest{SessionRef: active.SessionRef, TokenID: tokenID, Decision: decision}); err != nil {
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
			if err := runtime.ResolveApproval(context.Background(), agent.ResolveApprovalRequest{SessionRef: active.SessionRef, TokenID: tokenID, Decision: decision}); err != nil {
				t.Fatalf("matching idempotent retry error = %v", err)
			}
			conflict := decision
			conflict.OptionID = "reject_once"
			conflict.Approved = false
			if err := runtime.ResolveApproval(context.Background(), agent.ResolveApprovalRequest{SessionRef: active.SessionRef, TokenID: tokenID, Decision: conflict}); err == nil {
				t.Fatal("conflicting retry succeeded, want fail closed")
			}
		})
	}
}
