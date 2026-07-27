package controlprompt

import (
	"context"

	controlclient "github.com/caelis-labs/caelis/control/client"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
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

// SessionReconnect is the transitional in-process view of one Control-owned
// reconnect transaction. Backfill is transcript-only; Events is the already
// spliced live continuation. Closing it never cancels the Runtime Turn.
type SessionReconnect interface {
	Turn
	State() controlclient.SessionState
	Backfill() <-chan eventstream.Envelope
	BackfillDone() <-chan struct{}
	BootstrapEvents() []eventstream.Envelope
	Err() error
}

type StatusService interface {
	Status(context.Context) (controlstatus.StatusSnapshot, error)
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
	Compact(context.Context) error
}

type SessionModeService interface {
	CycleSessionMode(context.Context) (controlstatus.StatusSnapshot, error)
	SetSessionMode(context.Context, string) (controlstatus.StatusSnapshot, error)
}

type ModelService interface {
	Connect(context.Context, ConnectConfig) (controlstatus.StatusSnapshot, error)
	UseModel(context.Context, string, ...string) (controlstatus.StatusSnapshot, error)
	DeleteModel(context.Context, string) error
}

type SandboxService interface {
	SetSandboxBackend(context.Context, string) (controlstatus.StatusSnapshot, error)
	PrepareSandbox(context.Context) (controlstatus.StatusSnapshot, error)
	RepairSandbox(context.Context) (controlstatus.StatusSnapshot, error)
}

type AgentService interface {
	ListAgents(context.Context, int) ([]AgentCandidate, error)
	AgentStatus(context.Context) (AgentStatusSnapshot, error)
	HandoffAgent(context.Context, string) (AgentStatusSnapshot, error)
	StartAgentRun(context.Context, string, string, []Attachment) (Turn, error)
	ContinueAgentRun(context.Context, string, string, []Attachment) (Turn, error)
}

type ReviewService interface {
	StartReview(context.Context, string, []Attachment) (Turn, error)
}

type CompletionService interface {
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
	SessionModeService
	ModelService
	SandboxService
	AgentService
	ReviewService
	CompletionService
	PluginService
}

type LightweightStatusProvider interface {
	LightweightStatus(context.Context) (controlstatus.StatusSnapshot, error)
}
