package policy

import (
	"cmp"
	"context"
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/internal/sandboxmeta"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// BeforeTool evaluates the host's mode against the final call. The caller
// supplies allow for guarded dispatch and ask for approval settlement.
func BeforeTool(ctx context.Context, mode Mode, input ToolContext, allow, ask func(Decision) (tool.Result, error)) (tool.Result, error) {
	if mode == nil {
		return tool.Result{}, &ProfileError{Profile: input.Mode, Detail: "policy mode is unavailable"}
	}
	decision, err := mode.DecideTool(ctx, CloneToolContext(input))
	if err != nil {
		return tool.Result{}, err
	}
	decision, err = NormalizeDecision(input.Mode, decision)
	if err != nil {
		return tool.Result{}, err
	}
	switch decision.Action {
	case ActionAllow:
		return allow(decision)
	case ActionAskApproval:
		return ask(decision)
	case ActionDeny:
		return decision.Refusal(input.Tool, approval.Response{})
	default:
		return tool.Result{}, &DecisionError{Mode: input.Mode, Detail: "unhandled normalized decision"}
	}
}

// EffectiveCall attaches the policy's execution constraints to a private call.
func (d Decision) EffectiveCall(call tool.Call) tool.Call {
	call = tool.CloneCall(call)
	if call.Metadata == nil {
		call.Metadata = map[string]any{}
	}
	if !constraintsIsZero(d.Constraints) {
		call.Metadata[sandboxmeta.ConstraintsKey] = d.Constraints
	}
	if d.Metadata != nil {
		call.Metadata["policy_metadata"] = d.Metadata
	}
	return call
}

// Refusal constructs a paired tool reply for a denied or unresolved request.
func (decision Decision) Refusal(
	def tool.Definition,
	outcome approval.Response,
) (tool.Result, error) {
	errorText, systemHint := policyModelFeedback(decision, outcome)
	payload := map[string]any{
		"error":     errorText,
		"tool_name": strings.TrimSpace(def.Name),
	}
	if systemHint != "" {
		payload["system_hint"] = systemHint
	}
	raw, _ := json.Marshal(payload)
	return tool.Result{
		Content: []tool.ContentBlock{tool.StructuredContent{Value: raw}},
	}, tool.FailedReply(errorText)
}

// policyModelFeedback separates factual error text from one actionable model hint.
func policyModelFeedback(decision Decision, outcome approval.Response) (errorText string, systemHint string) {
	reason := strings.TrimSpace(decision.Reason)
	outcomeReason := cmp.Or(strings.TrimSpace(outcome.Reason), strings.TrimSpace(outcome.ReviewText))
	approvalResolved := strings.TrimSpace(outcome.Outcome) != "" || strings.TrimSpace(outcome.OptionID) != "" || outcomeReason != ""

	switch {
	case decision.Action == ActionAskApproval && approvalResolved && !outcome.Approved:
		errorText = cmp.Or(outcomeReason, "approval denied")
	case decision.Action == ActionAskApproval:
		errorText = cmp.Or(reason, "approval required")
		systemHint = "This operation will run only after approval; keep using the same tool rather than shell workarounds."
	case reason != "":
		errorText = reason
		systemHint = "Follow the policy error exactly; do not bypass it with a different command or shell trick."
	default:
		errorText = "tool denied by policy"
		systemHint = "Follow the policy error exactly; do not bypass it with a different command or shell trick."
	}
	return errorText, systemHint
}

func constraintsIsZero(in sandbox.Constraints) bool {
	return in.Route == "" &&
		in.Backend == "" &&
		in.Permission == "" &&
		in.Isolation == "" &&
		in.Network == "" &&
		len(in.PathRules) == 0
}

// PermissionRequest builds the native approval ticket for the effective call.
// Its option set and origin are detached from the policy and tool.
func (decision Decision) PermissionRequest(ctx context.Context, active session.Session, runID, turnID string, def tool.Definition, call tool.Call, producingModel model.LLM, step *tool.ModelStepRef) approval.Request {
	request := approval.Request{
		Authorization:  session.CloneApprovalAuthorization(decision.Authorization),
		Origin:         &approval.Origin{WorkingDirectory: active.CWD, Role: approval.RoleMain, Endpoint: approval.EndpointBuiltin, SessionID: active.SessionID, ToolCallID: call.ID},
		SessionRef:     active.SessionRef,
		Session:        session.CloneSession(active),
		RunID:          strings.TrimSpace(runID),
		TurnID:         strings.TrimSpace(turnID),
		Tool:           tool.CloneDefinition(def),
		Call:           tool.CloneCall(call),
		Execution:      tool.InvocationFromContext(ctx),
		Parent:         tool.ParentFromContext(ctx),
		ProducingModel: producingModel,
		ModelStep:      tool.CloneModelStepRef(step),
		Metadata:       session.CloneState(decision.Metadata),
	}
	if decision.Approval != nil {
		payload := session.CloneProtocolApproval(*decision.Approval)
		request.Approval = &payload
	}
	if request.Approval != nil && len(request.Approval.Options) == 0 {
		request.Approval.Options = []session.ProtocolApprovalOption{{ID: "allow_once", Name: "Allow once", Kind: "allow_once"}, {ID: "reject_once", Name: "Reject", Kind: "reject_once"}}
	}
	return request
}
