package runtime

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestEndpointIndependentPermissionPreservesMachineContextWithoutFakeTool(t *testing.T) {
	original := &session.ProtocolApproval{Context: &session.PermissionContext{Title: "Original title", Description: new("Original description"), RawSubject: json.RawMessage(`{"type":"command","command":"exact command","cwd":"/workspace"}`), NativeRequestID: json.RawMessage(`"original-jsonrpc-id"`)}, Options: []session.ProtocolApprovalOption{{ID: "native-allow", Name: " Allow original ", Kind: "allow_once"}}}
	for _, child := range []bool{false, true} {
		var observed agent.ApprovalRequest
		requester := approvalRequesterFunc(func(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
			observed = req
			return agent.ApprovalResponse{Outcome: "selected", OptionID: "native-allow", Approved: true}, nil
		})
		var err error
		if child {
			_, err = (subagentApprovalRequester{sessionRef: session.SessionRef{SessionID: "parent"}, requester: requester}).RequestSubagentApproval(t.Context(), agent.SubagentApprovalRequest{Origin: agent.ApprovalOrigin{SessionID: "child", Endpoint: agent.ApprovalEndpointExternalACP}, TaskID: "task", ParentCallID: "spawn", Approval: original})
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
