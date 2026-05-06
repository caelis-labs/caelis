package tuiapp

// events.go transplants the legacy tuievents message types into the tuiapp
// package. These were previously in internal/cli/tuievents/messages.go.

import "time"

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

type SetStatusMsg struct {
	Workspace string
	Model     string
	Context   string
	ModeLabel string
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

type ApprovalReviewHintMsg struct {
	Text    string
	Pending bool
}

type SetRunningMsg struct {
	Running bool
}

type TaskResultMsg struct {
	ExitNow             bool
	Err                 error
	Interrupted         bool
	ContinueRunning     bool
	SuppressTurnDivider bool
}

type PromptRequestMsg struct {
	Title              string
	Prompt             string
	Details            []PromptDetail
	Secret             bool
	Choices            []PromptChoice
	DefaultChoice      string
	SelectedChoices    []string
	Filterable         bool
	MultiSelect        bool
	AllowFreeformInput bool
	Response           chan PromptResponse
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

type ACPProjectionScope string

const (
	ACPProjectionMain        ACPProjectionScope = "main"
	ACPProjectionParticipant ACPProjectionScope = "participant"
	ACPProjectionSubagent    ACPProjectionScope = "subagent"
)

type TranscriptEventsMsg struct {
	Events []TranscriptEvent
}

type PlanEntry struct {
	Content string
	Status  string
}

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

type SubagentStartMsg struct {
	SpawnID      string
	AttachTarget string
	Agent        string
	CallID       string
	AnchorTool   string
	ClaimAnchor  bool
	Provisional  bool
	OccurredAt   time.Time
}

type SubagentStatusMsg struct {
	SpawnID         string
	State           string
	ApprovalTool    string
	ApprovalCommand string
	OccurredAt      time.Time
}

type SubagentDoneMsg struct {
	SpawnID    string
	State      string
	OccurredAt time.Time
}
