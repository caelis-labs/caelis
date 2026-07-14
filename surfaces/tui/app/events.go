package tuiapp

// events.go keeps the TUI-internal message types in the app package. These were
// previously in internal/cli/tuievents/messages.go.

import (
	"time"

	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/surfaces/transcript"
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
	Result control.SlashCommandResult
}

type SetStatusMsg struct {
	Workspace string
	Model     string
	Context   string
	ModeLabel string
	Status    StatusViewModel
}

type StatusRefreshResultMsg struct {
	Workspace    string
	HasWorkspace bool
	Model        string
	Context      string
	HasStatus    bool
	HasContext   bool
	ModeLabel    string
	HasModeLabel bool
	Status       StatusViewModel
	HasView      bool
}

type SetCommandsMsg struct {
	Commands []string
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

type RunningActivityMsg struct {
	Kind   runningActivityKind
	Detail string
	Active bool
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

type UserMessageMsg struct {
	Text string
}

type ParticipantStatusMsg struct {
	SessionID       string
	State           string
	ApprovalTool    string
	ApprovalCommand string
	OccurredAt      time.Time
}

// Transitional aliases keep existing TUI call sites stable while shared
// transcript semantics move to surfaces/transcript. New cross-surface code
// should import surfaces/transcript directly.
type ACPProjectionScope = transcript.Scope

const (
	ACPProjectionMain        = transcript.ScopeMain
	ACPProjectionParticipant = transcript.ScopeParticipant
	ACPProjectionSubagent    = transcript.ScopeSubagent
)

type TranscriptEventsMsg struct {
	Events []TranscriptEvent
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
