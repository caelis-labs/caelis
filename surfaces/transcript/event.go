package transcript

import (
	"encoding/json"
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
	EventNarrative       EventKind = "narrative"
	EventNotice          EventKind = "notice"
	EventPlan            EventKind = "plan"
	EventTool            EventKind = "tool"
	EventApprovalRequest EventKind = "approval_request"
	EventApproval        EventKind = "approval"
	EventParticipant     EventKind = "participant"
	EventLifecycle       EventKind = "lifecycle"
	EventUsage           EventKind = "usage"
	EventError           EventKind = "error"
	EventRawExtension    EventKind = "raw_extension"
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
	Content  string
	Status   string
	Priority string
}

type ApprovalOption struct {
	ID   string
	Name string
	Kind string
}

type Event struct {
	Kind    EventKind
	Scope   Scope
	ScopeID string
	// SourceEventID and SourceProjectionID preserve the stable identities of
	// the ACP Envelope that produced this Surface event. They are reducer
	// inputs only: Surfaces must not persist them as a second transcript.
	SourceEventID      string
	SourceProjectionID string
	RunID              string
	TurnID             string
	Actor              string
	ParticipantID      string
	OccurredAt         time.Time
	Meta               map[string]any

	NarrativeKind NarrativeKind
	NoticeKind    NoticeKind
	MessageID     string
	Text          string
	Final         bool
	Citations     []Citation

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
	// ToolOutputCursor is the cumulative terminal-output byte position after
	// ToolOutput. ToolOutputStartCursor is present for durable observation
	// snapshots whose display text may be compacted rather than byte-exact.
	ToolOutputCursor           int64
	ToolOutputCursorKnown      bool
	ToolOutputStartCursor      int64
	ToolOutputStartCursorKnown bool
	// ToolOutputGapBefore is a render-only notice that exact terminal bytes
	// before this event are unavailable. It is never part of ToolOutput.
	ToolOutputGapBefore bool
	// ToolTaskHandle is the Session-scoped public identity shown to users. It
	// must never carry the opaque TaskID used by Task stream endpoints.
	ToolTaskHandle     string
	ToolTaskAction     string
	ToolTaskInput      string
	ToolTaskTargetKind string

	PlanEntries []PlanEntry

	ApprovalTool      string
	ApprovalRequestID string
	ApprovalOptions   []ApprovalOption
	ApprovalCommand   string
	ApprovalStatus    string
	ApprovalRisk      string
	ApprovalAuth      string
	ApprovalText      string

	State      string
	Reason     string
	StopReason string

	Usage *eventstream.UsageSnapshot

	RawSessionUpdate string
	RawUpdate        json.RawMessage

	// Parent-tool fields flatten the Envelope relationship. They do not make
	// transcript state authoritative.
	AnchorToolCallID string
	AnchorToolName   string
}
