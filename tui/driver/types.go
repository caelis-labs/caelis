package tuidriver

import (
	"context"
	"time"

	"github.com/OnslaughtSnail/caelis/gateway"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
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
	SessionID                string
	Workspace                string
	StoreDir                 string
	Model                    string
	ReasoningEffort          string
	Provider                 string
	ModelName                string
	ModeLabel                string
	SessionMode              string
	SandboxType              string
	SandboxRequestedBackend  string
	SandboxResolvedBackend   string
	Route                    string
	FallbackReason           string
	SecuritySummary          string
	MissingAPIKey            bool
	HostExecution            bool
	FullAccessMode           bool
	PermissionGrantCount     int
	PermissionGrantNetwork   bool
	PermissionReadRootCount  int
	PermissionWriteRootCount int
	Surface                  string
	PromptTokens             int
	CompletionTokens         int
	TotalTokens              int
	ContextWindowTokens      int
	SessionInputTokens       int
	SessionCachedInputTokens int
	SessionOutputTokens      int
	SessionTotalTokens       int
	Running                  bool
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
	SessionRef() sdksession.SessionRef
	Events() <-chan gateway.EventEnvelope
	Submit(context.Context, gateway.SubmitRequest) error
	Cancel() bool
	Close() error
}

// Driver is the backend contract consumed by the Bubble Tea application.
type Driver interface {
	Status(context.Context) (StatusSnapshot, error)
	WorkspaceDir() string

	Submit(context.Context, Submission) (Turn, error)
	Interrupt(context.Context) error

	NewSession(context.Context) (sdksession.Session, error)
	ResumeSession(context.Context, string) (sdksession.Session, error)
	ListSessions(context.Context, int) ([]ResumeCandidate, error)
	ReplayEvents(context.Context) ([]gateway.EventEnvelope, error)
	Compact(context.Context) error

	Connect(context.Context, ConnectConfig) (StatusSnapshot, error)
	UseModel(context.Context, string, ...string) (StatusSnapshot, error)
	DeleteModel(context.Context, string) error
	CycleSessionMode(context.Context) (StatusSnapshot, error)
	SetSandboxBackend(context.Context, string) (StatusSnapshot, error)
	SetSandboxMode(context.Context, string) (StatusSnapshot, error)
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
