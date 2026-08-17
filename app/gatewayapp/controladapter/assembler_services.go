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

// StatusAssemblyDeps contains only the read authorities used by status
// projection. It cannot assemble Agent, completion, or plugin capabilities.
type StatusAssemblyDeps struct {
	Gateway GatewayRuntimeDeps
	Session SessionRuntimeDeps
	Status  StatusRuntimeDeps
	Agent   AgentRuntimeDeps
	Model   ModelRuntimeDeps
	Sandbox SandboxRuntimeDeps
}

func (d StatusAssemblyDeps) runtimeDeps() *ControlRuntimeDeps {
	return &ControlRuntimeDeps{
		Gateway: d.Gateway, Session: d.Session, Status: d.Status,
		Agent: d.Agent, Model: d.Model, Sandbox: d.Sandbox,
	}
}

// AgentAssemblyDeps contains only Agent catalog and live Session projection
// authorities.
type AgentAssemblyDeps struct {
	Gateway GatewayRuntimeDeps
	Session SessionRuntimeDeps
	Agent   AgentRuntimeDeps
}

func (d AgentAssemblyDeps) runtimeDeps() *ControlRuntimeDeps {
	return &ControlRuntimeDeps{Gateway: d.Gateway, Session: d.Session, Agent: d.Agent}
}

// CompletionAssemblyDeps contains the catalog and principal-bound Session
// reads needed to complete AppServer input without mutation authority.
type CompletionAssemblyDeps struct {
	Session SessionRuntimeDeps
	Status  StatusRuntimeDeps
	Agent   AgentRuntimeDeps
	Model   ModelRuntimeDeps
	Skill   SkillRuntimeDeps
	Plugin  PluginRuntimeDeps
}

func (d CompletionAssemblyDeps) runtimeDeps() *ControlRuntimeDeps {
	return &ControlRuntimeDeps{
		Session: d.Session, Status: d.Status, Agent: d.Agent,
		Model: d.Model, Skill: d.Skill, Plugin: d.Plugin,
	}
}

// PluginAssemblyDeps contains only pure plugin and marketplace reads.
type PluginAssemblyDeps struct {
	Plugin PluginRuntimeDeps
}

func (d PluginAssemblyDeps) runtimeDeps() *ControlRuntimeDeps {
	return &ControlRuntimeDeps{Plugin: d.Plugin}
}

// NewStatusAssemblerForSession binds the status assembler to an already
// authorized Session. It is server composition, not a presentation client.
func NewStatusAssemblerForSession(ctx context.Context, deps StatusAssemblyDeps, active session.Session, bindingKey, modelText string) (StatusAssembler, error) {
	return newAssemblerForSession(ctx, deps.runtimeDeps(), active, bindingKey, modelText)
}

// NewStatusAssemblerForHost constructs the Host-scoped status projection.
// Session-specific fields remain empty until the caller explicitly selects a
// Session and uses NewStatusAssemblerForSession.
func NewStatusAssemblerForHost(deps StatusAssemblyDeps, bindingKey, modelText string) StatusAssembler {
	return newHostAssembler(deps.runtimeDeps(), bindingKey, modelText)
}

// NewAgentAssemblerForSession binds the Agent assembler to an already
// authorized Session.
func NewAgentAssemblerForSession(ctx context.Context, deps AgentAssemblyDeps, active session.Session, bindingKey, modelText string) (AgentAssembler, error) {
	return newAssemblerForSession(ctx, deps.runtimeDeps(), active, bindingKey, modelText)
}

// NewAgentAssemblerForHost constructs the Host Agent catalog and disconnect
// projection. Product onboarding mutations use the focused Agent command
// client. Controller and participant state require the Session variant.
func NewAgentAssemblerForHost(deps AgentAssemblyDeps, bindingKey, modelText string) AgentAssembler {
	return newHostAssembler(deps.runtimeDeps(), bindingKey, modelText)
}

// NewCompletionAssemblerForSession binds the completion assembler to an
// already authorized Session.
func NewCompletionAssemblerForSession(ctx context.Context, deps CompletionAssemblyDeps, active session.Session, bindingKey, modelText string) (CompletionAssembler, error) {
	return newAssemblerForSession(ctx, deps.runtimeDeps(), active, bindingKey, modelText)
}

// NewCompletionAssemblerForHost constructs workspace/catalog completion
// without creating or activating a Session.
func NewCompletionAssemblerForHost(deps CompletionAssemblyDeps, bindingKey, modelText string) CompletionAssembler {
	return newHostAssembler(deps.runtimeDeps(), bindingKey, modelText)
}

// NewPluginAssemblerForHost constructs Host-owned plugin and marketplace
// configuration without a Session address.
func NewPluginAssemblerForHost(deps PluginAssemblyDeps, bindingKey, modelText string) PluginAssembler {
	return newHostAssembler(deps.runtimeDeps(), bindingKey, modelText)
}
