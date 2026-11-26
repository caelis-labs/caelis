package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestApprovalReviewFinalSettlementReachesModel(t *testing.T) {
	options := []session.ProtocolApprovalOption{
		{ID: "allow-execution", Kind: "reject_once", Name: "Do not allow execution"},
		{ID: "stop", Kind: "allow_once", Name: "Do not reject execution"},
	}
	for _, tc := range []struct {
		name        string
		options     []session.ProtocolApprovalOption
		review      approval.ReviewResult
		wantOption  string
		wantOutcome approval.Status
		allow       bool
	}{
		{"select_reject", options, approval.ReviewResult{OptionID: "allow-execution", Approved: true}, "allow-execution", approval.StatusSelected, false},
		{"approve_by_outcome", options, approval.ReviewResult{Approved: true}, "stop", approval.StatusSelected, true},
		{"approve_without_offered_allow", options[:1], approval.ReviewResult{Outcome: "approved"}, "", approval.StatusRejected, false},
		{"cancel_with_stray_option", options, approval.ReviewResult{Outcome: "cancelled", OptionID: "stop", Approved: true}, "", "cancelled", false},
		{"approve_without_options", nil, approval.ReviewResult{Outcome: "allow"}, "", approval.StatusApproved, true},
		{"reject_without_options", nil, approval.ReviewResult{Outcome: "denied", Approved: true}, "", approval.StatusRejected, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, active, _ := codeModeStore(t)
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hook.Set{ToolPolicy: mcpFixturePolicy()}, DefaultPolicyMode: "mcp-test"})
			if err != nil {
				t.Fatal(err)
			}
			llm := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "approval-call", Name: "ApprovalProbe", Args: `{}`}}
				}
				return nil
			}}
			approver := approval.ReviewerAdapter{Reviewer: settlementReviewer{result: tc.review}}
			var requestID string
			requester := approvalRequesterFunc(func(ctx context.Context, req approval.Request) (approval.Response, error) {
				requestID = req.ApprovalRequestID
				result, err := approver.Decide(ctx, approval.ReviewRequest{Approval: approval.PayloadFromRuntimeRequest(req), RuntimeRequest: req})
				if result.Approved != tc.allow || result.OptionID != tc.wantOption || result.Outcome != string(tc.wantOutcome) {
					t.Errorf("finalized review = %+v", result)
				}
				return approval.RuntimeResponseFromFinalReview(result), err
			})
			var effects atomic.Int32
			probe := tool.NamedTool{Def: tool.Definition{Name: "ApprovalProbe"}, Invoke: func(ctx context.Context, call tool.Call) (tool.Result, error) {
				// The controller permission bridge preserves an endpoint's empty
				// option set and still uses the same durable settlement as tool policy.
				scope := lifecycleScopeFromContext(ctx)
				bridge := controllerApprovalRequester{runtime: rt, requester: requester, session: active, sessionRef: active.SessionRef, runID: scope.runID, turnID: scope.turnID}
				decision, err := bridge.RequestControllerApproval(ctx, controller.ApprovalRequest{
					EndpointSessionID: "fixture-endpoint",
					Approval:          &session.ProtocolApproval{Options: tc.options, ToolCall: session.ProtocolToolCall{ID: call.ID, Name: call.Name}},
				})
				if err != nil {
					return tool.Result{}, err
				}
				if !decision.Approved {
					return tool.Result{Content: []tool.ContentBlock{tool.Text("endpoint approval denied")}}, tool.FailedReply("endpoint approval denied")
				}
				effects.Add(1)
				return tool.Result{Content: []tool.ContentBlock{tool.Text("effect completed")}}, nil
			}}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "Run the approval probe", AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{probe}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			record, err := rt.approvalRecord(t.Context(), active.SessionRef, requestID)
			if err != nil || record.Status != session.ApprovalRecordResolved || record.Approved != tc.allow || record.OptionID != tc.wantOption || record.Outcome != string(tc.wantOutcome) {
				t.Fatalf("durable settlement = %+v, %v", record, err)
			}
			var wantEffects int32
			wantText := "endpoint approval denied"
			if tc.allow {
				wantEffects, wantText = 1, "effect completed"
			}
			if len(llm.requests) != 2 || effects.Load() != wantEffects {
				t.Fatalf("model requests = %d, effects = %d, want effects = %d", len(llm.requests), effects.Load(), wantEffects)
			}
			result := errorDeliveryModelResult(t, llm.requests[1], "approval-call")
			if result.IsError == tc.allow || !bytes.Contains(errorDeliveryJSON(t, result.Content), []byte(wantText)) {
				t.Fatalf("next model tool result = %+v, want %q", result, wantText)
			}
		})
	}
}

type settlementReviewer struct {
	result approval.ReviewResult
}

func (r settlementReviewer) ReviewApproval(context.Context, approval.ReviewRequest) (approval.ReviewResult, error) {
	return r.result, nil
}

func TestApprovalOriginBuiltinNoOptionsNormalizationAndReason(t *testing.T) {
	var captured approval.Request
	invoked := false
	wrapped := policyAdmission{session: session.Session{SessionRef: session.SessionRef{SessionID: "s"}}, tool: tool.NamedTool{Def: tool.Definition{Name: "Probe"}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { invoked = true; return tool.Result{}, nil }}, approval: approvalContext{requester: approvalRequesterFunc(func(_ context.Context, req approval.Request) (approval.Response, error) {
		captured = req
		return approval.Response{OptionID: "reject_once", Outcome: "selected", Reason: "Destructive target is not authorized."}, nil
	})}}
	result, err := wrapped.requestApproval(t.Context(), tool.Call{ID: "call", Name: "Probe"}, policy.Decision{Action: policy.ActionAskApproval, Approval: &session.ProtocolApproval{ToolCall: session.ProtocolToolCall{ID: "call", Name: "Probe"}}}, toolbinding.Dispatch{}, dispatchTool{base: wrapped.tool}.invoke)
	if !tool.HasFailedReply(err) {
		t.Fatalf("denial error = %v", err)
	}
	if invoked || captured.Origin == nil || captured.Origin.Role != approval.RoleMain || captured.Origin.Endpoint != approval.EndpointBuiltin || len(captured.Approval.Options) != 2 {
		t.Fatalf("bad admission: %+v invoked=%v", captured, invoked)
	}
	encoded, _ := json.Marshal(result)
	if !strings.Contains(string(encoded), "Destructive target is not authorized.") {
		t.Fatalf("denial reason did not reach tool result: %+v", result)
	}
}

func TestApprovalOriginEndpointRoleOrthogonality(t *testing.T) {
	for _, endpoint := range []approval.Endpoint{approval.EndpointBuiltin, approval.EndpointExternalACP} {
		var captured approval.Request
		bridge := subagentApprovalRequester{sessionRef: session.SessionRef{SessionID: "parent"}, requester: approvalRequesterFunc(func(_ context.Context, req approval.Request) (approval.Response, error) {
			captured = req
			return approval.Response{Reason: "Exact denial"}, nil
		})}
		response, err := bridge.RequestSubagentApproval(t.Context(), approval.SubagentRequest{Origin: approval.Origin{Endpoint: endpoint, SessionID: "child"}, TaskID: "task", ParentCallID: "spawn", ToolCall: session.ProtocolToolCall{ID: "call"}})
		if err != nil || response.Reason != "Exact denial" {
			t.Fatalf("response=%+v err=%v", response, err)
		}
		o := captured.Origin
		if o.Role != approval.RoleSubagent || o.Endpoint != endpoint || o.SessionID != "child" || o.ParentSessionID != "parent" || o.TaskID != "task" || o.ParentCallID != "spawn" || o.ToolCallID != "call" {
			t.Fatalf("origin=%+v", o)
		}
	}
	var captured approval.Request
	bridge := controllerApprovalRequester{sessionRef: session.SessionRef{SessionID: "parent"}, requester: approvalRequesterFunc(func(_ context.Context, req approval.Request) (approval.Response, error) {
		captured = req
		return approval.Response{}, nil
	})}
	_, err := bridge.RequestControllerApproval(t.Context(), controller.ApprovalRequest{EndpointSessionID: "remote", ToolCall: session.ProtocolToolCall{ID: "call"}})
	if err != nil {
		t.Fatal(err)
	}
	if captured.Origin.Role != approval.RoleMain || captured.Origin.Endpoint != approval.EndpointExternalACP || captured.Origin.SessionID != "remote" {
		t.Fatalf("origin=%+v", captured.Origin)
	}
}

func TestNativeApprovalDirectRequestersCannotChangeOriginalPermission(t *testing.T) {
	for _, role := range []string{"builtin", "controller", "subagent"} {
		for _, test := range []struct {
			name                 string
			decision             approval.Response
			mutate, valid, allow bool
		}{
			{"forged", approval.Response{Outcome: "selected", OptionID: "forged-option", Approved: true}, false, false, false},
			{"mutated_request", approval.Response{Outcome: "selected", OptionID: "forged-option", Approved: true}, true, false, false},
			{"original_allow", approval.Response{Outcome: "selected", OptionID: "native-allow"}, false, true, true},
			{"original_deny", approval.Response{Outcome: "selected", OptionID: "native-reject", Approved: true}, false, true, false},
			{"original_cancel", approval.Response{Outcome: "cancelled", Approved: true}, false, true, false},
		} {
			t.Run(role+"/"+test.name, func(t *testing.T) {
				permission := &session.ProtocolApproval{Context: &session.PermissionContext{RawSubject: json.RawMessage(`{"type":"command","command":"exact"}`), NativeRequestID: json.RawMessage(`"native-rpc"`)},
					Options: []session.ProtocolApprovalOption{{ID: "native-allow", Kind: "allow_once"}, {ID: "native-reject", Kind: "reject_once"}}}
				requester := approvalRequesterFunc(func(_ context.Context, request approval.Request) (approval.Response, error) {
					if test.mutate {
						request.Approval.Options[0].ID = "forged-option"
					}
					return test.decision, nil
				})
				var err error
				var allowed bool
				switch role {
				case "builtin":
					wrapped := policyAdmission{tool: tool.NamedTool{Def: tool.Definition{Name: "Probe"}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { allowed = true; return tool.Result{}, nil }}, approval: approvalContext{requester: requester}}
					_, err = wrapped.requestApproval(t.Context(), tool.Call{ID: "native-call", Name: "Probe"}, policy.Decision{Action: policy.ActionAskApproval, Approval: permission}, toolbinding.Dispatch{}, dispatchTool{base: wrapped.tool}.invoke)
				case "controller":
					response, responseErr := (controllerApprovalRequester{sessionRef: session.SessionRef{SessionID: "owner"}, requester: requester}).RequestControllerApproval(t.Context(), controller.ApprovalRequest{EndpointSessionID: "remote", Approval: permission})
					allowed, err = response.Approved, responseErr
				case "subagent":
					response, responseErr := (subagentApprovalRequester{sessionRef: session.SessionRef{SessionID: "parent"}, requester: requester}).RequestSubagentApproval(t.Context(), approval.SubagentRequest{Origin: approval.Origin{SessionID: "child", Endpoint: approval.EndpointExternalACP}, TaskID: "native-task", Approval: permission})
					allowed, err = response.Approved, responseErr
				}
				wantFailedReply := role == "builtin" && test.valid && !test.allow
				if tool.HasFailedReply(err) != wantFailedReply || (err == nil || wantFailedReply) != test.valid || allowed != test.allow {
					t.Fatalf("allowed=%v err=%v", allowed, err)
				}
				if len(permission.Options) != 2 || permission.Options[0].ID != "native-allow" || string(permission.Context.NativeRequestID) != `"native-rpc"` || string(permission.Context.RawSubject) != `{"type":"command","command":"exact"}` {
					t.Fatal("resolution changed original evidence or options")
				}
			})
		}
	}
}

func TestNativeApprovalResolutionValidatesPrivateRecoveredOptionsBeforeCommit(t *testing.T) {
	for _, backend := range []string{"memory", "file"} {
		for _, choice := range []string{"native-allow", "native-reject", "cancelled"} {
			t.Run(backend+"/"+choice, func(t *testing.T) {
				var sessions session.ManagedService
				if backend == "memory" {
					sessions = inmemory.NewStore(inmemory.Config{})
				} else {
					sessions = sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
				}
				active, err := sessions.StartSession(t.Context(), session.StartSessionRequest{AppName: "native", UserID: "owner"})
				if err != nil {
					t.Fatal(err)
				}
				runtime, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
				if err != nil {
					t.Fatal(err)
				}
				now := time.Unix(1000, 0).UTC()
				pending := session.ApprovalRecord{Schema: session.ApprovalSchemaVersion, RequestID: "native-token", SessionID: active.SessionID, RunID: "native-run", TurnID: "native-turn", ToolCallID: "native-call", ToolName: "NativeTool", Revision: 1, Status: session.ApprovalRecordPending, CreatedAt: now, UpdatedAt: now,
					Input: json.RawMessage(`{"original":"exact"}`), Metadata: map[string]any{"task_id": "native-task"},
					Approval: &session.ProtocolApproval{Options: []session.ProtocolApprovalOption{{ID: "native-allow", Kind: "allow_once"}, {ID: "native-reject", Kind: "reject_once"}}},
					// Public projection cannot supply a choice missing privately.
					Presentation: &session.ProtocolApproval{Options: []session.ProtocolApprovalOption{{ID: "forged-option", Kind: "allow_once"}}},
				}
				if err := runtime.appendApprovalRecord(t.Context(), active.SessionRef, pending, session.MutationGuard{}); err != nil {
					t.Fatal(err)
				}
				// Reconstruct Runtime from durable Session truth, with no live
				// request or cached permission available for validation.
				runtime, err = New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
				if err != nil {
					t.Fatal(err)
				}
				waiter := make(chan approval.Response, 1)
				runtime.approvalWaiters[keyForApproval(pending)] = approvalWaiter{responses: waiter, owner: t.Context()}
				before, err := sessions.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
				if err != nil {
					t.Fatal(err)
				}
				for _, invalid := range []approval.Response{{Outcome: "selected", OptionID: "forged-option", Approved: true}, {Outcome: "selected", OptionID: " native-allow ", Approved: true}, {Outcome: "selected", Approved: true}, {Outcome: "cancelled", OptionID: "native-allow", Approved: true}, {Outcome: "unknown_outcome", Approved: true}} {
					if err := runtime.ResolveApproval(t.Context(), approval.ResolveRequest{SessionRef: active.SessionRef, RequestID: pending.RequestID, Decision: invalid}); err == nil {
						t.Fatalf("invalid native resolution committed: %+v", invalid)
					}
					current, err := runtime.approvalRecord(t.Context(), active.SessionRef, pending.RequestID)
					if err != nil || !reflect.DeepEqual(current, pending) {
						t.Fatalf("invalid resolution changed private pending token: %+v %v", current, err)
					}
					after, err := sessions.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatal("invalid resolution published a durable event")
					}
					select {
					case got := <-waiter:
						t.Fatalf("invalid resolution woke execution: %+v", got)
					default:
					}
				}
				decision := approval.Response{Outcome: "selected", OptionID: choice, Approved: choice != "native-allow"}
				if choice == "cancelled" {
					decision.Outcome, decision.OptionID, decision.Approved = "cancelled", "", true
				}
				if err := runtime.ResolveApproval(t.Context(), approval.ResolveRequest{SessionRef: active.SessionRef, RequestID: pending.RequestID, Decision: decision}); err != nil {
					t.Fatal(err)
				}
				resolved, err := runtime.approvalRecord(t.Context(), active.SessionRef, pending.RequestID)
				if err != nil || resolved.Status != session.ApprovalRecordResolved || resolved.Approved != (choice == "native-allow") || resolved.OptionID != decision.OptionID || string(resolved.Input) != string(pending.Input) || !reflect.DeepEqual(resolved.Approval, pending.Approval) || !reflect.DeepEqual(resolved.Metadata, pending.Metadata) {
					t.Fatalf("canonical resolution: %+v %v", resolved, err)
				}
				if got := <-waiter; got.Approved != resolved.Approved || got.OptionID != resolved.OptionID {
					t.Fatalf("live response: %+v", got)
				}
				// The same raw selected request is idempotent after owner
				// normalization and redelivers only the committed native decision.
				if err := runtime.ResolveApproval(t.Context(), approval.ResolveRequest{SessionRef: active.SessionRef, RequestID: pending.RequestID, Decision: decision}); err != nil {
					t.Fatal(err)
				}
				if got := <-waiter; got.Approved != resolved.Approved || got.OptionID != resolved.OptionID {
					t.Fatalf("recovered response: %+v", got)
				}
				canonical, err := sessions.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
				if err != nil || len(session.PageEvents(canonical, session.EventPageRequest{Visibility: session.EventPageCanonical}).Events) != 0 {
					t.Fatal("approval resolution entered model context")
				}
			})
		}
	}
}

func TestRuntimeApprovalStepWaitsForPolicyAdmissionNotEffectCompletion(t *testing.T) {
	sessions, active := newTestSessionService(t, "mixed-approval-step")
	rt, err := New(Config{
		Sessions: sessions, AgentFactory: chat.Factory{}, DefaultPolicyMode: "mixed",
		Hooks: hook.Set{ToolPolicy: staticPolicyRegistry{mode: policy.NamedMode{ID: "mixed", Decide: func(_ context.Context, input policy.ToolContext) (policy.Decision, error) {
			if input.Call.ID == "allow" {
				return policy.Decision{Action: policy.ActionAllow}, nil
			}
			return policy.Decision{Action: policy.ActionAskApproval, Approval: &session.ProtocolApproval{
				ToolCall: session.ProtocolToolCall{ID: input.Call.ID, Name: input.Tool.Name, Status: "pending"},
			}}, nil
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	barrierClosed := make(chan struct{})
	allowedEffect := make(chan struct{})
	requester := approvalRequesterFunc(func(ctx context.Context, req approval.Request) (approval.Response, error) {
		step := req.ModelStep
		if step == nil || step.ID == "" || step.Index != 1 || step.CallCount != 2 || step.AdmissionDone() == nil {
			t.Errorf("approval step = %#v; want concurrent second call of two", step)
			return approval.Response{}, fmt.Errorf("missing mixed-batch admission reference")
		}
		// The approval queue marks this index once the request is registered.
		step.MarkAdmissionComplete()
		select {
		case <-step.AdmissionDone():
			close(barrierClosed)
			return approval.Response{Outcome: "selected", OptionID: "reject_once"}, nil
		case <-ctx.Done():
			t.Errorf("mixed model step admission remained open: %v", ctx.Err())
			return approval.Response{}, ctx.Err()
		}
	})
	probe := tool.NamedTool{Def: tool.Definition{Name: "Probe"}, Invoke: func(ctx context.Context, call tool.Call) (tool.Result, error) {
		if call.ID != "allow" {
			t.Errorf("rejected call %q reached the tool", call.ID)
		}
		// Keep the allowed effect incomplete until its sibling's admission unblocks.
		select {
		case <-barrierClosed:
			close(allowedEffect)
			return tool.Result{Content: []tool.ContentBlock{tool.Text("done")}}, nil
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		}
	}}
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "allow", Name: "Probe", Args: `{}`}, {ID: "ask", Name: "Probe", Args: `{}`}}
		}
		return nil
	}}
	run, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "probe both", ApprovalRequester: requester,
		AgentSpec: agent.AgentSpec{Name: "chat", Model: driver, Tools: []tool.Tool{probe}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer run.Handle.Cancel()
	if err := run.Handle.WaitCompletion(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-allowedEffect:
	default:
		t.Fatal("allowed effect did not complete after concurrent approval admission")
	}
}
