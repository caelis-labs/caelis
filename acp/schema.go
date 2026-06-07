package acp

// ─── ACP session/update wire types ───────────────────────────────────
// These types match the standard ACP wire format with camelCase JSON tags.

// UpdateKind identifies the ACP session/update type.
type UpdateKind string

const (
	MethodInitialize           = "initialize"
	MethodAuthenticate         = "authenticate"
	MethodSessionNew           = "session/new"
	MethodSessionList          = "session/list"
	MethodSessionLoad          = "session/load"
	MethodSessionResume        = "session/resume"
	MethodSessionClose         = "session/close"
	MethodSessionSetMode       = "session/set_mode"
	MethodSessionSetConfig     = "session/set_config_option"
	MethodSessionSetModel      = "session/set_model"
	MethodSessionPrompt        = "session/prompt"
	MethodSessionCancel        = "session/cancel"
	MethodSessionUpdate        = "session/update"
	MethodSessionReqPermission = "session/request_permission"
	MethodReadTextFile         = "fs/read_text_file"
	MethodWriteTextFile        = "fs/write_text_file"
	MethodTerminalCreate       = "terminal/create"
	MethodTerminalOutput       = "terminal/output"
	MethodTerminalWaitForExit  = "terminal/wait_for_exit"
	MethodTerminalKill         = "terminal/kill"
	MethodTerminalRelease      = "terminal/release"
)

const (
	StopReasonEndTurn   = "end_turn"
	StopReasonCancelled = "cancelled"
)

const (
	UpdateUserMessage   UpdateKind = "user_message_chunk"
	UpdateAgentMessage  UpdateKind = "agent_message_chunk"
	UpdateAgentThought  UpdateKind = "agent_thought_chunk"
	UpdateToolCall      UpdateKind = "tool_call"
	UpdateToolCallInfo  UpdateKind = "tool_call_update"
	UpdatePlan          UpdateKind = "plan"
	UpdateAvailableCmds UpdateKind = "available_commands_update"
	UpdateCurrentMode   UpdateKind = "current_mode_update"
	UpdateConfigOption  UpdateKind = "config_option_update"
	UpdateSessionInfo   UpdateKind = "session_info_update"
)

// SessionNotification wraps an ACP session/update notification.
type SessionNotification struct {
	SessionID string `json:"sessionId"`
	Update    Update `json:"update"`
}

// Update is the interface for all ACP update types.
type Update interface {
	SessionUpdateType() UpdateKind
}

// TextContent is a text content block.
type TextContent struct {
	Type string `json:"type"` // "text"
	Text string `json:"text"`
}

// ContentChunk is used for user_message_chunk, agent_message_chunk,
// and agent_thought_chunk updates.
type ContentChunk struct {
	SessionUpdate UpdateKind `json:"sessionUpdate"`
	Content       any        `json:"content"` // typically TextContent
}

func (c ContentChunk) SessionUpdateType() UpdateKind { return c.SessionUpdate }

// ToolCallUpdate is the ACP wire format for a tool call.
type ToolCallUpdate struct {
	SessionUpdate UpdateKind         `json:"sessionUpdate"`
	ToolCallID    string             `json:"toolCallId"`
	Title         string             `json:"title,omitempty"`
	Kind          string             `json:"kind,omitempty"`
	Status        string             `json:"status,omitempty"`
	RawInput      any                `json:"rawInput,omitempty"`
	RawOutput     any                `json:"rawOutput,omitempty"`
	Content       []ToolCallContent  `json:"content,omitempty"`
	Locations     []ToolCallLocation `json:"locations,omitempty"`
	Meta          map[string]any     `json:"_meta,omitempty"`
}

func (t ToolCallUpdate) SessionUpdateType() UpdateKind { return t.SessionUpdate }

// ToolCallContent describes content attached to a tool call.
type ToolCallContent struct {
	Type       string  `json:"type,omitempty"` // "text" | "terminal" | "content"
	Content    any     `json:"content,omitempty"`
	TerminalID string  `json:"terminalId,omitempty"`
	Path       string  `json:"path,omitempty"`
	OldText    *string `json:"oldText,omitempty"`
	NewText    string  `json:"newText,omitempty"`
}

// ToolCallLocation describes a file location relevant to a tool call.
type ToolCallLocation struct {
	Path string `json:"path"`
	Line *int   `json:"line,omitempty"`
}

// PlanUpdate is the ACP wire format for a plan update.
type PlanUpdate struct {
	SessionUpdate UpdateKind  `json:"sessionUpdate"`
	Entries       []PlanEntry `json:"entries"`
}

func (p PlanUpdate) SessionUpdateType() UpdateKind { return p.SessionUpdate }

// PlanEntry is a single plan step in ACP wire format.
type PlanEntry struct {
	Content  string `json:"content"`
	Status   string `json:"status"`
	Priority string `json:"priority,omitempty"`
}

// ─── Tool status normalization ───────────────────────────────────────

// NormalizeToolStatus maps internal status strings to ACP-standard status.
func NormalizeToolStatus(status string) string {
	switch status {
	case "", "pending":
		return "pending"
	case "running", "waiting_approval", "in_progress":
		return "in_progress"
	case "completed":
		return "completed"
	case "failed":
		return "failed"
	case "cancelled", "canceled", "interrupted", "terminated", "timed_out", "timeout":
		return "failed"
	default:
		return status
	}
}

// ─── Tool kind mapping ───────────────────────────────────────────────

// ToolKindForName maps tool names to ACP tool kind strings.
func ToolKindForName(name string) string {
	switch name {
	case "READ", "GLOB", "SEARCH":
		return "read"
	case "WRITE", "PATCH":
		return "edit"
	case "LIST":
		return "search"
	case "RUN_COMMAND":
		return "execute"
	case "PLAN":
		return "think"
	case "SPAWN":
		return "execute"
	case "TASK":
		return "execute"
	default:
		return "other"
	}
}
