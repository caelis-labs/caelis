package runtime

import (
	"context"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/agent-sdk/tool/identity"
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

	allowed, err := bridge.RequestControllerApproval(context.Background(), controller.ApprovalRequest{
		Mode: presets.ModeDangerFullAccess,
		ToolCall: controller.ApprovalToolCall{
			ID:       "call-allow",
			Name:     identity.RunCommand,
			RawInput: map[string]any{"command": "go test ./..."},
		},
		Options: endpointApprovalTestOptions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !allowed.Approved || allowed.Outcome != "selected" || allowed.OptionID != "allow_once" {
		t.Fatalf("allowed response = %#v, want selected allow_once", allowed)
	}

	denied, err := bridge.RequestControllerApproval(context.Background(), controller.ApprovalRequest{
		Mode: presets.ModeDangerFullAccess,
		ToolCall: controller.ApprovalToolCall{
			ID:       "call-deny",
			Name:     "execute",
			RawInput: map[string]any{"cmd": "rm -rf /"},
		},
		Options: endpointApprovalTestOptions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if denied.Approved || denied.Outcome != "selected" || denied.OptionID != "reject_once" {
		t.Fatalf("denied response = %#v, want selected reject_once", denied)
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
			Name:     identity.RunCommand,
			RawInput: map[string]any{"command": "go test ./..."},
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
