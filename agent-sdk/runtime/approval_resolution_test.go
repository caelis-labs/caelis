package runtime

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestNativeApprovalDirectRequestersCannotChangeOriginalPermission(t *testing.T) {
	for _, role := range []string{"builtin", "controller", "subagent"} {
		for _, test := range []struct {
			name                 string
			decision             agent.ApprovalResponse
			mutate, valid, allow bool
		}{
			{"forged", agent.ApprovalResponse{Outcome: "selected", OptionID: "forged-option", Approved: true}, false, false, false},
			{"mutated_request", agent.ApprovalResponse{Outcome: "selected", OptionID: "forged-option", Approved: true}, true, false, false},
			{"original_allow", agent.ApprovalResponse{Outcome: "selected", OptionID: "native-allow"}, false, true, true},
			{"original_deny", agent.ApprovalResponse{Outcome: "selected", OptionID: "native-reject", Approved: true}, false, true, false},
			{"original_cancel", agent.ApprovalResponse{Outcome: "cancelled", Approved: true}, false, true, false},
		} {
			t.Run(role+"/"+test.name, func(t *testing.T) {
				permission := &session.ProtocolApproval{Context: &session.PermissionContext{RawSubject: json.RawMessage(`{"type":"command","command":"exact"}`), NativeRequestID: json.RawMessage(`"native-rpc"`)},
					Options: []session.ProtocolApprovalOption{{ID: "native-allow", Kind: "allow_once"}, {ID: "native-reject", Kind: "reject_once"}}}
				requester := approvalRequesterFunc(func(_ context.Context, request agent.ApprovalRequest) (agent.ApprovalResponse, error) {
					if test.mutate {
						request.Approval.Options[0].ID = "forged-option"
					}
					return test.decision, nil
				})
				var err error
				var allowed bool
				switch role {
				case "builtin":
					wrapped := policyWrappedTool{tool: tool.NamedTool{Def: tool.Definition{Name: "Probe"}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { allowed = true; return tool.Result{}, nil }}, approval: approvalContext{requester: requester}}
					_, err = wrapped.requestApproval(t.Context(), tool.Call{ID: "native-call", Name: "Probe"}, policy.Decision{Action: policy.ActionAskApproval, Approval: permission})
				case "controller":
					response, responseErr := (controllerApprovalRequester{sessionRef: session.SessionRef{SessionID: "owner"}, requester: requester}).RequestControllerApproval(t.Context(), controller.ApprovalRequest{EndpointSessionID: "remote", Approval: permission})
					allowed, err = response.Approved, responseErr
				case "subagent":
					response, responseErr := (subagentApprovalRequester{sessionRef: session.SessionRef{SessionID: "parent"}, requester: requester}).RequestSubagentApproval(t.Context(), agent.SubagentApprovalRequest{Origin: agent.ApprovalOrigin{SessionID: "child", Endpoint: agent.ApprovalEndpointExternalACP}, TaskID: "native-task", Approval: permission})
					allowed, err = response.Approved, responseErr
				}
				if (err == nil) != test.valid || allowed != test.allow {
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
				var sessions session.Service
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
				pending := session.PauseToken{Schema: session.ExecutionJournalSchemaVersion, TokenID: "native-token", SessionID: active.SessionID, RunID: "native-run", TurnID: "native-turn", ToolCallID: "native-call", ToolName: "NativeTool", Revision: 1, Status: session.PauseTokenPending, CreatedAt: now, UpdatedAt: now,
					Input: json.RawMessage(`{"original":"exact"}`), Metadata: map[string]any{"task_id": "native-task"},
					Approval: &session.ProtocolApproval{Options: []session.ProtocolApprovalOption{{ID: "native-allow", Kind: "allow_once"}, {ID: "native-reject", Kind: "reject_once"}}},
					// Public projection cannot supply a choice missing privately.
					Presentation: &session.ProtocolApproval{Options: []session.ProtocolApprovalOption{{ID: "forged-option", Kind: "allow_once"}}},
				}
				if err := runtime.appendPauseToken(t.Context(), active.SessionRef, pending); err != nil {
					t.Fatal(err)
				}
				// Reconstruct Runtime from durable Session truth, with no live
				// request or cached permission available for validation.
				runtime, err = New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
				if err != nil {
					t.Fatal(err)
				}
				waiter := make(chan agent.ApprovalResponse, 1)
				runtime.approvalWaiters[pending.TokenID] = waiter
				before, err := sessions.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
				if err != nil {
					t.Fatal(err)
				}
				for _, invalid := range []agent.ApprovalResponse{{Outcome: "selected", OptionID: "forged-option", Approved: true}, {Outcome: "selected", OptionID: " native-allow ", Approved: true}, {Outcome: "selected", Approved: true}, {Outcome: "cancelled", OptionID: "native-allow", Approved: true}, {Outcome: "unknown", Approved: true}} {
					if err := runtime.ResolveApproval(t.Context(), agent.ResolveApprovalRequest{SessionRef: active.SessionRef, TokenID: pending.TokenID, Decision: invalid}); err == nil {
						t.Fatalf("invalid native resolution committed: %+v", invalid)
					}
					current, err := runtime.pauseToken(t.Context(), active.SessionRef, pending.TokenID)
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
				decision := agent.ApprovalResponse{Outcome: "selected", OptionID: choice, Approved: choice != "native-allow"}
				if choice == "cancelled" {
					decision.Outcome, decision.OptionID, decision.Approved = "cancelled", "", true
				}
				if err := runtime.ResolveApproval(t.Context(), agent.ResolveApprovalRequest{SessionRef: active.SessionRef, TokenID: pending.TokenID, Decision: decision}); err != nil {
					t.Fatal(err)
				}
				resolved, err := runtime.pauseToken(t.Context(), active.SessionRef, pending.TokenID)
				if err != nil || resolved.Status != session.PauseTokenResolved || resolved.Approved != (choice == "native-allow") || resolved.OptionID != decision.OptionID || string(resolved.Input) != string(pending.Input) || !reflect.DeepEqual(resolved.Approval, pending.Approval) || !reflect.DeepEqual(resolved.Metadata, pending.Metadata) {
					t.Fatalf("canonical resolution: %+v %v", resolved, err)
				}
				if got := <-waiter; got.Approved != resolved.Approved || got.OptionID != resolved.OptionID {
					t.Fatalf("live response: %+v", got)
				}
				// The same raw selected request is idempotent after owner
				// normalization and redelivers only the committed native decision.
				if err := runtime.ResolveApproval(t.Context(), agent.ResolveApprovalRequest{SessionRef: active.SessionRef, TokenID: pending.TokenID, Decision: decision}); err != nil {
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
