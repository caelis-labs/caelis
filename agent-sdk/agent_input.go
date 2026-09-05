package agentsdk

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// AgentInputParent is the topology address of the current Agent's parent.
const AgentInputParent = session.AgentCommunicationParentHandle

// AgentInput is one Agent-to-Agent communication addressed within an Agent
// topology. The sender is supplied by the trusted runtime context rather than
// by the model-facing request.
type AgentInput struct {
	Target       string              `json:"target,omitempty"`
	Input        string              `json:"input,omitempty"`
	DisplayInput string              `json:"display_input,omitempty"`
	ContentParts []model.ContentPart `json:"content_parts,omitempty"`
}

// AgentInputSender routes Agent communication on behalf of the sender bound by
// the Agent host. It does not acknowledge remote consumption or own target
// lifecycle state.
type AgentInputSender interface {
	SendAgentInput(context.Context, AgentInput) error
}

type agentInputSenderContextKey struct{}

// WithAgentInputSender binds a trusted source-specific input route to a
// Runtime producer context.
func WithAgentInputSender(ctx context.Context, sender AgentInputSender) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if sender == nil {
		return ctx
	}
	return context.WithValue(ctx, agentInputSenderContextKey{}, sender)
}

// AgentInputSenderFromContext returns the source-bound input route installed
// by the current Agent host.
func AgentInputSenderFromContext(ctx context.Context) AgentInputSender {
	if ctx == nil {
		return nil
	}
	sender, _ := ctx.Value(agentInputSenderContextKey{}).(AgentInputSender)
	return sender
}

// CloneAgentInput returns a detached normalized input value.
func CloneAgentInput(in AgentInput) AgentInput {
	out := in
	out.Target = strings.TrimSpace(in.Target)
	out.Input = strings.TrimSpace(in.Input)
	out.DisplayInput = strings.TrimSpace(in.DisplayInput)
	out.ContentParts = append([]model.ContentPart(nil), in.ContentParts...)
	return out
}
