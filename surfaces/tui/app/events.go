package tuiapp

// events.go keeps the TUI-internal message types in the app package. These were
// previously in internal/cli/tuievents/messages.go.

import (
	"time"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/surfaces/internal/transcript"
)

type HintPriority int

const (
	HintPriorityUnspecified HintPriority = iota
	HintPriorityLow
	HintPriorityNormal
	HintPriorityHigh
	HintPriorityCritical
)

type LogChunkMsg struct {
	Chunk string
}

type SlashCommandResultMsg struct {
	Result controlprompt.SlashCommandResult
}

// SlashNoticeMsg carries non-tabular Slash command output. Placement chooses
// hint chrome versus transcript feedback or requested content.
type SlashNoticeMsg struct {
	Text      string
	Placement SlashNoticePlacement
}

type SetStatusMsg struct {
	Workspace           string
	Model               string
	Context             string
	TotalTokens         int
	ContextWindowTokens int
	ModeLabel           string
	Status              StatusViewModel
}

type StatusRefreshResultMsg struct {
	Workspace           string
	HasWorkspace        bool
	Model               string
	Context             string
	HasStatus           bool
	HasContext          bool
	TotalTokens         int
	ContextWindowTokens int
	HasUsage            bool
	ModeLabel           string
	HasModeLabel        bool
	Status              StatusViewModel
	HasView             bool
}

type SetCommandsMsg struct {
	Commands []string
	Details  map[string]string
}

type SetHintMsg struct {
	Hint           string
	ClearAfter     time.Duration
	Priority       HintPriority
	ClearOnMessage bool
}

// UpdateCheckResultMsg carries a completed background update check from the CLI host.
type UpdateCheckResultMsg struct {
	LatestVersion string
	Eligible      bool
}

type TaskResultMsg struct {
	ExitNow             bool
	Err                 error
	Interrupted         bool
	ContinueRunning     bool
	SuppressTurnDivider bool
}

type RunningInterruptResultMsg struct {
	Accepted bool
}

type SandboxProgressMsg struct {
	Title   string
	Source  string
	Phase   string
	Message string
	Step    int
	Total   int
	Done    bool
	Clear   bool
}

type PromptRequestMsg struct {
	Title               string
	Prompt              string
	Details             []PromptDetail
	Secret              bool
	Choices             []PromptChoice
	DefaultChoice       string
	SelectedChoices     []string
	Filterable          bool
	MultiSelect         bool
	AllowEmptySelection bool
	AllowFreeformInput  bool
	Response            chan PromptResponse
}

type PromptResponse struct {
	Line string
	Err  error
}

type PromptChoice struct {
	Label         string
	Value         string
	Detail        string
	AlwaysVisible bool
}

type PromptDetail struct {
	Label    string
	Value    string
	Emphasis bool
}

const (
	PromptErrInterrupt = "prompt_interrupted"
	PromptErrEOF       = "prompt_eof"
)

type MentionCandidatesMsg struct {
	Query      string
	Candidates []string
	Latency    time.Duration
}

type TickStatusMsg struct{}

type AttachmentCountMsg struct {
	Count int
}

type ClearHistoryMsg struct{}

// SessionReconnectMsg atomically replaces transcript/interaction state and
// installs the Control-owned running snapshot for a resumed Session.
type SessionReconnectMsg struct {
	State appserver.SessionState
}

type UserMessageMsg struct {
	Text string
}

type ParticipantStatusMsg struct {
	SessionID       string
	Actor           string
	State           string
	ApprovalTool    string
	ApprovalCommand string
	OccurredAt      time.Time
}

// Transitional aliases keep existing TUI call sites stable while shared
// transcript semantics move to surfaces/internal/transcript. New cross-surface code
// should import surfaces/internal/transcript directly.
type ACPProjectionScope = transcript.Scope

const (
	ACPProjectionMain        = transcript.ScopeMain
	ACPProjectionParticipant = transcript.ScopeParticipant
	ACPProjectionSubagent    = transcript.ScopeSubagent
)

// TranscriptEventsMsg is one normalized Surface projection batch. Task owner
// repairs are decoded alongside transcript events so live delivery and
// reconnect never build independent correlation paths from the same Envelope.
type TranscriptEventsMsg struct {
	Events       []TranscriptEvent
	OwnerRepairs acpprojector.TaskOwnerRepairs
	// ReconnectReplay marks history delivered by the reconnect backfill path.
	// It affects only first-paint layout reconstruction, never transcript data.
	ReconnectReplay bool
}

type PlanEntry = transcript.PlanEntry

type PlanUpdateMsg struct {
	Entries []PlanEntry
}

type BTWOverlayMsg struct {
	Text  string
	Final bool
}

type BTWErrorMsg struct {
	Text string
}
