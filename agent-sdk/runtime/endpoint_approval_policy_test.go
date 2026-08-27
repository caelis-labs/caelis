package runtime

import (
	"context"
	"errors"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
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
	requester := approvalRequesterFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		requesterCalls++
		return agent.ApprovalResponse{}, nil
	})
	bridge := controllerApprovalRequester{
		runtime:    runtime,
		requester:  requester,
		sessionRef: activeSession.SessionRef,
		session:    activeSession,
	}

	for i, name := range []string{shell.RunCommandToolName, "Read", "external.custom.tool"} {
		response, err := bridge.RequestControllerApproval(context.Background(), controller.ApprovalRequest{
			Mode: presets.ModeDangerFullAccess,
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
		requester: approvalRequesterFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
			requesterCalls++
			return agent.ApprovalResponse{}, nil
		}),
		sessionRef:           activeSession.SessionRef,
		session:              activeSession,
		turnID:               "participant-turn-1",
		participantID:        "side-1",
		participantKind:      string(session.ParticipantKindACP),
		participantSessionID: "remote-side-1",
	}
	response, err := bridge.RequestControllerApproval(context.Background(), controller.ApprovalRequest{
		Mode: presets.ModeDangerFullAccess,
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

func TestDangerFullAccessResolvesSubagentPermissionsBeforeApprovalRequester(t *testing.T) {
	runtime, activeSession := newDangerFullAccessEndpointTestRuntime(t, "subagent")
	requesterCalls := 0
	requester := approvalRequesterFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		requesterCalls++
		return agent.ApprovalResponse{}, nil
	})
	bridge := newSubagentApprovalRequester(runtime, presets.ModeDangerFullAccess, requester, activeSession, activeSession.SessionRef)
	response, err := bridge.RequestSubagentApproval(context.Background(), subagent.ApprovalRequest{
		Mode: presets.ModeDangerFullAccess,
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

func TestDangerFullAccessEndpointPermissionsRequireRegisteredPolicy(t *testing.T) {
	registry, err := presets.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{
		policies:          registry,
		defaultPolicyMode: presets.ModeWorkspaceWrite,
	}
	mode := runtime.policyMode(agent.AgentSpec{Metadata: map[string]any{
		policy.MetadataPolicyProfile: presets.ModeDangerFullAccess,
	}})
	if mode != presets.ModeDangerFullAccess {
		t.Fatalf("policyMode() = %q, want metadata-selected danger profile", mode)
	}
	activeSession := session.Session{SessionRef: session.SessionRef{SessionID: "sess-unregistered-danger"}}
	requesterCalls := 0
	requester := approvalRequesterFunc(func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		requesterCalls++
		return agent.ApprovalResponse{Approved: true}, nil
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
	runtime := &Runtime{policies: errorPolicyRegistry{err: wantErr}}
	_, handled, err := runtime.resolveEndpointApprovalByPolicy(context.Background(), presets.ModeDangerFullAccess, agent.ApprovalRequest{
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
	if profileErr.Profile != presets.ModeDangerFullAccess || profileErr.Detail != "unknown policy profile" {
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
		PolicyRegistry:    registry,
		DefaultPolicyMode: presets.ModeDangerFullAccess,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime, activeSession
}

func endpointApprovalTestOptions() []agent.ApprovalOption {
	return []agent.ApprovalOption{
		{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
		{ID: "reject_once", Name: "Reject once", Kind: "reject_once"},
	}
}
