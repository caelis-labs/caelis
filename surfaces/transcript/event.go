package transcript

import (
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

type Scope string

const (
	ScopeMain        Scope = "main"
	ScopeParticipant Scope = "participant"
	ScopeSubagent    Scope = "subagent"
)

type EventKind string

const (
	EventNarrative   EventKind = "narrative"
	EventNotice      EventKind = "notice"
	EventPlan        EventKind = "plan"
	EventTool        EventKind = "tool"
	EventApproval    EventKind = "approval"
	EventParticipant EventKind = "participant"
	EventLifecycle   EventKind = "lifecycle"
	EventUsage       EventKind = "usage"
)

type NarrativeKind string

const (
	NarrativeUser      NarrativeKind = "user"
	NarrativeAssistant NarrativeKind = "assistant"
	NarrativeReasoning NarrativeKind = "reasoning"
	NarrativeSystem    NarrativeKind = "system"
	NarrativeNotice    NarrativeKind = "notice"
)

const CompactNoticeLabel = display.CompactNoticeLabel

// NoticeKind identifies structured notices that need behavior beyond display.
type NoticeKind string

const (
	NoticeKindCompact    NoticeKind = "compact"
	NoticeKindModelRetry NoticeKind = "model_retry"
)

type PlanEntry struct {
	Content string
	Status  string
}

type Event struct {
	Kind       EventKind
	Scope      Scope
	ScopeID    string
	TurnID     string
	Actor      string
	OccurredAt time.Time
	Meta       map[string]any

	NarrativeKind NarrativeKind
	NoticeKind    NoticeKind
	Text          string
	Final         bool

	ToolCallID          string
	ToolName            string
	ToolKind            string
	ToolTitle           string
	ToolArgs            string
	ToolFullArgs        string
	ToolOutput          string
	ToolStream          string
	ToolStatus          string
	ToolError           bool
	ToolTerminal        bool
	ToolOutputSynthetic bool
	ToolOutputTerminal  bool
	ToolTaskID          string
	ToolTaskAction      string
	ToolTaskInput       string
	ToolTaskTargetKind  string

	PlanEntries []PlanEntry

	ApprovalTool    string
	ApprovalCommand string
	ApprovalStatus  string
	ApprovalRisk    string
	ApprovalAuth    string
	ApprovalText    string

	State string

	Usage *eventstream.UsageSnapshot

	AnchorToolCallID     string
	AnchorToolName       string
	MirroredToParentTool bool
}
