package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
)

func resourceMCPFixture(t *testing.T) (policyWrappedTool, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	grants, err := NewMCPGrantStore(filepath.Join(t.TempDir(), "grants.json"))
	if err != nil {
		t.Fatal(err)
	}
	active := session.Session{SessionRef: session.SessionRef{SessionID: "resources"}, CWD: t.TempDir(), CreatedAt: time.Now()}
	def := fixtureMCPDefinition("resource-source")
	def.Metadata[tool.MetadataMCPResourceParameter] = "app"
	calls, approvals := new(atomic.Int32), new(atomic.Int32)
	wrapped := policyWrappedTool{session: active, sessionRef: active.SessionRef, mcpGrants: grants, policy: presets.WorkspaceWriteMode(), mode: presets.ModeWorkspaceWrite,
		tool: tool.NamedTool{Def: def, Invoke: func(context.Context, tool.Call) (tool.Result, error) { calls.Add(1); return tool.Result{}, nil }},
		approval: approvalContext{requester: approvalRequesterFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
			approvals.Add(1)
			return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_session", Approved: true}, nil
		})}}
	return wrapped, calls, approvals
}

func resourceMCPCall(value string) tool.Call {
	raw, _ := json.Marshal(map[string]string{"app": value, "source": "observe()"})
	return tool.Call{ID: "resource-call", Name: "docs__read", Input: raw}
}

func TestMCPResourceApprovalSeparatesResourceValues(t *testing.T) {
	wrapped, calls, approvals := resourceMCPFixture(t)
	for _, value := range []string{"first.app", "first.app", "second.app", "first.app"} {
		if _, err := wrapped.Call(t.Context(), resourceMCPCall(value)); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 4 || approvals.Load() != 2 {
		t.Fatalf("calls=%d approvals=%d; want 4 calls and 2 resource approvals", calls.Load(), approvals.Load())
	}
}

func TestMCPResourceApprovalRejectsInvalidParameterBeforeDispatch(t *testing.T) {
	for _, raw := range []string{`{}`, `{"app":""}`, `{"app":"  "}`, `{"app":null}`, `{"app":42}`, `{"app":{}}`, `{"nested":{"app":"first.app"}}`, `null`, `[]`, `{"app":`} {
		t.Run(raw, func(t *testing.T) {
			wrapped, calls, approvals := resourceMCPFixture(t)
			_, err := wrapped.Call(t.Context(), tool.Call{ID: "invalid", Name: "docs__read", Input: []byte(raw)})
			if err == nil || calls.Load() != 0 || approvals.Load() != 0 {
				t.Fatalf("err=%v calls=%d approvals=%d", err, calls.Load(), approvals.Load())
			}
		})
	}
}

func TestMCPResourceApprovalLegacyAndSourceTrustCannotBypass(t *testing.T) {
	for _, scope := range []string{"session", "always", "source_trusted", "full_access"} {
		t.Run(scope, func(t *testing.T) {
			wrapped, calls, approvals := resourceMCPFixture(t)
			legacy, err := mcpGrantFor(wrapped.Definition(), wrapped.session)
			if err != nil {
				t.Fatal(err)
			}
			switch scope {
			case "source_trusted":
				base := wrapped.tool.(tool.NamedTool)
				base.Def.Metadata[tool.MetadataMCPSourceTrusted] = true
				wrapped.tool = base
			case "full_access":
				wrapped.policy = presets.DangerFullAccessMode()
			default:
				sid := wrapped.sessionRef.SessionID
				if scope == "always" {
					sid = ""
				}
				if err := wrapped.mcpGrants.Grant(legacy, scope, sid); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := wrapped.Call(t.Context(), resourceMCPCall("first.app")); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 || approvals.Load() != 1 {
				t.Fatalf("legacy scope bypassed resource approval: calls=%d approvals=%d", calls.Load(), approvals.Load())
			}
		})
	}
}

func TestMCPResourceApprovalSessionSourceAndOwnerIsolation(t *testing.T) {
	for _, boundary := range []string{"session", "epoch", "source", "owner", "parameter", "workspace"} {
		t.Run(boundary, func(t *testing.T) {
			wrapped, calls, approvals := resourceMCPFixture(t)
			call := resourceMCPCall("first.app")
			if _, err := wrapped.Call(t.Context(), call); err != nil {
				t.Fatal(err)
			}
			switch boundary {
			case "session":
				wrapped.session.SessionID = "another"
				wrapped.sessionRef = wrapped.session.SessionRef
			case "epoch":
				wrapped.session.CreatedAt = wrapped.session.CreatedAt.Add(time.Second)
			case "source":
				base := wrapped.tool.(tool.NamedTool)
				base.Def.Metadata[tool.MetadataMCPSourceFingerprint] = "new-source"
				wrapped.tool = base
			case "owner":
				wrapped.mcpGrantOwner = "other-owner"
			case "parameter":
				base := wrapped.tool.(tool.NamedTool)
				base.Def.Metadata[tool.MetadataMCPResourceParameter] = "target"
				wrapped.tool = base
				call.Input = []byte(`{"target":"first.app","source":"observe()"}`)
			case "workspace":
				wrapped.session.CWD = t.TempDir()
			}
			if _, err := wrapped.Call(t.Context(), call); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 2 || approvals.Load() != 2 {
				t.Fatalf("%s boundary reused grant: calls=%d approvals=%d", boundary, calls.Load(), approvals.Load())
			}
		})
	}
}

func TestMCPResourceApprovalDurableReuseAndExactRevocation(t *testing.T) {
	wrapped, calls, approvals := resourceMCPFixture(t)
	for _, app := range []string{"first.app", "second.app"} {
		if _, err := wrapped.Call(t.Context(), resourceMCPCall(app)); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := NewMCPGrantStore(wrapped.mcpGrants.path)
	if err != nil {
		t.Fatal(err)
	}
	wrapped.mcpGrants = reopened
	requester := wrapped.approval.requester
	wrapped.approval.requester = nil // Reusable grants do not need an active requester.
	if _, err := wrapped.Call(t.Context(), resourceMCPCall("first.app")); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || approvals.Load() != 2 {
		t.Fatalf("reconstruction lost grant: calls=%d approvals=%d", calls.Load(), approvals.Load())
	}
	wrapped.approval.requester = requester
	grant, err := mcpGrantForCall(wrapped.Definition(), wrapped.session, resourceMCPCall("first.app"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Grant(grant, "always", ""); err == nil {
		t.Fatal("resource accepted Always scope")
	}
	if err := reopened.Revoke(grant, "session", wrapped.session.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped.Call(t.Context(), resourceMCPCall("second.app")); err != nil {
		t.Fatal(err)
	}
	if approvals.Load() != 2 {
		t.Fatal("revoking one resource revoked another")
	}
	if _, err := wrapped.Call(t.Context(), resourceMCPCall("first.app")); err != nil {
		t.Fatal(err)
	}
	if approvals.Load() != 3 || calls.Load() != 5 {
		t.Fatalf("revoked resource did not reapprove: approvals=%d calls=%d", approvals.Load(), calls.Load())
	}
}

func TestMCPResourceApprovalDenialCancellationAndInvalidOptionsDoNotDispatch(t *testing.T) {
	for _, option := range []string{"cancel", "allow_once", "allow_always", "unknown", "cancelled_context"} {
		t.Run(option, func(t *testing.T) {
			wrapped, calls, _ := resourceMCPFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			wrapped.approval.requester = approvalRequesterFunc(func(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
				if len(req.Approval.Options) != 2 || req.Approval.Options[0].ID != "allow_session" || req.Approval.Options[0].Kind != "allow_once" || req.Approval.Options[1].ID != "cancel" {
					t.Errorf("unexpected resource options: %+v", req.Approval.Options)
				}
				if req.Approval.ToolCall.RawInput["app"] != "first.app" || req.Tool.Metadata[tool.MetadataMCPResourceParameter] != "app" {
					t.Errorf("missing resource evidence: %+v", req)
				}
				if option == "cancelled_context" {
					cancel()
					return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_session", Approved: true}, nil
				}
				return agent.ApprovalResponse{Outcome: "selected", OptionID: option, Approved: option != "cancel"}, nil
			})
			result, err := wrapped.Call(ctx, resourceMCPCall("first.app"))
			if calls.Load() != 0 || (err == nil && !result.IsError) {
				t.Fatalf("invalid/denied approval dispatched: calls=%d result=%+v err=%v", calls.Load(), result, err)
			}
			grant, _ := mcpGrantForCall(wrapped.Definition(), wrapped.session, resourceMCPCall("first.app"))
			if allowed, err := wrapped.mcpGrants.Allows(grant, wrapped.session.SessionID); err != nil || allowed {
				t.Fatalf("invalid/denied approval stored grant: %v %v", allowed, err)
			}
		})
	}
}

func TestMCPResourceApprovalPreservesPerCallAskAndDeny(t *testing.T) {
	for _, action := range []policy.Action{policy.ActionAskApproval, policy.ActionDeny} {
		t.Run(string(action), func(t *testing.T) {
			wrapped, calls, _ := resourceMCPFixture(t)
			var resourceAsks, callAsks atomic.Int32
			wrapped.policy = policy.NamedMode{ID: "per-call", Decide: func(_ context.Context, input policy.ToolContext) (policy.Decision, error) {
				return policy.Decision{Action: action, Reason: "call-specific restriction", Approval: &session.ProtocolApproval{ToolCall: session.ProtocolToolCall{ID: input.Call.ID, Name: input.Call.Name}, Options: []session.ProtocolApprovalOption{{ID: "sensitive", Name: "Allow call", Kind: "allow_once"}, {ID: "deny", Name: "Deny", Kind: "reject_once"}}}}, nil
			}}
			wrapped.approval.requester = approvalRequesterFunc(func(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
				if req.Approval.Options[0].ID == "allow_session" {
					resourceAsks.Add(1)
					return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_session", Approved: true}, nil
				}
				callAsks.Add(1)
				return agent.ApprovalResponse{Outcome: "selected", OptionID: "deny"}, nil
			})
			for range 2 {
				result, err := wrapped.Call(t.Context(), resourceMCPCall("first.app"))
				if err != nil || !result.IsError {
					t.Fatalf("per-call denial lost: %+v %v", result, err)
				}
			}
			if calls.Load() != 0 {
				t.Fatal("denied call dispatched")
			}
			if action == policy.ActionAskApproval && (resourceAsks.Load() != 1 || callAsks.Load() != 2) {
				t.Fatalf("resource=%d per-call=%d", resourceAsks.Load(), callAsks.Load())
			}
			if action == policy.ActionDeny && (resourceAsks.Load() != 0 || callAsks.Load() != 0) {
				t.Fatal("Deny requested approval")
			}
		})
	}
}

func TestMCPResourceApprovalConcurrentFirstUseSharesOneDecision(t *testing.T) {
	for _, option := range []string{"allow_session", "cancel", "error"} {
		t.Run(option, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				wrapped, calls, approvals := resourceMCPFixture(t)
				executed := make(chan tool.Call, 8)
				base := wrapped.tool.(tool.NamedTool)
				invoke := base.Invoke
				base.Invoke = func(ctx context.Context, call tool.Call) (tool.Result, error) {
					executed <- tool.CloneCall(call)
					return invoke(ctx, call)
				}
				wrapped.tool = base
				release := make(chan struct{})
				wrapped.approval.requester = approvalRequesterFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
					approvals.Add(1)
					<-release
					if option == "error" {
						return agent.ApprovalResponse{}, errors.New("approval unavailable")
					}
					return agent.ApprovalResponse{Outcome: "selected", OptionID: option, Approved: option == "allow_session"}, nil
				})
				var workers sync.WaitGroup
				for i := range 8 {
					workers.Go(func() {
						call := resourceMCPCall("first.app")
						call.ID = fmt.Sprintf("concurrent-%d", i)
						call.Input, _ = json.Marshal(map[string]string{"app": "first.app", "source": call.ID})
						result, err := wrapped.Call(t.Context(), call)
						if option == "allow_session" && (err != nil || result.IsError) {
							t.Errorf("approved call failed: %+v %v", result, err)
						}
						if option != "allow_session" && err == nil && !result.IsError {
							t.Error("denied call succeeded")
						}
					})
				}
				synctest.Wait()
				if approvals.Load() != 1 || calls.Load() != 0 {
					t.Fatalf("first use prompts=%d calls=%d", approvals.Load(), calls.Load())
				}
				close(release)
				workers.Wait()
				wantCalls := int32(0)
				if option == "allow_session" {
					wantCalls = 8
				}
				if approvals.Load() != 1 || calls.Load() != wantCalls {
					t.Fatalf("prompts=%d calls=%d want=%d", approvals.Load(), calls.Load(), wantCalls)
				}
				close(executed)
				seen := make(map[string]bool)
				for call := range executed {
					var args map[string]string
					if err := json.Unmarshal(call.Input, &args); err != nil || args["source"] != call.ID || seen[call.ID] {
						t.Fatalf("shared approval replayed or replaced an invocation: %+v, %v", call, err)
					}
					seen[call.ID] = true
				}
				if len(wrapped.mcpGrants.resourceApprovals) != 0 {
					t.Fatal("finished approval retained pending state")
				}
			})
		})
	}
}

func TestMCPResourceApprovalRevocationRejectsPendingDecisionAndWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		wrapped, calls, approvals := resourceMCPFixture(t)
		release := make(chan struct{})
		wrapped.approval.requester = approvalRequesterFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
			approvals.Add(1)
			<-release
			return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_session", Approved: true}, nil
		})
		var workers sync.WaitGroup
		for range 3 {
			workers.Go(func() {
				if _, err := wrapped.Call(t.Context(), resourceMCPCall("first.app")); err == nil {
					t.Error("revoked pending call succeeded")
				}
			})
		}
		synctest.Wait()
		grant, _ := mcpGrantForCall(wrapped.Definition(), wrapped.session, resourceMCPCall("first.app"))
		if err := wrapped.mcpGrants.Revoke(grant, "session", wrapped.session.SessionID); err != nil {
			t.Fatal(err)
		}
		close(release)
		workers.Wait()
		if calls.Load() != 0 || approvals.Load() != 1 {
			t.Fatalf("calls=%d approvals=%d", calls.Load(), approvals.Load())
		}
		if allowed, err := wrapped.mcpGrants.Allows(grant, wrapped.session.SessionID); err != nil || allowed {
			t.Fatalf("revoked approval restored grant: %v %v", allowed, err)
		}
	})
}

func TestMCPResourceApprovalCancelledWaiterCannotCancelOtherCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		wrapped, calls, approvals := resourceMCPFixture(t)
		release := make(chan struct{})
		wrapped.approval.requester = approvalRequesterFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
			approvals.Add(1)
			<-release
			return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_session", Approved: true}, nil
		})
		done := make(chan struct{})
		go func() {
			defer close(done)
			if _, err := wrapped.Call(t.Context(), resourceMCPCall("first.app")); err != nil {
				t.Error(err)
			}
		}()
		synctest.Wait()
		ctx, cancel := context.WithCancel(t.Context())
		waiter := make(chan struct{})
		go func() {
			defer close(waiter)
			if _, err := wrapped.Call(ctx, resourceMCPCall("first.app")); !errors.Is(err, context.Canceled) {
				t.Errorf("cancelled waiter returned %v", err)
			}
		}()
		synctest.Wait()
		cancel()
		<-waiter
		close(release)
		<-done
		if approvals.Load() != 1 || calls.Load() != 1 {
			t.Fatalf("prompts=%d calls=%d", approvals.Load(), calls.Load())
		}
	})
}

func TestMCPResourceRevocationDuringPerCallApprovalPreventsDispatch(t *testing.T) {
	wrapped, calls, _ := resourceMCPFixture(t)
	grant, _ := mcpGrantForCall(wrapped.Definition(), wrapped.session, resourceMCPCall("first.app"))
	if err := wrapped.mcpGrants.Grant(grant, "session", wrapped.session.SessionID); err != nil {
		t.Fatal(err)
	}
	wrapped.policy = policy.NamedMode{ID: "sensitive", Decide: func(context.Context, policy.ToolContext) (policy.Decision, error) {
		return policy.Decision{Action: policy.ActionAskApproval, Approval: &session.ProtocolApproval{Options: []session.ProtocolApprovalOption{{ID: "allow", Name: "Allow", Kind: "allow_once"}, {ID: "reject", Name: "Reject", Kind: "reject_once"}}}}, nil
	}}
	wrapped.approval.requester = approvalRequesterFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		if err := wrapped.mcpGrants.Revoke(grant, "session", wrapped.session.SessionID); err != nil {
			t.Fatal(err)
		}
		return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow", Approved: true}, nil
	})
	if _, err := wrapped.Call(t.Context(), resourceMCPCall("first.app")); err == nil || calls.Load() != 0 {
		t.Fatalf("revoked per-call approval dispatched: err=%v calls=%d", err, calls.Load())
	}
}

func TestMCPResourceApprovalDifferentResourcesDoNotBlockEachOther(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		wrapped, calls, approvals := resourceMCPFixture(t)
		release := make(chan struct{})
		wrapped.approval.requester = approvalRequesterFunc(func(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
			approvals.Add(1)
			if req.Approval.ToolCall.RawInput["app"] == "first.app" {
				<-release
			}
			return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_session", Approved: true}, nil
		})
		done := make(chan struct{})
		go func() {
			defer close(done)
			if _, err := wrapped.Call(t.Context(), resourceMCPCall("first.app")); err != nil {
				t.Error(err)
			}
		}()
		synctest.Wait()
		if _, err := wrapped.Call(t.Context(), resourceMCPCall("second.app")); err != nil {
			t.Fatal(err)
		}
		if calls.Load() != 1 || approvals.Load() != 2 {
			t.Fatalf("resource approval serialized unrelated resources: calls=%d prompts=%d", calls.Load(), approvals.Load())
		}
		close(release)
		<-done
		if calls.Load() != 2 {
			t.Fatalf("calls=%d", calls.Load())
		}
	})
}

func TestMCPResourceApprovalRuntimeDeferredTurnsAndReconstruction(t *testing.T) {
	root := t.TempDir()
	sessions := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
	active, err := sessions.StartSession(t.Context(), session.StartSessionRequest{AppName: "fixture", UserID: "fixture", PreferredSessionID: "resource-runtime", Workspace: session.WorkspaceRef{CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	grants, err := NewMCPGrantStore(filepath.Join(root, "grants.json"))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}, MCPGrants: grants})
	if err != nil {
		t.Fatal(err)
	}
	def := fixtureMCPDefinition("resource-runtime-source")
	def.Metadata[tool.MetadataMCPResourceParameter] = "app"
	var calls, approvals atomic.Int32
	remote := tool.NamedTool{Def: def, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		calls.Add(1)
		return tool.Result{Content: []model.Part{model.NewTextPart("resource complete")}}, nil
	}}
	for turn, resource := range []string{"first.app", "first.app", "second.app", "first.app"} {
		if turn == 3 {
			sessions = sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
			grants, err = NewMCPGrantStore(filepath.Join(root, "grants.json"))
			if err != nil {
				t.Fatal(err)
			}
			runtime, err = New(Config{Sessions: sessions, AgentFactory: chat.Factory{}, MCPGrants: grants})
			if err != nil {
				t.Fatal(err)
			}
		}
		source := &runtimeDeferredSource{}
		source.publish(remote)
		llm := &retirementScriptModel{steps: func(step int, _ *model.Request) []model.ToolCall {
			switch step {
			case 0:
				return []model.ToolCall{{ID: fmt.Sprintf("search-%d", turn), Name: tool.ToolSearchToolName, Args: `{"query":"docs"}`}}
			case 1:
				return []model.ToolCall{{ID: fmt.Sprintf("call-%d", turn), Name: def.Name, Args: string(resourceMCPCall(resource).Input)}}
			default:
				return nil
			}
		}}
		tokenID := ""
		run, err := runtime.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "use resource " + resource, ApprovalRequester: approvalRequesterFunc(func(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
			approvals.Add(1)
			tokenID = req.PauseTokenID
			if turn == 1 || turn == 3 {
				t.Error("same Session resource prompted again")
			}
			if req.Tool.Metadata[tool.MetadataMCPResourceParameter] != "app" || req.Approval.ToolCall.RawInput["app"] != resource || len(req.Approval.Options) != 2 {
				t.Errorf("resource evidence lost through Runtime: %+v", req)
			}
			return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_session", Approved: true}, nil
		}), AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{toolsearch.NewSource(source, toolsearch.NewLexicalRanker())}, DeferredTools: source}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := drainRunnerEvents(t, run.Handle); err != nil {
			t.Fatal(err)
		}
		if turn == 0 || turn == 2 {
			token, err := runtime.pauseToken(t.Context(), active.SessionRef, tokenID)
			if err != nil || token.Status != session.PauseTokenResolved || token.OptionID != "allow_session" || len(token.Approval.Options) != 2 || token.Approval.ToolCall.RawInput["app"] != resource {
				t.Fatalf("resource approval journal lost scope: %+v %v", token, err)
			}
		}
	}
	if calls.Load() != 4 || approvals.Load() != 2 {
		t.Fatalf("runtime calls=%d approvals=%d", calls.Load(), approvals.Load())
	}
}

func TestMCPResourceApprovalCancelledLeaderCannotAuthorizeWaiter(t *testing.T) {
	for _, lateAllow := range []bool{false, true} {
		t.Run(fmt.Sprintf("late_allow_%t", lateAllow), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				wrapped, calls, approvals := resourceMCPFixture(t)
				release := make(chan struct{})
				wrapped.approval.requester = approvalRequesterFunc(func(ctx context.Context, _ agent.ApprovalRequest) (agent.ApprovalResponse, error) {
					approvals.Add(1)
					if lateAllow {
						<-release
						return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_session", Approved: true}, nil
					}
					<-ctx.Done()
					return agent.ApprovalResponse{}, ctx.Err()
				})
				leaderCtx, cancel := context.WithCancel(t.Context())
				var workers sync.WaitGroup
				workers.Go(func() {
					if _, err := wrapped.Call(leaderCtx, resourceMCPCall("first.app")); !errors.Is(err, context.Canceled) {
						t.Errorf("cancelled leader returned %v", err)
					}
				})
				synctest.Wait()
				workers.Go(func() {
					if _, err := wrapped.Call(t.Context(), resourceMCPCall("first.app")); !errors.Is(err, context.Canceled) {
						t.Errorf("cancelled shared approval returned %v", err)
					}
				})
				synctest.Wait()
				cancel()
				close(release)
				workers.Wait()
				if calls.Load() != 0 || approvals.Load() != 1 {
					t.Fatalf("cancelled leader leaked execution: calls=%d approvals=%d", calls.Load(), approvals.Load())
				}
				grant, _ := mcpGrantForCall(wrapped.Definition(), wrapped.session, resourceMCPCall("first.app"))
				if allowed, err := wrapped.mcpGrants.Allows(grant, wrapped.session.SessionID); err != nil || allowed {
					t.Fatalf("cancelled leader persisted grant: %v %v", allowed, err)
				}
				if len(wrapped.mcpGrants.resourceApprovals) != 0 {
					t.Fatal("cancelled shared approval retained pending state")
				}
			})
		})
	}
}

func TestMCPResourceGateMarkerCannotBeClaimedByPerCallPolicy(t *testing.T) {
	wrapped, calls, approvals := resourceMCPFixture(t)
	wrapped.policy = policy.NamedMode{ID: "same-options", Decide: func(context.Context, policy.ToolContext) (policy.Decision, error) {
		return policy.Decision{Action: policy.ActionAskApproval, Metadata: map[string]any{approval.MetadataMCPResourceGate: true}, Approval: &session.ProtocolApproval{Options: []session.ProtocolApprovalOption{{ID: "allow_session", Name: "Allow this call", Kind: "allow_once"}, {ID: "cancel", Name: "Reject", Kind: "reject_once"}}}}, nil
	}}
	wrapped.approval.requester = approvalRequesterFunc(func(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		index := approvals.Add(1)
		marker, _ := req.Metadata[approval.MetadataMCPResourceGate].(bool)
		if marker != (index == 1) {
			t.Errorf("approval %d marker=%t; only the Runtime resource gate may assert Session scope", index, marker)
		}
		if index == 3 {
			return agent.ApprovalResponse{Outcome: "selected", OptionID: "cancel"}, nil
		}
		return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow_session", Approved: true}, nil
	})
	call := resourceMCPCall("first.app")
	call.Metadata = map[string]any{approval.MetadataMCPResourceGate: true}
	if result, err := wrapped.Call(t.Context(), call); err != nil || result.IsError {
		t.Fatalf("first approved call=%+v %v", result, err)
	}
	if result, err := wrapped.Call(t.Context(), call); err != nil || !result.IsError {
		t.Fatalf("second per-call rejection=%+v %v", result, err)
	}
	if approvals.Load() != 3 || calls.Load() != 1 {
		t.Fatalf("colliding option IDs bypassed per-call approval: approvals=%d calls=%d", approvals.Load(), calls.Load())
	}
}
