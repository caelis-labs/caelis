package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"iter"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
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

// mcpAskFixturePolicy supplies an explicit SDK per-call Ask. It proves native
// dispatch/approval behavior without relying on a removed MCP source gate.
func mcpAskFixturePolicy() policy.Registry {
	return staticPolicyRegistry{mode: policy.NamedMode{ID: "mcp-test", Decide: func(_ context.Context, input policy.ToolContext) (policy.Decision, error) {
		if !tool.IsMCPDefinition(input.Tool) {
			return policy.Decision{Action: policy.ActionAllow}, nil
		}
		decision, err := presets.MCPApprovalDecision(input, sandbox.Constraints{})
		if err != nil {
			return policy.Decision{}, err
		}
		decision.Approval.Options = []session.ProtocolApprovalOption{{ID: "allow_once", Kind: "allow_once"}, {ID: "cancel", Kind: "reject_once"}}
		binding := tool.BindingForDefinition(input.Tool)
		raw, err := json.Marshal(input.Call)
		if err != nil {
			return policy.Decision{}, err
		}
		digest := sha256.Sum256(raw)
		decision.Authorization = &session.ApprovalAuthorization{Schema: session.AuthorizationSchemaVersion, Scope: session.AuthorizationScope{Authority: "fixture", Workspace: input.Session.CWD, SessionID: input.Session.SessionID, SessionIncarnation: input.Session.CreatedAt.UTC().Format(time.RFC3339Nano), SourceID: binding.Identity, SourceRevision: binding.Version, PolicyRevision: "fixture-ask-v1", Tool: input.Tool.Name}, CallDigest: hex.EncodeToString(digest[:]), PerCall: true, Choices: []session.AuthorizationChoice{{OptionID: "allow_once", Persistence: session.AuthorizationOnce}}}
		return decision, nil
	}}}
}
