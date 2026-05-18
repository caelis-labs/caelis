package tuidriver

import (
	"context"
	"time"

	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

type SubmissionMode string

const (
	SubmissionModeDefault SubmissionMode = ""
	SubmissionModeOverlay SubmissionMode = "overlay"
)

type Attachment struct {
	Name   string
	Offset int
}

type Submission struct {
	Text        string
	DisplayText string
	Mode        SubmissionMode
	Attachments []Attachment
}

type StatusSnapshot struct {
	SessionID                 string
	Workspace                 string
	StoreDir                  string
	Model                     string
	ReasoningEffort           string
	Provider                  string
	ModelName                 string
	ModeLabel                 string
	SessionMode               string
	SandboxType               string
	SandboxRequestedBackend   string
	SandboxResolvedBackend    string
	Route                     string
	FallbackReason            string
	SandboxInstallHint        string
	SandboxSetupRequired      bool
	SandboxSetupError         string
	SandboxSetupMarkerCurrent bool
	SandboxSetupMarkerReason  string
	SecuritySummary           string
	MissingAPIKey             bool
	HostExecution             bool
	FullAccessMode            bool
	PermissionGrantCount      int
	PermissionGrantNetwork    bool
	PermissionReadRootCount   int
	PermissionWriteRootCount  int
	Surface                   string
	PromptTokens              int
	CompletionTokens          int
	TotalTokens               int
	ContextWindowTokens       int
	SessionUsageTotal         kernel.UsageSnapshot
	SessionUsageMain          kernel.UsageSnapshot
	SessionUsageSubagents     kernel.UsageSnapshot
	SessionUsageAutoReview    kernel.UsageSnapshot
	SessionInputTokens        int
	SessionCachedInputTokens  int
	SessionOutputTokens       int
	SessionReasoningTokens    int
	SessionTotalTokens        int
	ActiveJobs                int
	ActiveTurnKind            string
	Running                   bool
}

type ResumeCandidate struct {
	SessionID string
	Title     string
	Prompt    string
	Model     string
	Workspace string
	Age       string
	UpdatedAt time.Time
}

type CompletionCandidate struct {
	Value   string
	Display string
	Detail  string
	Path    string
}

type SlashArgCandidate struct {
	Value   string
	Display string
	Detail  string
	NoAuth  bool
}

type AgentCandidate struct {
	Name        string
	Description string
}

type AgentParticipantSnapshot struct {
	ID        string
	Label     string
	AgentName string
	Kind      string
	Role      string
	SessionID string
}

type AgentStatusSnapshot struct {
	SessionID                 string
	ControllerKind            string
	ControllerLabel           string
	ControllerEpoch           string
	ControllerModel           string
	ControllerReasoningEffort string
	ControllerCommands        []string
	ControllerModels          []SlashArgCandidate
	ControllerEfforts         []SlashArgCandidate
	HasActiveTurn             bool
	ActiveTurnKind            string
	AvailableAgents           []AgentCandidate
	Participants              []AgentParticipantSnapshot
	DelegatedParticipants     []AgentParticipantSnapshot
}

type CustomAgentConfig struct {
	Name        string
	Description string
	Command     string
	Args        []string
	Env         map[string]string
	WorkDir     string
}

type AgentAddOptions struct {
	Install bool
	Custom  *CustomAgentConfig
}

type ConnectConfig struct {
	Provider            string
	EndpointID          string
	Model               string
	BaseURL             string
	TimeoutSeconds      int
	APIKey              string
	TokenEnv            string
	AuthType            string
	ContextWindowTokens int
	MaxOutputTokens     int
	ReasoningEffort     string
	ReasoningLevels     []string
}

type Turn interface {
	HandleID() string
	RunID() string
	TurnID() string
	SessionRef() session.SessionRef
	Events() <-chan kernel.EventEnvelope
	Submit(context.Context, kernel.SubmitRequest) error
	Cancel() kernel.CancelResult
	Close() error
}

// Driver is the backend contract consumed by the Bubble Tea application.
type Driver interface {
	Status(context.Context) (StatusSnapshot, error)
	WorkspaceDir() string

	Submit(context.Context, Submission) (Turn, error)
	Interrupt(context.Context) error

	NewSession(context.Context) (session.Session, error)
	ResumeSession(context.Context, string) (session.Session, error)
	ListSessions(context.Context, int) ([]ResumeCandidate, error)
	ReplayEvents(context.Context) ([]kernel.EventEnvelope, error)
	Compact(context.Context) error

	Connect(context.Context, ConnectConfig) (StatusSnapshot, error)
	UseModel(context.Context, string, ...string) (StatusSnapshot, error)
	DeleteModel(context.Context, string) error
	CycleSessionMode(context.Context) (StatusSnapshot, error)
	SetSandboxBackend(context.Context, string) (StatusSnapshot, error)
	PrepareSandbox(context.Context) (StatusSnapshot, error)
	SetSessionMode(context.Context, string) (StatusSnapshot, error)
	ListAgents(context.Context, int) ([]AgentCandidate, error)
	AgentStatus(context.Context) (AgentStatusSnapshot, error)
	AddAgent(context.Context, string) (AgentStatusSnapshot, error)
	AddAgentWithOptions(context.Context, string, AgentAddOptions) (AgentStatusSnapshot, error)
	RemoveAgent(context.Context, string) (AgentStatusSnapshot, error)
	HandoffAgent(context.Context, string) (AgentStatusSnapshot, error)
	StartAgentSubagent(context.Context, string, string) (Turn, error)
	ContinueSubagent(context.Context, string, string) (Turn, error)

	CompleteMention(context.Context, string, int) ([]CompletionCandidate, error)
	CompleteFile(context.Context, string, int) ([]CompletionCandidate, error)
	CompleteSkill(context.Context, string, int) ([]CompletionCandidate, error)
	CompleteResume(context.Context, string, int) ([]ResumeCandidate, error)
	CompleteSlashArg(context.Context, string, string, int) ([]SlashArgCandidate, error)
}
