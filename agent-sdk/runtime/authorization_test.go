package runtime

import (
	"context"
	"errors"
	"fmt"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPolicyIsOnlyMCPApprovalDecision(t *testing.T) {
	for _, action := range []policy.Action{policy.ActionAllow, policy.ActionDeny, policy.ActionAskApproval} {
		t.Run(string(action), func(t *testing.T) {
			var decisions, approvals, effects atomic.Int32
			def := fixtureMCPDefinition("source")
			base := tool.NamedTool{Def: def, Invoke: func(context.Context, tool.Call) (tool.Result, error) { effects.Add(1); return tool.Result{}, nil }}
			wrapped := policyAdmission{tool: base, mode: "custom", policy: policy.NamedMode{ID: "custom", Decide: func(context.Context, policy.ToolContext) (policy.Decision, error) {
				decisions.Add(1)
				return policy.Decision{Action: action, Approval: &session.ProtocolApproval{Options: []session.ProtocolApprovalOption{{ID: "approve", Kind: "allow_once"}, {ID: "reject", Kind: "reject_once"}}}}, nil
			}}, approval: approvalContext{requester: approvalRequesterFunc(func(context.Context, approval.Request) (approval.Response, error) {
				approvals.Add(1)
				return approval.Response{Outcome: "selected", OptionID: "approve", Approved: true}, nil
			})}}
			_, err := (dispatchTool{base: wrapped.tool, before: wrapped.beforeTool}).Call(t.Context(), tool.Call{ID: "original-call", Name: def.Name, Input: []byte(`{}`)})
			if err != nil && action != policy.ActionDeny {
				t.Fatal(err)
			}
			wantApproval, wantEffects := int32(0), int32(1)
			if action == policy.ActionAskApproval {
				wantApproval = 1
			}
			if action == policy.ActionDeny {
				wantEffects = 0
				if !tool.HasFailedReply(err) {
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

func authorizationTestRequest(active session.Session, a *session.ApprovalAuthorization) approval.Request {
	return approval.Request{SessionRef: active.SessionRef, Session: active, RunID: "run", TurnID: "turn", Authorization: a, Call: tool.Call{ID: "call", Name: "fixture", Input: []byte(`{"app":"com.example.A"}`)}, Tool: tool.Definition{Name: "fixture"}, Approval: &session.ProtocolApproval{Options: []session.ProtocolApprovalOption{{ID: "allow_app", Kind: "allow_once"}, {ID: "cancel", Kind: "reject_once"}}}}
}

func TestAuthorizationFlightSurvivesFirstCallerCancellation(t *testing.T) {
	r, active, store := authorizationTestSession(t)
	root, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var asks atomic.Int32
	target := policyAdmission{session: active, approval: approvalContext{ctx: root, runtime: r, requester: approvalRequesterFunc(func(ctx context.Context, _ approval.Request) (approval.Response, error) {
		asks.Add(1)
		close(entered)
		select {
		case <-release:
			return approval.Response{Outcome: "selected", OptionID: "allow_app", Approved: true}, nil
		case <-ctx.Done():
			return approval.Response{}, ctx.Err()
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
	if err := permit.consume(t.Context()); err == nil {
		t.Fatal("revoked queued operation was admitted")
	}
	if _, err := r.checkAuthorization(t.Context(), active, a); err == nil {
		t.Fatal("late approval was current")
	}
	// The fact can be reloaded without recreating an execution owner.
	evidence, err := r.authorizationEvidence(t.Context(), active)
	if err != nil || evidence.Revocations[a.Scope.Key()] == 0 {
		t.Fatalf("revocation: %+v %v", evidence, err)
	}
	a.Revocation = evidence.Revocations[a.Scope.Key()]
	permit = &policyDispatchPermit{runtime: r, session: active, decision: policy.Decision{Action: policy.ActionAllow, Authorization: a}}
	if err := permit.consume(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := permit.consume(t.Context()); err == nil {
		t.Fatal("permission was consumed twice")
	}
}

func TestSessionConsentRebuildsFromFileStoreWithoutExecuting(t *testing.T) {
	r, active, store := authorizationTestSession(t)
	a := authorizationTestRequirement(active)
	request := authorizationTestRequest(active, a)
	_, err := r.requestDurableApproval(t.Context(), request, approvalRequesterFunc(func(context.Context, approval.Request) (approval.Response, error) {
		return approval.Response{Outcome: "selected", OptionID: "allow_app", Approved: true}, nil
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
			entered := make(chan approval.Request, 1)
			workerDone := make(chan struct{})
			releaseRequester := make(chan struct{})
			var releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(releaseRequester) })
			root, stopRoot := context.WithCancel(t.Context())
			defer stopRoot()
			target := policyAdmission{session: active, approval: approvalContext{ctx: root, runtime: r, requester: approvalRequesterFunc(func(ctx context.Context, req approval.Request) (approval.Response, error) {
				entered <- req
				<-ctx.Done()
				close(workerDone)
				<-releaseRequester
				return approval.Response{}, ctx.Err()
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
			if err := r.ResolveApproval(t.Context(), approval.ResolveRequest{SessionRef: active.SessionRef, RequestID: req.ApprovalRequestID, Decision: approval.Response{Outcome: "selected", OptionID: "allow_app", Approved: true}}); err == nil {
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
	target := policyAdmission{session: active, tool: tool.NamedTool{Def: req.Tool, Invoke: func(context.Context, tool.Call) (tool.Result, error) { effects.Add(1); return tool.Result{}, nil }}, approval: approvalContext{ctx: t.Context(), runtime: r, runID: "run", turnID: "turn", requester: approvalRequesterFunc(func(context.Context, approval.Request) (approval.Response, error) {
		return approval.Response{Outcome: "selected", OptionID: "allow_app", Approved: true}, nil
	})}}
	_, err := target.requestApproval(t.Context(), req.Call, policy.Decision{Action: policy.ActionAskApproval, Approval: req.Approval, Authorization: a}, toolbinding.Dispatch{}, dispatchTool{base: target.tool}.invoke)
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
		if err := permit.consume(t.Context()); err == nil {
			t.Fatal("old pending authorization survived a canonical Session replacement")
		}
	}
}

type committedAuthorizationRevocationStore struct {
	session.Service
	fired               atomic.Bool
	failNextReadback    atomic.Bool
	appends             atomic.Int32
	unavailableReadback bool
	conflictingScope    bool
}

func (s *committedAuthorizationRevocationStore) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	if req.Event.AuthorizationRevocation != nil {
		s.appends.Add(1)
		if s.conflictingScope {
			req.Event = session.CloneEvent(req.Event)
			req.Event.AuthorizationRevocation.Scope.CapabilityRevision = "different-capability"
		}
	}
	event, err := s.Service.AppendEvent(ctx, req)
	if err == nil && req.Event.AuthorizationRevocation != nil && !s.fired.Swap(true) {
		s.failNextReadback.Store(s.unavailableReadback)
		return event, &session.CommittedError{Err: errors.New("injected post-commit revoke report loss")}
	}
	return event, err
}

func (s *committedAuthorizationRevocationStore) Events(ctx context.Context, req session.EventsRequest) ([]*session.Event, error) {
	if s.failNextReadback.Swap(false) {
		return nil, errors.New("injected unavailable revocation readback")
	}
	return s.Service.Events(ctx, req)
}

func TestAuthorizationRevokeReadbackCancelsOriginalFlight(t *testing.T) {
	for _, mode := range []string{"confirmed", "unavailable-then-original-id-retry", "conflicting-scope"} {
		t.Run(mode, func(t *testing.T) {
			r, active, store := authorizationTestSession(t)
			faults := &committedAuthorizationRevocationStore{Service: store, unavailableReadback: mode == "unavailable-then-original-id-retry", conflictingScope: mode == "conflicting-scope"}
			r.sessions = faults
			req := authorizationTestRequest(active, authorizationTestRequirement(active))
			entered := make(chan context.Context, 1)
			finished := make(chan struct{})
			target := policyAdmission{session: active, approval: approvalContext{ctx: t.Context(), runtime: r, requester: approvalRequesterFunc(func(ctx context.Context, _ approval.Request) (approval.Response, error) {
				entered <- ctx
				<-ctx.Done()
				return approval.Response{}, ctx.Err()
			})}}
			go func() { defer close(finished); _, _ = target.resolvePolicyApproval(t.Context(), req) }()
			producer := <-entered
			// Keep the original producer for readback and always join this fixture.
			defer func() {
				r.authorizationMu.Lock()
				for _, f := range r.authorizationFlights {
					f.cancel()
				}
				r.authorizationMu.Unlock()
				select {
				case <-finished:
				case <-time.After(time.Second):
					t.Error("fixture producer cleanup failed")
				}
			}()
			if err := r.RevokeAuthorization(t.Context(), active.SessionRef, req.Authorization.Scope, "original-revoke"); !session.IsCommitted(err) {
				t.Fatalf("expected original committed report: %v", err)
			}
			if mode == "confirmed" {
				if producer.Err() == nil {
					t.Fatal("confirmed post-commit readback left its producer live")
				}
			} else if producer.Err() != nil {
				t.Fatal("unconfirmed or different-scope fact cancelled the producer")
			}
			err := r.RevokeAuthorization(t.Context(), active.SessionRef, req.Authorization.Scope, "original-revoke")
			if mode == "conflicting-scope" {
				if err == nil || producer.Err() != nil {
					t.Fatalf("original ID accepted a different complete scope: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				select {
				case <-finished:
				case <-time.After(time.Second):
					t.Fatal("confirmed original revocation did not finish its pending waiter")
				}
				if producer.Err() == nil {
					t.Fatal("confirmed original revocation left its producer live")
				}
			}
			if faults.appends.Load() != 1 {
				t.Fatalf("original ID readback repeated the write: %d", faults.appends.Load())
			}
			events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, e := range events {
				if e.IdempotencyKey == "authorization-revoke:original-revoke" {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("original revocation facts=%d want=1", count)
			}
		})
	}
}

// Model the real catalog-refresh interleaving between Code Mode's target
// binding check and the single-use permission consumption. The Session lookup
// is an existing yield point inside the permission owner, before consume.
type refreshAuthorizationAtPermitStore struct {
	session.ManagedService
	fired   atomic.Bool
	refresh func() error
}

func (s *refreshAuthorizationAtPermitStore) Session(ctx context.Context, ref session.SessionRef) (session.Session, error) {
	if permit, _ := ctx.Value(policyDispatchKey{}).(*policyDispatchPermit); permit != nil && permit.decision.Authorization != nil && !s.fired.Swap(true) {
		if err := s.refresh(); err != nil {
			return session.Session{}, err
		}
	}
	return s.ManagedService.Session(ctx, ref)
}
func TestAuthorizationCodeModeUnrelatedCatalogRefreshBeforeConsume(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "fixture", Version: "1"}, &mcpsdk.ServerOptions{Capabilities: &mcpsdk.ServerCapabilities{Tools: &mcpsdk.ToolCapabilities{}}})
	var effects, asks atomic.Int32
	publish := func(name, revision string) {
		server.AddTool(&mcpsdk.Tool{Name: name, InputSchema: map[string]any{"type": "object", "required": []string{"app"}, "properties": map[string]any{"app": map[string]any{"type": "string"}, "revision_marker": map[string]any{"type": "string", "const": revision}}}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			effects.Add(1)
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "effect"}}}, nil
		})
	}
	for _, name := range []string{"look", "click"} {
		publish(name, "v1")
	}
	ep := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
	defer ep.Close()
	mgr, err := mcp.NewManager(ctx, []mcp.ServerSpec{{PluginID: "fixture", Name: "fixture", Transport: mcp.TransportStreamableHTTP, URL: ep.URL}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	<-mgr.Initialized()
	old, _, err := mgr.Lookup(ctx, "fixture__look")
	if err != nil {
		t.Fatal(err)
	}
	files, active, _ := codeModeStore(t)
	store := &refreshAuthorizationAtPermitStore{ManagedService: files, refresh: func() error {
		publish("click", "v2")
		if err := mgr.RefreshTools(ctx, "fixture", "fixture"); err != nil {
			return err
		}
		current, _, err := mgr.Lookup(ctx, "fixture__look")
		if err != nil {
			return err
		}
		if mcp.BindingForDefinition(current.Definition()) != mcp.BindingForDefinition(old.Definition()) {
			return errors.New("unrelated refresh changed the pinned target")
		}
		return nil
	}}
	policies, err := policy.NewMemory(sourceGroupFixtureMode())
	if err != nil {
		t.Fatal(err)
	}
	llm := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "original-script", Name: "js_exec", Args: `{"code":"return await tools.call('fixture__look',{app:'com.example.A'});","yield_time_ms":60000}`}}
		}
		return nil
	}}
	rt, err := New(Config{Sessions: store, TaskStore: sessionfile.NewTaskStore(files), AgentFactory: chat.Factory{}, Hooks: hook.Set{ToolPolicy: policies}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.ReleaseCodeModeEnvironments(); err != nil {
			t.Error(err)
		}
	}()
	run, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "fixture", AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{&codemode.Tool{Name: "js_exec", Executor: &gojaexec.Executor{}}}, DeferredTools: mgr}, ApprovalRequester: approvalRequesterFunc(func(context.Context, approval.Request) (approval.Response, error) {
		asks.Add(1)
		return approval.Response{Outcome: "selected", OptionID: "allow_session", Approved: true}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Handle.WaitCompletion(ctx); err != nil {
		t.Fatal(err)
	}
	events, _ := files.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	for _, e := range events {
		if e.Type == session.EventTypeToolResult && e.Tool != nil {
			t.Logf("result %s: %v", e.Tool.Name, e.Tool.Output)
		}
	}
	t.Logf("refresh before permission consume=%v approvals=%d effects=%d", store.fired.Load(), asks.Load(), effects.Load())
	if !store.fired.Load() || asks.Load() != 1 {
		t.Fatal("fixture did not reach permission boundary")
	}
	if effects.Load() != 1 {
		t.Fatalf("unrelated refresh prevented the approved call: effects=%d want=1", effects.Load())
	}
}

// This SDK fixture asks for a generic source capability group. The product's
// DTW scope construction is tested by control/toolpolicy, outside the SDK.
func sourceGroupFixtureMode() policy.Mode {
	return policy.NamedMode{ID: policy.ProfileWorkspaceWrite, Decide: func(_ context.Context, input policy.ToolContext) (policy.Decision, error) {
		if !mcp.IsDefinition(input.Tool) {
			return policy.Decision{Action: policy.ActionAllow}, nil
		}
		decision, err := mcpFixtureApproval(input, "allow_session")
		if err != nil {
			return policy.Decision{}, err
		}
		authorization := authorizationTestRequirement(input.Session)
		authorization.Scope.CapabilitySet = "fixture-source-group"
		authorization.Scope.ResourceKind = "fixture"
		authorization.Choices = []session.AuthorizationChoice{{OptionID: "allow_session", Persistence: session.AuthorizationSession}}
		decision.Authorization = authorization
		return decision, nil
	}}
}
