package runtime

import (
	"context"
	"encoding/json"
	"errors"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	"reflect"
	"testing"
)

func TestDangerFullAccessResolvesControllerPermissionsBeforeApprovalRequester(t *testing.T) {
	runtime, activeSession := newDangerFullAccessEndpointTestRuntime(t, "controller")
	requesterCalls := 0
	requester := approvalRequesterFunc(func(context.Context, approval.Request) (approval.Response, error) {
		requesterCalls++
		return approval.Response{}, nil
	})
	bridge := controllerApprovalRequester{
		runtime:    runtime,
		requester:  requester,
		sessionRef: activeSession.SessionRef,
		session:    activeSession,
	}

	for i, name := range []string{shell.RunCommandToolName, "Read", "external.custom.tool"} {
		response, err := bridge.RequestControllerApproval(context.Background(), controller.ApprovalRequest{
			Mode: policy.ProfileDangerFullAccess,
			ToolCall: controller.ApprovalToolCall{
				ID:       "call-display-name-" + name,
				Name:     name,
				Kind:     "execute",
				RawInput: map[string]any{"cmd": "rm -rf /"},
			},
			Options: endpointApprovalTestOptions(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if !response.Approved || response.Outcome != "selected" || response.OptionID != "allow_once" {
			t.Fatalf("case %d (%q) response = %#v, want selected allow_once", i, name, response)
		}
	}
	if requesterCalls != 0 {
		t.Fatalf("ApprovalRequester calls = %d, want zero in danger-full-access", requesterCalls)
	}
}

func TestDangerFullAccessResolvesParticipantPermissionsBeforeApprovalRequester(t *testing.T) {
	runtime, activeSession := newDangerFullAccessEndpointTestRuntime(t, "participant")
	requesterCalls := 0
	bridge := controllerApprovalRequester{
		runtime: runtime,
		requester: approvalRequesterFunc(func(context.Context, approval.Request) (approval.Response, error) {
			requesterCalls++
			return approval.Response{}, nil
		}),
		sessionRef:           activeSession.SessionRef,
		session:              activeSession,
		turnID:               "participant-turn-1",
		participantID:        "side-1",
		participantKind:      string(session.ParticipantKindACP),
		participantSessionID: "remote-side-1",
	}
	response, err := bridge.RequestControllerApproval(context.Background(), controller.ApprovalRequest{
		Mode: policy.ProfileDangerFullAccess,
		ToolCall: controller.ApprovalToolCall{
			ID:       "call-participant",
			Name:     "external.custom.tool",
			Kind:     "execute",
			RawInput: map[string]any{"cmd": "rm -rf /"},
		},
		Options: endpointApprovalTestOptions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Approved || response.Outcome != "selected" || response.OptionID != "allow_once" {
		t.Fatalf("participant response = %#v, want selected allow_once", response)
	}
	if requesterCalls != 0 {
		t.Fatalf("ApprovalRequester calls = %d, want zero in danger-full-access", requesterCalls)
	}
}

func TestDetachedSubagentApprovalAcrossParentTurns(t *testing.T) {
	for _, cancelApproval := range []bool{false, true} {
		t.Run(map[bool]string{false: "settle_after_parent_starts", true: "register_and_cancel_during_parent"}[cancelApproval], func(t *testing.T) {
			store, active := newTestSessionService(t, "parent")
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
			if err != nil {
				t.Fatal(err)
			}
			previous, release, err := store.AcquireWriter(t.Context(), active.SessionRef, "spawn-turn")
			if err != nil {
				t.Fatal(err)
			}
			if err = release(); err != nil {
				t.Fatal(err)
			}
			startParent := func() {
				t.Helper()
				blocking := &blockingTestAgent{started: make(chan struct{})}
				run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Agent: blocking, Input: "next"})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_ = run.Handle.Close()
					_ = run.Handle.WaitCompletion(context.Background())
				})
				<-blocking.started
			}
			if cancelApproval {
				startParent()
			}
			var requestID string
			bridge := newSubagentApprovalRequester(rt, policy.ProfileWorkspaceWrite, approvalRequesterFunc(func(_ context.Context, req approval.Request) (approval.Response, error) {
				requestID = req.ApprovalRequestID
				if cancelApproval {
					return approval.Response{}, context.Canceled
				}
				startParent()
				return approval.Response{Outcome: "selected", OptionID: "allow_once"}, nil
			}), active, active.SessionRef)
			resp, err := bridge.RequestSubagentApproval(previous, subagent.ApprovalRequest{TaskID: "child", ParentCallID: "spawn", ToolCall: subagent.ApprovalToolCall{ID: "req", Name: "lookup"}, Options: endpointApprovalTestOptions()})
			want := session.ApprovalRecordResolved
			if cancelApproval {
				want = session.ApprovalRecordCancelled
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel: %v", err)
				}
			} else if err != nil || !resp.Approved {
				t.Fatalf("settle: %+v, %v", resp, err)
			}
			record, err := rt.approvalRecord(t.Context(), active.SessionRef, requestID)
			if err != nil || record.Status != want || record.Revision != 2 {
				t.Fatalf("durable record=%+v err=%v", record, err)
			}
		})
	}
}

func TestDangerFullAccessResolvesSubagentPermissionsBeforeApprovalRequester(t *testing.T) {
	runtime, activeSession := newDangerFullAccessEndpointTestRuntime(t, "subagent")
	requesterCalls := 0
	requester := approvalRequesterFunc(func(context.Context, approval.Request) (approval.Response, error) {
		requesterCalls++
		return approval.Response{}, nil
	})
	bridge := newSubagentApprovalRequester(runtime, policy.ProfileDangerFullAccess, requester, activeSession, activeSession.SessionRef)
	response, err := bridge.RequestSubagentApproval(context.Background(), subagent.ApprovalRequest{
		Mode: policy.ProfileDangerFullAccess,
		ToolCall: subagent.ApprovalToolCall{
			ID:       "call-subagent",
			Name:     "external.custom.tool",
			Kind:     "execute",
			RawInput: map[string]any{"cmd": "rm -rf /"},
		},
		Options: endpointApprovalTestOptions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Approved || response.Outcome != "selected" || response.OptionID != "allow_once" {
		t.Fatalf("subagent response = %#v, want selected allow_once", response)
	}
	if requesterCalls != 0 {
		t.Fatalf("ApprovalRequester calls = %d, want zero in danger-full-access", requesterCalls)
	}
}

func TestDangerFullAccessRejectsMalformedEndpointPermissionOptions(t *testing.T) {
	runtime, activeSession := newDangerFullAccessEndpointTestRuntime(t, "malformed-options")
	requesterCalls := 0
	bridge := controllerApprovalRequester{
		runtime: runtime,
		requester: approvalRequesterFunc(func(context.Context, approval.Request) (approval.Response, error) {
			requesterCalls++
			return approval.Response{Approved: true}, nil
		}),
		sessionRef: activeSession.SessionRef,
		session:    activeSession,
	}
	tests := map[string][]approval.Option{
		"unknown kind":   {{ID: "allow_once", Name: "Allow once", Kind: "vendor_custom"}},
		"uppercase kind": {{ID: "allow_once", Name: "Allow once", Kind: "ALLOW_ONCE"}},
		"spaced kind":    {{ID: "allow_once", Name: "Allow once", Kind: " allow_once "}},
		"duplicate id": {
			{ID: "same", Name: "Allow", Kind: "allow_once"},
			{ID: "same", Name: "Reject", Kind: "reject_once"},
		},
		"blank id":  {{ID: " ", Name: "Allow", Kind: "allow_once"}},
		"spaced id": {{ID: " allow_once ", Name: "Allow", Kind: "allow_once"}},
	}
	for name, options := range tests {
		t.Run(name, func(t *testing.T) {
			response, err := bridge.RequestControllerApproval(context.Background(), controller.ApprovalRequest{
				Mode: policy.ProfileDangerFullAccess,
				ToolCall: controller.ApprovalToolCall{
					ID: "call-malformed", Name: "external.custom.tool", Kind: "execute",
				},
				Options: options,
			})
			if err == nil {
				t.Fatalf("RequestControllerApproval() response = %#v, error = nil", response)
			}
			if response.Approved {
				t.Fatalf("RequestControllerApproval() response = %#v, want fail-closed", response)
			}
		})
	}
	if requesterCalls != 0 {
		t.Fatalf("ApprovalRequester calls = %d, want zero for malformed full-access options", requesterCalls)
	}
}

func TestDangerFullAccessEndpointPermissionsRequireRegisteredPolicy(t *testing.T) {
	registry, err := presets.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{
		hooks:             hook.Set{ToolPolicy: registry},
		defaultPolicyMode: policy.ProfileWorkspaceWrite,
	}
	mode := runtime.policyMode(agent.AgentSpec{Metadata: map[string]any{
		policy.MetadataPolicyProfile: policy.ProfileDangerFullAccess,
	}})
	if mode != policy.ProfileDangerFullAccess {
		t.Fatalf("policyMode() = %q, want metadata-selected danger profile", mode)
	}
	activeSession := session.Session{SessionRef: session.SessionRef{SessionID: "sess-unregistered-danger"}}
	requesterCalls := 0
	requester := approvalRequesterFunc(func(context.Context, approval.Request) (approval.Response, error) {
		requesterCalls++
		return approval.Response{Approved: true}, nil
	})

	for _, participant := range []bool{false, true} {
		bridge := controllerApprovalRequester{
			runtime:    runtime,
			requester:  requester,
			sessionRef: activeSession.SessionRef,
			session:    activeSession,
		}
		if participant {
			bridge.participantID = "side-1"
			bridge.participantKind = string(session.ParticipantKindACP)
			bridge.participantSessionID = "remote-side-1"
		}
		_, err := bridge.RequestControllerApproval(context.Background(), controller.ApprovalRequest{
			Mode: mode,
			ToolCall: controller.ApprovalToolCall{
				ID:   "call-unregistered",
				Name: "external.custom.tool",
			},
			Options: endpointApprovalTestOptions(),
		})
		assertUnknownDangerProfile(t, err)
	}

	subagentBridge := newSubagentApprovalRequester(runtime, mode, requester, activeSession, activeSession.SessionRef)
	if subagentBridge == nil {
		t.Fatal("subagent approval requester = nil, want fail-closed policy bridge")
	}
	_, err = subagentBridge.RequestSubagentApproval(context.Background(), subagent.ApprovalRequest{
		Mode: mode,
		ToolCall: subagent.ApprovalToolCall{
			ID:   "call-subagent-unregistered",
			Name: "external.custom.tool",
		},
		Options: endpointApprovalTestOptions(),
	})
	assertUnknownDangerProfile(t, err)
	if requesterCalls != 0 {
		t.Fatalf("ApprovalRequester calls = %d, want zero for unregistered danger profile", requesterCalls)
	}
}

func TestDangerFullAccessEndpointPolicyRegistryFailureFailsClosed(t *testing.T) {
	wantErr := errors.New("registry unavailable")
	runtime := &Runtime{hooks: hook.Set{ToolPolicy: errorPolicyRegistry{err: wantErr}}}
	_, handled, err := runtime.resolveEndpointApprovalByPolicy(context.Background(), policy.ProfileDangerFullAccess, approval.Request{
		Approval: &session.ProtocolApproval{Options: []session.ProtocolApprovalOption{{ID: "allow_once", Kind: "allow_once"}}},
	})
	if !handled {
		t.Fatal("handled = false, want registered-danger validation to own the failure")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want registry error", err)
	}
	var profileErr *policy.ProfileError
	if !errors.As(err, &profileErr) {
		t.Fatalf("error = %v, want *policy.ProfileError", err)
	}
}

func assertUnknownDangerProfile(t *testing.T, err error) {
	t.Helper()
	var profileErr *policy.ProfileError
	if !errors.As(err, &profileErr) {
		t.Fatalf("error = %v, want *policy.ProfileError", err)
	}
	if profileErr.Profile != policy.ProfileDangerFullAccess || profileErr.Detail != "unknown policy profile" {
		t.Fatalf("profile error = %#v, want unknown danger profile", profileErr)
	}
}

func newDangerFullAccessEndpointTestRuntime(t *testing.T, suffix string) (*Runtime, session.Session) {
	t.Helper()
	sessions, activeSession := newTestSessionService(t, "sess-endpoint-policy-"+suffix)
	registry, err := presets.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(presets.DangerFullAccessMode()); err != nil {
		t.Fatal(err)
	}
	runtime, err := New(Config{
		Sessions:          sessions,
		AgentFactory:      chat.Factory{},
		Hooks:             hook.Set{ToolPolicy: registry},
		DefaultPolicyMode: policy.ProfileDangerFullAccess,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime, activeSession
}

func endpointApprovalTestOptions() []approval.Option {
	return []approval.Option{
		{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
		{ID: "reject_once", Name: "Reject once", Kind: "reject_once"},
	}
}

func TestEndpointApprovalRequestersPreserveNamePresence(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"controller", "subagent"} {
		for _, tc := range []struct {
			label, name string
			present     *bool
		}{
			{"native", "Read", nil},
			{"standard", " read_file ", new(true)},
			{"empty", "", new(true)},
			{"fallback", "Read file", new(false)},
		} {
			t.Run(source+"/"+tc.label, func(t *testing.T) {
				var captured approval.Request
				requester := approvalRequesterFunc(func(_ context.Context, req approval.Request) (approval.Response, error) {
					captured = req
					return approval.Response{}, nil
				})
				call := session.ProtocolToolCall{ID: "call-1", Name: tc.name, NamePresent: tc.present, Title: "Read file", Kind: "read"}
				var err error
				if source == "controller" {
					_, err = (controllerApprovalRequester{requester: requester}).RequestControllerApproval(t.Context(), controller.ApprovalRequest{ToolCall: call})
				} else {
					_, err = (subagentApprovalRequester{requester: requester}).RequestSubagentApproval(t.Context(), subagent.ApprovalRequest{ToolCall: call})
				}
				if err != nil {
					t.Fatal(err)
				}
				if captured.Approval == nil {
					t.Fatal("approval was not forwarded")
				}
				approval := session.CloneProtocolApproval(*captured.Approval)
				if approval.ToolCall.Name != tc.name || !reflect.DeepEqual(approval.ToolCall.NamePresent, tc.present) {
					t.Fatalf("approval name = %q, presence = %v; want %q, %v", approval.ToolCall.Name, approval.ToolCall.NamePresent, tc.name, tc.present)
				}
			})
		}
	}
}

func TestEndpointIndependentPermissionPreservesMachineContextWithoutFakeTool(t *testing.T) {
	original := &session.ProtocolApproval{Context: &session.PermissionContext{Title: "Original title", Description: new("Original description"), RawSubject: json.RawMessage(`{"type":"command","command":"exact command","cwd":"/workspace"}`), NativeRequestID: json.RawMessage(`"original-jsonrpc-id"`)}, Options: []session.ProtocolApprovalOption{{ID: "native-allow", Name: " Allow original ", Kind: "allow_once"}}}
	for _, child := range []bool{false, true} {
		var observed approval.Request
		requester := approvalRequesterFunc(func(_ context.Context, req approval.Request) (approval.Response, error) {
			observed = req
			return approval.Response{Outcome: "selected", OptionID: "native-allow", Approved: true}, nil
		})
		var err error
		if child {
			_, err = (subagentApprovalRequester{sessionRef: session.SessionRef{SessionID: "parent"}, requester: requester}).RequestSubagentApproval(t.Context(), approval.SubagentRequest{Origin: approval.Origin{SessionID: "child", Endpoint: approval.EndpointExternalACP}, TaskID: "task", ParentCallID: "spawn", Approval: original})
		} else {
			_, err = (controllerApprovalRequester{sessionRef: session.SessionRef{SessionID: "owner"}, requester: requester}).RequestControllerApproval(t.Context(), controller.ApprovalRequest{EndpointSessionID: "remote", Approval: original})
		}
		if err != nil || !reflect.DeepEqual(observed.Approval, original) || observed.Call.ID != "" || observed.Call.Name != "" || len(observed.Call.Input) != 0 || observed.Tool.Name != "" || observed.Origin.ToolCallID != "" {
			t.Fatalf("independent request became synthetic tool or lost private evidence: %+v %v", observed, err)
		}
		observed.Approval.Context.RawSubject[0] = 'x'
		if original.Context.RawSubject[0] != '{' {
			t.Fatal("endpoint evidence not isolated from requester")
		}
	}
}
