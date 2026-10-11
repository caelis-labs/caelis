package subagent

import (
	"context"
	"errors"
	"testing"

	v2 "github.com/caelis-labs/acp-go-sdk/experimental/v2"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
)

func TestDraftChildPermissionIndependentContextAndObservationBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, bound, requested string
		observation, legacy    bool
	}{
		{name: "matched", bound: "child", requested: "child"}, {name: "mismatch", bound: "child", requested: "other"}, {name: "unbound", requested: "child"}, {name: "observation", bound: "child", requested: "child", observation: true}, {name: "stable-only bridge", bound: "child", requested: "child", legacy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			var observed tasksubagent.ApprovalRequest
			spawn := tasksubagent.SpawnContext{SessionRef: session.SessionRef{SessionID: "parent"}, TaskID: "task", ParentCallID: "spawn", ApprovalRequester: subagentApprovalRequesterFunc(func(_ context.Context, req tasksubagent.ApprovalRequest) (tasksubagent.ApprovalResponse, error) {
				calls++
				observed = req
				return agent.ApprovalResponse{Outcome: "selected", OptionID: "native-allow", Approved: true}, nil
			})}
			runner := &Runner{}
			if tc.legacy {
				runner.permissionBridge = &recordingPermissionBridge{}
			}
			run := &childRun{anchor: delegation.Anchor{SessionID: tc.bound}, observationOnly: tc.observation}
			req := v2.RequestPermissionRequest{SessionId: v2.SessionId(tc.requested), Title: "Independent command", Options: []v2.PermissionOption{{OptionId: "native-allow", Name: "Allow", Kind: "allow_once"}}, Subject: &v2.RequestPermissionSubject{Command: &v2.RequestPermissionSubjectCommand{Type: "command", Command: "git fetch", Cwd: "/workspace"}}}
			response, err := runner.boundDraftPermissionHandler(run, spawn, AgentConfig{Name: "helper"}, "helper")(t.Context(), req)
			if tc.bound == "" || tc.bound != tc.requested {
				if !errors.Is(err, errACPPermissionSessionMismatchChild) || calls != 0 {
					t.Fatalf("mismatched child entered approval: %v", err)
				}
				return
			}
			if tc.observation || tc.legacy {
				if err != nil || response.Outcome.Cancelled == nil || calls != 0 {
					t.Fatalf("observer or unsupported callback granted: %+v %v", response, err)
				}
				return
			}
			if err != nil || response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != "native-allow" || calls != 1 || observed.Origin.SessionID != "child" || observed.Origin.ParentSessionID != "parent" || observed.TaskID != "task" || observed.Approval == nil || observed.Approval.ToolCall.ID != "" {
				t.Fatalf("child context or resolve changed: %+v %+v %v", observed, response, err)
			}
		})
	}
}
