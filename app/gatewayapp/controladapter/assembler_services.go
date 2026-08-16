package controladapter

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

// StatusAssembler is the narrow server-side status projection used by the
// local AppServer implementation.
type StatusAssembler interface {
	Status(context.Context) (controlstatus.StatusSnapshot, error)
	LightweightStatus(context.Context) (controlstatus.StatusSnapshot, error)
}

// AgentAssembler is the narrow server-side Agent capability used by the local
// AppServer implementation.
type AgentAssembler interface {
	ListAgents(context.Context, int) ([]controlprompt.AgentCandidate, error)
	AgentStatus(context.Context) (controlprompt.AgentStatusSnapshot, error)
	DisconnectCandidates(context.Context) ([]controlagents.DisconnectCandidate, error)
}

// CompletionAssembler is the narrow server-side completion capability used by
// the local AppServer implementation.
type CompletionAssembler interface {
	CompleteFile(context.Context, string, int) ([]controlprompt.CompletionCandidate, error)
	CompleteSkill(context.Context, string, int) ([]controlprompt.CompletionCandidate, error)
	CompleteResume(context.Context, string, int) ([]controlprompt.ResumeCandidate, error)
	CompleteSlashArg(context.Context, string, string, int) ([]controlprompt.SlashArgCandidate, error)
	ResolveSkill(context.Context, string) (controlprompt.SkillResolveResult, error)
}

// PluginAssembler is the narrow server-side plugin read capability used by the
// local AppServer implementation. Host plugin mutations use the shared Control
// command path instead of assembler hooks.
type PluginAssembler interface {
	ListPlugins(context.Context) ([]controlprompt.PluginSnapshot, error)
	ListMarketplaces(context.Context) ([]controlprompt.MarketplaceSnapshot, error)
	InspectPlugin(context.Context, string) (controlprompt.PluginSnapshot, error)
}

// NewStatusAssemblerForSession binds the status assembler to an already
// authorized Session. It is server composition, not a presentation client.
func NewStatusAssemblerForSession(ctx context.Context, stack *RuntimeStack, active session.Session, bindingKey, modelText string) (StatusAssembler, error) {
	return newAssemblerForSession(ctx, stack, active, bindingKey, modelText)
}

// NewStatusAssemblerForStack constructs the Host-scoped status projection.
// Session-specific fields remain empty until the caller explicitly selects a
// Session and uses NewStatusAssemblerForSession.
func NewStatusAssemblerForStack(stack *RuntimeStack, bindingKey, modelText string) StatusAssembler {
	return newAssemblerForStack(stack, bindingKey, modelText)
}

// NewAgentAssemblerForSession binds the Agent assembler to an already
// authorized Session.
func NewAgentAssemblerForSession(ctx context.Context, stack *RuntimeStack, active session.Session, bindingKey, modelText string) (AgentAssembler, error) {
	return newAssemblerForSession(ctx, stack, active, bindingKey, modelText)
}

// NewAgentAssemblerForStack constructs the Host Agent catalog and disconnect
// projection. Product onboarding mutations use the focused Agent command
// client. Controller and participant state require the Session variant.
func NewAgentAssemblerForStack(stack *RuntimeStack, bindingKey, modelText string) AgentAssembler {
	return newAssemblerForStack(stack, bindingKey, modelText)
}

// NewCompletionAssemblerForSession binds the completion assembler to an
// already authorized Session.
func NewCompletionAssemblerForSession(ctx context.Context, stack *RuntimeStack, active session.Session, bindingKey, modelText string) (CompletionAssembler, error) {
	return newAssemblerForSession(ctx, stack, active, bindingKey, modelText)
}

// NewCompletionAssemblerForStack constructs workspace/catalog completion
// without creating or activating a Session.
func NewCompletionAssemblerForStack(stack *RuntimeStack, bindingKey, modelText string) CompletionAssembler {
	return newAssemblerForStack(stack, bindingKey, modelText)
}

// NewPluginAssemblerForStack constructs Host-owned plugin and marketplace
// configuration without a Session address.
func NewPluginAssemblerForStack(stack *RuntimeStack, bindingKey, modelText string) PluginAssembler {
	return newAssemblerForStack(stack, bindingKey, modelText)
}
