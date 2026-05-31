package tuidriver

import (
	"context"
	"time"

	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	"github.com/OnslaughtSnail/caelis/core/sandbox"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
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
	SessionID                       string
	Workspace                       string
	StoreDir                        string
	Model                           string
	ReasoningEffort                 string
	Provider                        string
	ModelName                       string
	ModeLabel                       string
	SessionMode                     string
	SandboxType                     string
	SandboxRequestedBackend         string
	SandboxResolvedBackend          string
	Route                           string
	FallbackReason                  string
	SandboxInstallHint              string
	SandboxSetup                    sandbox.SetupStatus
	SandboxSetupRequired            bool
	SandboxSetupError               string
	SandboxSetupMarkerCurrent       bool
	SandboxSetupMarkerReason        string
	SandboxGlobalSetupCurrent       bool
	SandboxGlobalSetupRequired      bool
	SandboxGlobalSetupReason        string
	SandboxWorkspaceSetupCurrent    bool
	SandboxWorkspaceSetupRequired   bool
	SandboxWorkspaceSetupReason     string
	SandboxWorkspaceSetupRoot       string
	SandboxWorkspaceSetupWriteRoots int
	SandboxWorkspaceSetupPolicyHash string
	SandboxWorkspaceSetupUpdatedAt  time.Time
	SecuritySummary                 string
	MissingAPIKey                   bool
	HostExecution                   bool
	FullAccessMode                  bool
	PermissionGrantCount            int
	PermissionReadRootCount         int
	PermissionWriteRootCount        int
	Surface                         string
	PromptTokens                    int
	CompletionTokens                int
	TotalTokens                     int
	ContextWindowTokens             int
	SessionUsageTotal               appviewmodel.TokenUsage
	SessionUsageMain                appviewmodel.TokenUsage
	SessionUsageSubagents           appviewmodel.TokenUsage
	SessionUsageAutoReview          appviewmodel.TokenUsage
	SessionUsageCompaction          appviewmodel.TokenUsage
	SessionInputTokens              int
	SessionCachedInputTokens        int
	SessionOutputTokens             int
	SessionReasoningTokens          int
	SessionTotalTokens              int
	ActiveJobs                      int
	ActiveTurnKind                  string
	Running                         bool
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

type CommandExecutionOptions struct {
	Input       string
	Attachments []Attachment
}

type CommandCatalogView = appviewmodel.CommandCatalogView
type CommandView = appviewmodel.CommandView
type CommandExecutionView = appviewmodel.CommandExecutionView

type Turn interface {
	HandleID() string
	RunID() string
	TurnID() string
	SessionRef() session.SessionRef
	Events() <-chan kernel.EventEnvelope
	Submit(context.Context, coreruntime.Submission) error
	Cancel() kernel.CancelResult
	Close() error
}

// Driver is the backend contract consumed by the Bubble Tea application.
type Driver interface {
	Status(context.Context) (StatusSnapshot, error)
	WorkspaceDir() string

	Submit(context.Context, Submission) (Turn, error)
	Interrupt(context.Context) error
	ExecuteCommand(context.Context, CommandExecutionOptions) (CommandExecutionView, error)

	NewSession(context.Context) (session.Session, error)
	ResumeSession(context.Context, string) (session.Session, error)
	ListSessions(context.Context, int) ([]ResumeCandidate, error)
	ReplayEvents(context.Context) ([]kernel.EventEnvelope, error)

	ListAgents(context.Context, int) ([]AgentCandidate, error)
	AgentStatus(context.Context) (AgentStatusSnapshot, error)
	ContinueSubagent(context.Context, string, string, []Attachment) (Turn, error)

	CompleteMention(context.Context, string, int) ([]CompletionCandidate, error)
	CompleteFile(context.Context, string, int) ([]CompletionCandidate, error)
	CompleteSkill(context.Context, string, int) ([]CompletionCandidate, error)
	CompleteResume(context.Context, string, int) ([]ResumeCandidate, error)
	CompleteSlashArg(context.Context, string, string, int) ([]SlashArgCandidate, error)
}
