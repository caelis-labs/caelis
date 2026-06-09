package control

import (
	"context"

	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
)

type Turn interface {
	HandleID() string
	RunID() string
	TurnID() string
	Events() <-chan eventstream.Envelope
	SubmitApproval(context.Context, ApprovalDecision) error
	Cancel()
	Close() error
}

type Service interface {
	Status(context.Context) (StatusSnapshot, error)
	WorkspaceDir() string

	Submit(context.Context, Submission) (Turn, error)
	Interrupt(context.Context) error

	NewSession(context.Context) (SessionSnapshot, error)
	ResumeSession(context.Context, string) (SessionSnapshot, error)
	ListSessions(context.Context, int) ([]ResumeCandidate, error)
	ReplayEvents(context.Context) ([]eventstream.Envelope, error)
	Compact(context.Context) error

	Connect(context.Context, ConnectConfig) (StatusSnapshot, error)
	UseModel(context.Context, string, ...string) (StatusSnapshot, error)
	DeleteModel(context.Context, string) error
	CycleSessionMode(context.Context) (StatusSnapshot, error)
	SetSandboxBackend(context.Context, string) (StatusSnapshot, error)
	PrepareSandbox(context.Context) (StatusSnapshot, error)
	RepairSandbox(context.Context) (StatusSnapshot, error)
	SetSessionMode(context.Context, string) (StatusSnapshot, error)
	ListAgents(context.Context, int) ([]AgentCandidate, error)
	AgentStatus(context.Context) (AgentStatusSnapshot, error)
	AddAgent(context.Context, string) (AgentStatusSnapshot, error)
	AddAgentWithOptions(context.Context, string, AgentAddOptions) (AgentStatusSnapshot, error)
	RemoveAgent(context.Context, string) (AgentStatusSnapshot, error)
	HandoffAgent(context.Context, string) (AgentStatusSnapshot, error)
	StartAgentSubagent(context.Context, string, string, []Attachment) (Turn, error)
	ContinueSubagent(context.Context, string, string, []Attachment) (Turn, error)

	CompleteMention(context.Context, string, int) ([]CompletionCandidate, error)
	CompleteFile(context.Context, string, int) ([]CompletionCandidate, error)
	CompleteSkill(context.Context, string, int) ([]CompletionCandidate, error)
	CompleteResume(context.Context, string, int) ([]ResumeCandidate, error)
	CompleteSlashArg(context.Context, string, string, int) ([]SlashArgCandidate, error)

	ListPlugins(context.Context) ([]PluginSnapshot, error)
	AddPluginPath(context.Context, string) (PluginSnapshot, error)
	EnablePlugin(context.Context, string) (PluginSnapshot, error)
	DisablePlugin(context.Context, string) (PluginSnapshot, error)
	RemovePlugin(context.Context, string) error
	InspectPlugin(context.Context, string) (PluginSnapshot, error)
}

type StreamSubscriber interface {
	SubscribeStream(context.Context, eventstream.Envelope) (<-chan eventstream.Envelope, bool)
}

type LightweightStatusProvider interface {
	LightweightStatus(context.Context) (StatusSnapshot, error)
}
