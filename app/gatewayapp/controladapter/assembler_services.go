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

// ConfigurationAssembler is the narrow server-side configuration capability
// used by the local AppServer implementation.
type ConfigurationAssembler interface {
	CycleSessionMode(context.Context) (controlstatus.StatusSnapshot, error)
	SetSessionMode(context.Context, string) (controlstatus.StatusSnapshot, error)
	Connect(context.Context, controlprompt.ConnectConfig) (controlstatus.StatusSnapshot, error)
	UseModel(context.Context, string, ...string) (controlstatus.StatusSnapshot, error)
	DeleteModel(context.Context, string) error
	SetSandboxBackend(context.Context, string) (controlstatus.StatusSnapshot, error)
	PrepareSandbox(context.Context) (controlstatus.StatusSnapshot, error)
	RepairSandbox(context.Context) (controlstatus.StatusSnapshot, error)
}

// AgentAssembler is the narrow server-side Agent capability used by the local
// AppServer implementation.
type AgentAssembler interface {
	ListAgents(context.Context, int) ([]controlprompt.AgentCandidate, error)
	AgentStatus(context.Context) (controlprompt.AgentStatusSnapshot, error)
	DiscoverACPConnection(context.Context, controlagents.ConnectRequest) (controlagents.DiscoverySnapshot, error)
	ConnectACP(context.Context, controlagents.ConnectRequest) (controlagents.ConnectResult, error)
	DisconnectCandidates(context.Context) ([]controlagents.DisconnectCandidate, error)
	DisconnectACP(context.Context, string) (controlagents.DisconnectResult, error)
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

// PluginAssembler is the narrow server-side plugin capability used by the
// local AppServer implementation.
type PluginAssembler interface {
	ListPlugins(context.Context) ([]controlprompt.PluginSnapshot, error)
	AddMarketplace(context.Context, string) (controlprompt.MarketplaceSnapshot, error)
	ListMarketplaces(context.Context) ([]controlprompt.MarketplaceSnapshot, error)
	UpdateMarketplace(context.Context, string) (controlprompt.MarketplaceSnapshot, error)
	RemoveMarketplace(context.Context, string) error
	AddPluginPath(context.Context, string) (controlprompt.PluginSnapshot, error)
	InstallPlugin(context.Context, string) (controlprompt.PluginSnapshot, error)
	EnablePlugin(context.Context, string) (controlprompt.PluginSnapshot, error)
	DisablePlugin(context.Context, string) (controlprompt.PluginSnapshot, error)
	RemovePlugin(context.Context, string) error
	InspectPlugin(context.Context, string) (controlprompt.PluginSnapshot, error)
}

// NewStatusAssemblerForSession binds the status assembler to an already
// authorized Session. It is server composition, not a presentation client.
func NewStatusAssemblerForSession(ctx context.Context, stack *RuntimeStack, active session.Session, bindingKey, modelText string) (StatusAssembler, error) {
	return newAssemblerForSession(ctx, stack, active, bindingKey, modelText)
}

// NewConfigurationAssemblerForSession binds the configuration assembler to an
// already authorized Session.
func NewConfigurationAssemblerForSession(ctx context.Context, stack *RuntimeStack, active session.Session, bindingKey, modelText string) (ConfigurationAssembler, error) {
	return newAssemblerForSession(ctx, stack, active, bindingKey, modelText)
}

// NewAgentAssemblerForSession binds the Agent assembler to an already
// authorized Session.
func NewAgentAssemblerForSession(ctx context.Context, stack *RuntimeStack, active session.Session, bindingKey, modelText string) (AgentAssembler, error) {
	return newAssemblerForSession(ctx, stack, active, bindingKey, modelText)
}

// NewCompletionAssemblerForSession binds the completion assembler to an
// already authorized Session.
func NewCompletionAssemblerForSession(ctx context.Context, stack *RuntimeStack, active session.Session, bindingKey, modelText string) (CompletionAssembler, error) {
	return newAssemblerForSession(ctx, stack, active, bindingKey, modelText)
}

// NewPluginAssemblerForSession binds the plugin assembler to an already
// authorized Session.
func NewPluginAssemblerForSession(ctx context.Context, stack *RuntimeStack, active session.Session, bindingKey, modelText string) (PluginAssembler, error) {
	return newAssemblerForSession(ctx, stack, active, bindingKey, modelText)
}
