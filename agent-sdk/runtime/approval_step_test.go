package runtime

import (
	"context"
	"fmt"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestRuntimeApprovalStepWaitsForPolicyAdmissionNotEffectCompletion(t *testing.T) {
	sessions, active := newTestSessionService(t, "mixed-approval-step")
	rt, err := New(Config{
		Sessions: sessions, AgentFactory: chat.Factory{}, DefaultPolicyMode: "mixed",
		Hooks: hook.Set{ToolPolicy: staticPolicyRegistry{mode: policy.NamedMode{ID: "mixed", Decide: func(_ context.Context, input policy.ToolContext) (policy.Decision, error) {
			if input.Call.ID == "allow" {
				return policy.Decision{Action: policy.ActionAllow}, nil
			}
			return policy.Decision{Action: policy.ActionAskApproval, Approval: &session.ProtocolApproval{
				ToolCall: session.ProtocolToolCall{ID: input.Call.ID, Name: input.Tool.Name, Status: "pending"},
			}}, nil
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	barrierClosed := make(chan struct{})
	allowedEffect := make(chan struct{})
	requester := approvalRequesterFunc(func(ctx context.Context, req approval.Request) (approval.Response, error) {
		step := req.ModelStep
		if step == nil || step.ID == "" || step.Index != 1 || step.CallCount != 2 || step.AdmissionDone() == nil {
			t.Errorf("approval step = %#v; want concurrent second call of two", step)
			return approval.Response{}, fmt.Errorf("missing mixed-batch admission reference")
		}
		// The approval queue marks this index once the request is registered.
		step.MarkAdmissionComplete()
		select {
		case <-step.AdmissionDone():
			close(barrierClosed)
			return approval.Response{Outcome: "selected", OptionID: "reject_once"}, nil
		case <-ctx.Done():
			t.Errorf("mixed model step admission remained open: %v", ctx.Err())
			return approval.Response{}, ctx.Err()
		}
	})
	probe := tool.NamedTool{Def: tool.Definition{Name: "Probe"}, Invoke: func(ctx context.Context, call tool.Call) (tool.Result, error) {
		if call.ID != "allow" {
			t.Errorf("rejected call %q reached the tool", call.ID)
		}
		// Keep the allowed effect incomplete until its sibling's admission unblocks.
		select {
		case <-barrierClosed:
			close(allowedEffect)
			return tool.Result{Content: []tool.ContentBlock{tool.Text("done")}}, nil
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		}
	}}
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "allow", Name: "Probe", Args: `{}`}, {ID: "ask", Name: "Probe", Args: `{}`}}
		}
		return nil
	}}
	run, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "probe both", ApprovalRequester: requester,
		AgentSpec: agent.AgentSpec{Name: "chat", Model: driver, Tools: []tool.Tool{probe}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer run.Handle.Cancel()
	if err := run.Handle.WaitCompletion(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-allowedEffect:
	default:
		t.Fatal("allowed effect did not complete after concurrent approval admission")
	}
}
