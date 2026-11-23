package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

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
