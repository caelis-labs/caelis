package control

import (
	"context"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

type Turn interface {
	HandleID() string
	RunID() string
	TurnID() string
	// Events returns the single-consumer live ACP-compatible stream for this
	// turn. Implementations must emit exactly one terminal lifecycle envelope
	// before closing the channel so clients can drive running state, timers, and
	// final UI barriers from the stream itself.
	Events() <-chan eventstream.Envelope
	SubmitApproval(context.Context, ApprovalDecision) error
	Cancel()
	Close() error
}

type StatusService interface {
	Status(context.Context) (StatusSnapshot, error)
	WorkspaceDir() string
}

type TurnService interface {
	Submit(context.Context, Submission) (Turn, error)
	Interrupt(context.Context) error
}

type SessionService interface {
	NewSession(context.Context) (SessionSnapshot, error)
	ResumeSession(context.Context, string) (SessionSnapshot, error)
	ListSessions(context.Context, int) ([]ResumeCandidate, error)
	ReplayEvents(context.Context) ([]eventstream.Envelope, error)
	Compact(context.Context) error
}

// ClientProtocolService is the stable GUI/API protocol surface. It returns
// eventstream envelopes and standard ACP schema payloads without exposing
// transitional gateway envelopes or TUI transcript view models.
type ClientProtocolService interface {
	ListSessionSnapshots(context.Context, schema.SessionListRequest) (schema.SessionListResponse, error)
	Replay(context.Context, eventstream.ReplayRequest) (eventstream.ReplayResult, error)
	RunState(context.Context) (eventstream.RunState, error)
}

type SessionModeService interface {
	CycleSessionMode(context.Context) (StatusSnapshot, error)
	SetSessionMode(context.Context, string) (StatusSnapshot, error)
}

type ModelService interface {
	Connect(context.Context, ConnectConfig) (StatusSnapshot, error)
	UseModel(context.Context, string, ...string) (StatusSnapshot, error)
	DeleteModel(context.Context, string) error
}

type SandboxService interface {
	SetSandboxBackend(context.Context, string) (StatusSnapshot, error)
	PrepareSandbox(context.Context) (StatusSnapshot, error)
	RepairSandbox(context.Context) (StatusSnapshot, error)
}

type AgentService interface {
	ListAgents(context.Context, int) ([]AgentCandidate, error)
	AgentStatus(context.Context) (AgentStatusSnapshot, error)
	AddAgent(context.Context, string) (AgentStatusSnapshot, error)
	AddAgentWithOptions(context.Context, string, AgentAddOptions) (AgentStatusSnapshot, error)
	RemoveAgent(context.Context, string) (AgentStatusSnapshot, error)
	HandoffAgent(context.Context, string) (AgentStatusSnapshot, error)
	StartAgentSubagent(context.Context, string, string, []Attachment) (Turn, error)
	ContinueSubagent(context.Context, string, string, []Attachment) (Turn, error)
}

type AgentProfileService interface {
	AgentProfileStatus(context.Context) (AgentProfileStatusSnapshot, error)
	BindAgentProfile(context.Context, AgentProfileBindingConfig) (AgentProfileStatusSnapshot, error)
	StartReviewSubagent(context.Context, string, []Attachment) (Turn, error)
}

type CompletionService interface {
	CompleteMention(context.Context, string, int) ([]CompletionCandidate, error)
	CompleteFile(context.Context, string, int) ([]CompletionCandidate, error)
	CompleteSkill(context.Context, string, int) ([]CompletionCandidate, error)
	CompleteResume(context.Context, string, int) ([]ResumeCandidate, error)
	CompleteSlashArg(context.Context, string, string, int) ([]SlashArgCandidate, error)
}

type PluginService interface {
	ListPlugins(context.Context) ([]PluginSnapshot, error)
	AddMarketplace(context.Context, string) (MarketplaceSnapshot, error)
	ListMarketplaces(context.Context) ([]MarketplaceSnapshot, error)
	UpdateMarketplace(context.Context, string) (MarketplaceSnapshot, error)
	RemoveMarketplace(context.Context, string) error
	AddPluginPath(context.Context, string) (PluginSnapshot, error)
	InstallPlugin(context.Context, string) (PluginSnapshot, error)
	EnablePlugin(context.Context, string) (PluginSnapshot, error)
	DisablePlugin(context.Context, string) (PluginSnapshot, error)
	RemovePlugin(context.Context, string) error
	InspectPlugin(context.Context, string) (PluginSnapshot, error)
}

type Service interface {
	StatusService
	TurnService
	SessionService
	ClientProtocolService
	SessionModeService
	ModelService
	SandboxService
	AgentService
	AgentProfileService
	CompletionService
	PluginService
}

type StreamSubscriber interface {
	SubscribeStream(context.Context, eventstream.Envelope) (<-chan eventstream.Envelope, bool)
}

type LightweightStatusProvider interface {
	LightweightStatus(context.Context) (StatusSnapshot, error)
}
