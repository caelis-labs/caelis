package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestApprovalOriginBuiltinNoOptionsNormalizationAndReason(t *testing.T) {
	var captured agent.ApprovalRequest
	invoked := false
	wrapped := policyWrappedTool{sessionRef: session.SessionRef{SessionID: "s"}, tool: tool.NamedTool{Def: tool.Definition{Name: "Probe"}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { invoked = true; return tool.Result{}, nil }}, approval: approvalContext{requester: approvalRequesterFunc(func(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		captured = req
		return agent.ApprovalResponse{OptionID: "reject_once", Outcome: "selected", Reason: "Destructive target is not authorized."}, nil
	})}}
	result, err := wrapped.requestApproval(t.Context(), tool.Call{ID: "call", Name: "Probe"}, policy.Decision{Action: policy.ActionAskApproval, Approval: &session.ProtocolApproval{ToolCall: session.ProtocolToolCall{ID: "call", Name: "Probe"}}})
	if err != nil {
		t.Fatal(err)
	}
	if invoked || captured.Origin == nil || captured.Origin.Role != agent.ApprovalRoleMain || captured.Origin.Endpoint != agent.ApprovalEndpointBuiltin || len(captured.Approval.Options) != 2 {
		t.Fatalf("bad admission: %+v invoked=%v", captured, invoked)
	}
	encoded, _ := json.Marshal(result)
	if !result.IsError || !strings.Contains(string(encoded), "Destructive target is not authorized.") {
		t.Fatalf("denial reason did not reach tool result: %+v", result)
	}
}

func TestApprovalOriginEndpointRoleOrthogonality(t *testing.T) {
	for _, endpoint := range []agent.ApprovalEndpoint{agent.ApprovalEndpointBuiltin, agent.ApprovalEndpointExternalACP} {
		var captured agent.ApprovalRequest
		bridge := subagentApprovalRequester{sessionRef: session.SessionRef{SessionID: "parent"}, requester: approvalRequesterFunc(func(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
			captured = req
			return agent.ApprovalResponse{Reason: "Exact denial"}, nil
		})}
		response, err := bridge.RequestSubagentApproval(t.Context(), agent.SubagentApprovalRequest{Origin: agent.ApprovalOrigin{Endpoint: endpoint, SessionID: "child"}, TaskID: "task", ParentCallID: "spawn", ToolCall: agent.EndpointApprovalToolCall{ID: "call"}})
		if err != nil || response.Reason != "Exact denial" {
			t.Fatalf("response=%+v err=%v", response, err)
		}
		o := captured.Origin
		if o.Role != agent.ApprovalRoleSubagent || o.Endpoint != endpoint || o.SessionID != "child" || o.ParentSessionID != "parent" || o.TaskID != "task" || o.ParentCallID != "spawn" || o.ToolCallID != "call" {
			t.Fatalf("origin=%+v", o)
		}
	}
	var captured agent.ApprovalRequest
	bridge := controllerApprovalRequester{sessionRef: session.SessionRef{SessionID: "parent"}, requester: approvalRequesterFunc(func(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		captured = req
		return agent.ApprovalResponse{}, nil
	})}
	_, err := bridge.RequestControllerApproval(t.Context(), controller.ApprovalRequest{EndpointSessionID: "remote", ToolCall: agent.EndpointApprovalToolCall{ID: "call"}})
	if err != nil {
		t.Fatal(err)
	}
	if captured.Origin.Role != agent.ApprovalRoleMain || captured.Origin.Endpoint != agent.ApprovalEndpointExternalACP || captured.Origin.SessionID != "remote" {
		t.Fatalf("origin=%+v", captured.Origin)
	}
}
