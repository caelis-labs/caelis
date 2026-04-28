package runtime

import (
	"context"
	"errors"
	"time"

	appgateway "github.com/OnslaughtSnail/caelis/gateway"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

var ErrMigrationPending = errors.New("tui/runtime: legacy tui migration wiring pending")

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
	SessionID               string
	Workspace               string
	StoreDir                string
	Model                   string
	ReasoningEffort         string
	Provider                string
	ModelName               string
	ModeLabel               string
	SessionMode             string
	SandboxType             string
	SandboxRequestedBackend string
	SandboxResolvedBackend  string
	Route                   string
	FallbackReason          string
	SecuritySummary         string
	MissingAPIKey           bool
	HostExecution           bool
	FullAccessMode          bool
	Surface                 string
	PromptTokens            int
	CompletionTokens        int
	TotalTokens             int
	ContextWindowTokens     int
	Running                 bool
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
	SessionID       string
	ControllerKind  string
	ControllerLabel string
	ControllerEpoch string
	HasActiveTurn   bool
	AvailableAgents []AgentCandidate
	Participants    []AgentParticipantSnapshot
}

type SubagentSnapshot struct {
	Handle        string
	Mention       string
	Agent         string
	TaskID        string
	TurnID        string
	State         string
	Running       bool
	OutputPreview string
	Result        string
	StdoutCursor  int64
	StderrCursor  int64
	EventCursor   int64
}

type SubagentStreamFrame struct {
	TaskID    string
	TurnID    string
	Stream    string
	Text      string
	State     string
	Running   bool
	Closed    bool
	Event     *appgateway.EventEnvelope
	UpdatedAt time.Time
}

type ConnectConfig struct {
	Provider            string
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
	Events() <-chan appgateway.EventEnvelope
	Submit(context.Context, appgateway.SubmitRequest) error
	Cancel() bool
	Close() error
}

// Driver is the only backend boundary that the transplanted legacy-style TUI
// shell should depend on.
type Driver interface {
	Status(context.Context) (StatusSnapshot, error)
	WorkspaceDir() string

	Submit(context.Context, Submission) (Turn, error)
	Interrupt(context.Context) error

	NewSession(context.Context) (sdksession.Session, error)
	ResumeSession(context.Context, string) (sdksession.Session, error)
	ListSessions(context.Context, int) ([]ResumeCandidate, error)
	ReplayEvents(context.Context) ([]appgateway.EventEnvelope, error)
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
	RemoveAgent(context.Context, string) (AgentStatusSnapshot, error)
	HandoffAgent(context.Context, string) (AgentStatusSnapshot, error)
	AskAgent(context.Context, string, string) (AgentStatusSnapshot, error)
	StartAgentSubagent(context.Context, string, string) (SubagentSnapshot, error)
	ContinueSubagent(context.Context, string, string) (SubagentSnapshot, error)

	CompleteMention(context.Context, string, int) ([]CompletionCandidate, error)
	CompleteFile(context.Context, string, int) ([]CompletionCandidate, error)
	CompleteSkill(context.Context, string, int) ([]CompletionCandidate, error)
	CompleteResume(context.Context, string, int) ([]ResumeCandidate, error)
	CompleteSlashArg(context.Context, string, string, int) ([]SlashArgCandidate, error)
}
