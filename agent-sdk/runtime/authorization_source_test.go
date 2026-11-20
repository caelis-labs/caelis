package runtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

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
			target := policyWrappedTool{session: active, approval: approvalContext{ctx: t.Context(), runtime: r, requester: approvalRequesterFunc(func(ctx context.Context, _ agent.ApprovalRequest) (agent.ApprovalResponse, error) {
				entered <- ctx
				<-ctx.Done()
				return agent.ApprovalResponse{}, ctx.Err()
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
	session.Service
	fired   atomic.Bool
	refresh func() error
}

func (s *refreshAuthorizationAtPermitStore) Session(ctx context.Context, ref session.SessionRef) (session.Session, error) {
	if permit, _ := ctx.Value(policyDispatchKey{}).(*policyDispatchPermit); permit != nil && permit.decision.Authorization != nil && !s.fired.Swap(true) {
		if err := s.refresh(); err != nil {
			return session.Session{}, err
		}
	}
	return s.Service.Session(ctx, ref)
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
	store := &refreshAuthorizationAtPermitStore{Service: files, refresh: func() error {
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
	rt, err := New(Config{Sessions: store, TaskStore: sessionfile.NewTaskStore(files), AgentFactory: chat.Factory{}, PolicyRegistry: policies})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.ReleaseCodeModeEnvironments(); err != nil {
			t.Error(err)
		}
	}()
	run, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "fixture", AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{&codemode.Tool{Name: "js_exec", Executor: &gojaexec.Executor{}}}, DeferredTools: mgr}, ApprovalRequester: approvalRequesterFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		asks.Add(1)
		return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_session", Approved: true}, nil
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
	return policy.NamedMode{ID: presets.ModeWorkspaceWrite, Decide: func(_ context.Context, input policy.ToolContext) (policy.Decision, error) {
		if !mcp.IsDefinition(input.Tool) {
			return policy.Decision{Action: policy.ActionAllow}, nil
		}
		decision, err := presets.MCPApprovalDecision(input, sandbox.Constraints{})
		if err != nil {
			return policy.Decision{}, err
		}
		authorization := authorizationTestRequirement(input.Session)
		authorization.Scope.CapabilitySet = "fixture-source-group"
		authorization.Scope.ResourceKind = "fixture"
		authorization.Choices = []session.AuthorizationChoice{{OptionID: "allow_session", Persistence: session.AuthorizationSession}}
		decision.Authorization = authorization
		decision.Approval.Options = []session.ProtocolApprovalOption{{ID: "allow_session", Kind: "allow_once"}, {ID: "cancel", Kind: "reject_once"}}
		return decision, nil
	}}
}
