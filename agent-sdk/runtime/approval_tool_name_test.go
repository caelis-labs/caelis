package runtime

import (
	"context"
	"reflect"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
)

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
				var captured agent.ApprovalRequest
				requester := approvalRequesterFunc(func(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
					captured = req
					return agent.ApprovalResponse{}, nil
				})
				call := agent.EndpointApprovalToolCall{ID: "call-1", Name: tc.name, NamePresent: tc.present, Title: "Read file", Kind: "read"}
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
