package runtime

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestPolicyIsOnlyMCPApprovalDecision(t *testing.T) {
	for _, action := range []policy.Action{policy.ActionAllow, policy.ActionDeny, policy.ActionAskApproval} {
		t.Run(string(action), func(t *testing.T) {
			var decisions, approvals, effects atomic.Int32
			def := fixtureMCPDefinition("source")
			base := tool.NamedTool{Def: def, Invoke: func(context.Context, tool.Call) (tool.Result, error) { effects.Add(1); return tool.Result{}, nil }}
			wrapped := policyWrappedTool{tool: base, mode: "custom", policy: policy.NamedMode{ID: "custom", Decide: func(context.Context, policy.ToolContext) (policy.Decision, error) {
				decisions.Add(1)
				return policy.Decision{Action: action, Approval: &session.ProtocolApproval{Options: []session.ProtocolApprovalOption{{ID: "approve", Kind: "allow_once"}, {ID: "reject", Kind: "reject_once"}}}}, nil
			}}, approval: approvalContext{requester: approvalRequesterFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
				approvals.Add(1)
				return agent.ApprovalResponse{Outcome: "selected", OptionID: "approve", Approved: true}, nil
			})}}
			result, err := wrapped.Call(t.Context(), tool.Call{ID: "original-call", Name: def.Name, Input: []byte(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			wantApproval, wantEffects := int32(0), int32(1)
			if action == policy.ActionAskApproval {
				wantApproval = 1
			}
			if action == policy.ActionDeny {
				wantEffects = 0
				if !result.IsError {
					t.Fatal("deny lost paired failure")
				}
			}
			if decisions.Load() != 1 || approvals.Load() != wantApproval || effects.Load() != wantEffects {
				t.Fatalf("decisions=%d approvals=%d effects=%d", decisions.Load(), approvals.Load(), effects.Load())
			}
		})
	}
}

func authorizationTestSession(t *testing.T) (*Runtime, session.Session, session.Service) {
	t.Helper()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "owner", Workspace: session.WorkspaceRef{Key: "test", CWD: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	return r, active, store
}

func authorizationTestRequirement(active session.Session) *session.ApprovalAuthorization {
	return &session.ApprovalAuthorization{Schema: session.AuthorizationSchemaVersion, Scope: session.AuthorizationScope{Authority: "owner", Workspace: active.CWD, SessionID: active.SessionID, SessionIncarnation: active.CreatedAt.UTC().Format(time.RFC3339Nano), SourceID: "source", SourceRevision: "v1", PolicyRevision: "p1", CapabilitySet: "atomic-app", CapabilityRevision: "14-v1", ResourceKind: "application", Resource: "com.example.A"}, CallDigest: "frozen-call", Choices: []session.AuthorizationChoice{{OptionID: "allow_app", Persistence: session.AuthorizationSession}}}
}

func authorizationTestRequest(active session.Session, a *session.ApprovalAuthorization) agent.ApprovalRequest {
	return agent.ApprovalRequest{SessionRef: active.SessionRef, Session: active, RunID: "run", TurnID: "turn", Authorization: a, Call: tool.Call{ID: "call", Name: "fixture", Input: []byte(`{"app":"com.example.A"}`)}, Tool: tool.Definition{Name: "fixture"}, Approval: &session.ProtocolApproval{Options: []session.ProtocolApprovalOption{{ID: "allow_app", Kind: "allow_once"}, {ID: "cancel", Kind: "reject_once"}}}}
}

func TestAuthorizationFlightSurvivesFirstCallerCancellation(t *testing.T) {
	r, active, store := authorizationTestSession(t)
	root, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var asks atomic.Int32
	target := policyWrappedTool{session: active, approval: approvalContext{ctx: root, runtime: r, requester: approvalRequesterFunc(func(ctx context.Context, _ agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		asks.Add(1)
		close(entered)
		select {
		case <-release:
			return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_app", Approved: true}, nil
		case <-ctx.Done():
			return agent.ApprovalResponse{}, ctx.Err()
		}
	})}}
	request := authorizationTestRequest(active, authorizationTestRequirement(active))
	leader, stopLeader := context.WithCancel(root)
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { _, err := target.resolvePolicyApproval(leader, request); first <- err }()
	<-entered
	go func() {
		response, err := target.resolvePolicyApproval(root, request)
		if err == nil && !response.Approved {
			err = errors.New("follower was not approved")
		}
		second <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		r.authorizationMu.Lock()
		joined := false
		for _, flight := range r.authorizationFlights {
			joined = flight.waiters == 2
		}
		r.authorizationMu.Unlock()
		if joined {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("follower did not join")
		}
		time.Sleep(time.Millisecond)
	}
	stopLeader()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("first cancellation: %v", err)
	}
	close(release)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if asks.Load() != 1 {
		t.Fatalf("approval requests=%d", asks.Load())
	}
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	projection := session.ProjectAuthorizations(active, events)
	if len(projection.Consents) != 1 || projection.Consents[0] != request.Authorization.Scope {
		t.Fatalf("settled consent: %+v", projection)
	}
}

func TestAuthorizationRevocationRejectsLateDecisionAndQueuedDispatch(t *testing.T) {
	r, active, _ := authorizationTestSession(t)
	a := authorizationTestRequirement(active)
	permit := &policyDispatchPermit{runtime: r, session: active, decision: policy.Decision{Action: policy.ActionAllow, Authorization: a}}
	if err := r.RevokeAuthorization(t.Context(), active.SessionRef, a.Scope, "original-revoke"); err != nil {
		t.Fatal(err)
	}
	if err := permit.consume(t.Context(), nil); err == nil {
		t.Fatal("revoked queued operation was admitted")
	}
	if err := r.checkAuthorization(t.Context(), active, a); err == nil {
		t.Fatal("late approval was current")
	}
	// The fact can be reloaded without recreating an execution owner.
	evidence, err := r.authorizationEvidence(t.Context(), active)
	if err != nil || evidence.Revocations[a.Scope.Key()] == 0 {
		t.Fatalf("revocation: %+v %v", evidence, err)
	}
	a.Revocation = evidence.Revocations[a.Scope.Key()]
	permit = &policyDispatchPermit{runtime: r, session: active, decision: policy.Decision{Action: policy.ActionAllow, Authorization: a}}
	if err := permit.consume(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := permit.consume(t.Context(), nil); err == nil {
		t.Fatal("permission was consumed twice")
	}
}

func TestSessionConsentRebuildsFromFileStoreWithoutExecuting(t *testing.T) {
	r, active, store := authorizationTestSession(t)
	a := authorizationTestRequirement(active)
	request := authorizationTestRequest(active, a)
	_, err := r.requestDurableApproval(t.Context(), request, approvalRequesterFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_app", Approved: true}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	fileStore := store.(*sessionfile.Store)
	// Reconstruct from persisted approval facts, without Runtime permission state.
	events, err := fileStore.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := fileStore.Session(t.Context(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if evidence := session.ProjectAuthorizations(loaded, events); !slices.Contains(evidence.Consents, a.Scope) {
		t.Fatalf("consent lost: %+v", evidence)
	}
	loaded.SessionID = "fork"
	if len(session.ProjectAuthorizations(loaded, events).Consents) != 0 {
		t.Fatal("fork inherited consent")
	}
	loaded = active
	loaded.CreatedAt = loaded.CreatedAt.Add(time.Second)
	if len(session.ProjectAuthorizations(loaded, events).Consents) != 0 {
		t.Fatal("new incarnation inherited consent")
	}
}

func TestAuthorizationLastWaiterAndRevocationRejectLateResolution(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprint(revoke), func(t *testing.T) {
			r, active, store := authorizationTestSession(t)
			request := authorizationTestRequest(active, authorizationTestRequirement(active))
			entered := make(chan agent.ApprovalRequest, 1)
			workerDone := make(chan struct{})
			releaseRequester := make(chan struct{})
			var releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(releaseRequester) })
			root, stopRoot := context.WithCancel(t.Context())
			defer stopRoot()
			target := policyWrappedTool{session: active, approval: approvalContext{ctx: root, runtime: r, requester: approvalRequesterFunc(func(ctx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
				entered <- req
				<-ctx.Done()
				close(workerDone)
				<-releaseRequester
				return agent.ApprovalResponse{}, ctx.Err()
			})}}
			waiter, stopWaiter := context.WithCancel(root)
			defer stopWaiter()
			done := make(chan error, 1)
			go func() { _, err := target.resolvePolicyApproval(waiter, request); done <- err }()
			req := <-entered
			var flightDone <-chan struct{}
			r.authorizationMu.Lock()
			for _, flight := range r.authorizationFlights {
				flightDone = flight.done
			}
			r.authorizationMu.Unlock()
			if revoke {
				if err := r.RevokeAuthorization(t.Context(), active.SessionRef, request.Authorization.Scope, "original-revocation"); err != nil {
					t.Fatal(err)
				}
			} else {
				stopWaiter()
			}
			select {
			case <-workerDone:
			case <-time.After(time.Second):
				t.Fatal("approval producer outlived last waiter/revocation")
			}
			if err := r.ResolveApproval(t.Context(), agent.ResolveApprovalRequest{SessionRef: active.SessionRef, RequestID: req.ApprovalRequestID, Decision: agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_app", Approved: true}}); err == nil {
				t.Fatal("late resolution restored cancelled consent")
			}
			releaseOnce.Do(func() { close(releaseRequester) })
			<-flightDone
			if err := <-done; err == nil {
				t.Fatal("cancelled approval succeeded")
			}
			events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			if len(session.ProjectAuthorizations(active, events).Consents) != 0 {
				t.Fatal("cancelled fact became consent")
			}
		})
	}
}

type failedConsentSettlement struct{ session.Service }

func (s failedConsentSettlement) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	if req.Event.Type == session.EventTypeLifecycle && req.Event.Approval != nil && req.Event.Approval.Status == session.ApprovalRecordResolved {
		return nil, errors.New("fixture settlement unavailable")
	}
	return s.Service.AppendEvent(ctx, req)
}

func TestAuthorizationCommittedRuleFailedSessionSettlementStopsEffect(t *testing.T) {
	r, active, store := authorizationTestSession(t)
	r.sessions = failedConsentSettlement{Service: store}
	a := authorizationTestRequirement(active)
	a.Scope.Tool = "fixture"
	a.Scope.CapabilitySet, a.Scope.CapabilityRevision, a.Scope.ResourceKind, a.Scope.Resource = "", "", "", ""
	a.Choices[0].Persistence = session.AuthorizationPolicy
	var writes, effects atomic.Int32
	r.persistPolicyConsent = func(_ context.Context, id string, _ session.AuthorizationScope) (string, uint64, error) {
		if id == "" {
			t.Error("lost original decision ID")
		}
		writes.Add(1)
		return "original-rule", 7, nil
	}
	req := authorizationTestRequest(active, a)
	target := policyWrappedTool{session: active, sessionRef: active.SessionRef, tool: tool.NamedTool{Def: req.Tool, Invoke: func(context.Context, tool.Call) (tool.Result, error) { effects.Add(1); return tool.Result{}, nil }}, approval: approvalContext{ctx: t.Context(), runtime: r, runID: "run", turnID: "turn", requester: approvalRequesterFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_app", Approved: true}, nil
	})}}
	_, err := target.requestApproval(t.Context(), req.Call, policy.Decision{Action: policy.ActionAskApproval, Approval: req.Approval, Authorization: a})
	if err == nil || !strings.Contains(err.Error(), "original-rule committed at configuration revision 7") || effects.Load() != 0 || writes.Load() != 1 {
		t.Fatalf("settlement: err=%v writes=%d effects=%d", err, writes.Load(), effects.Load())
	}
}

type replacedAuthorizationSession struct {
	session.Service
	current session.Session
}

func (s replacedAuthorizationSession) Session(context.Context, session.SessionRef) (session.Session, error) {
	return s.current, nil
}
func TestAuthorizationPendingCallRejectsCurrentSessionReplacement(t *testing.T) {
	r, active, store := authorizationTestSession(t)
	a := authorizationTestRequirement(active)
	for _, replacement := range []session.Session{func() session.Session { v := active; v.CreatedAt = v.CreatedAt.Add(time.Second); return v }(), func() session.Session { v := active; v.CWD += "-other"; return v }()} {
		r.sessions = replacedAuthorizationSession{Service: store, current: replacement}
		permit := &policyDispatchPermit{runtime: r, session: active, decision: policy.Decision{Action: policy.ActionAllow, Authorization: a}}
		if err := permit.consume(t.Context(), nil); err == nil {
			t.Fatal("old pending authorization survived a canonical Session replacement")
		}
	}
}
