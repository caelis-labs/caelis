package runtime

import (
	"context"
	"errors"
	"testing"

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
