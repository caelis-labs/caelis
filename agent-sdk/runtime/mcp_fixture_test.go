package runtime

import (
	"context"
	"iter"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
)

type mcpScriptModel struct {
	requests []*model.Request
	steps    func(int, *model.Request) []model.ToolCall
}

func (*mcpScriptModel) Name() string { return "mcp-script" }
func (*mcpScriptModel) Capabilities() model.Capabilities {
	return runtimeTestModelCapabilities()
}
func (m *mcpScriptModel) Generate(_ context.Context, request *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.requests = append(m.requests, model.CloneRequest(request))
	index := len(m.requests) - 1
	return func(yield func(*model.StreamEvent, error) bool) {
		calls := m.steps(index, request)
		message := model.NewTextMessage(model.RoleAssistant, "done")
		reason := model.FinishReasonStop
		if len(calls) > 0 {
			message = model.MessageFromToolCalls(model.RoleAssistant, calls, "")
			reason = model.FinishReasonToolCalls
		}
		yield(model.StreamEventFromResponse(&model.Response{Message: message, Status: model.ResponseStatusCompleted, FinishReason: reason, StepComplete: true, TurnComplete: true}), nil)
	}
}
func mcpFixturePolicy() policy.Registry {
	return staticPolicyRegistry{mode: policy.NamedMode{ID: "mcp-test", Decide: func(context.Context, policy.ToolContext) (policy.Decision, error) {
		return policy.Decision{Action: policy.ActionAllow}, nil
	}}}
}
