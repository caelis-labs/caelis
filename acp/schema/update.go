package schema

const (
	MethodSessionUpdate        = "session/update"
	MethodSessionReqPermission = "session/request_permission"
)

const (
	UpdateUserMessage  = "user_message_chunk"
	UpdateAgentMessage = "agent_message_chunk"
	UpdateAgentThought = "agent_thought_chunk"
	UpdateToolCall     = "tool_call"
	UpdateToolCallInfo = "tool_call_update"
	UpdatePlan         = "plan"
)

const (
	ToolStatusPending    = "pending"
	ToolStatusInProgress = "in_progress"
	ToolStatusCompleted  = "completed"
	ToolStatusFailed     = "failed"
)

const (
	ToolKindRead    = "read"
	ToolKindEdit    = "edit"
	ToolKindDelete  = "delete"
	ToolKindMove    = "move"
	ToolKindSearch  = "search"
	ToolKindExecute = "execute"
	ToolKindThink   = "think"
	ToolKindFetch   = "fetch"
	ToolKindSwitch  = "switch_mode"
	ToolKindOther   = "other"
)

const (
	PermAllowOnce    = "allow_once"
	PermAllowAlways  = "allow_always"
	PermRejectOnce   = "reject_once"
	PermRejectAlways = "reject_always"
)

type Update interface {
	SessionUpdateType() string
}

type SessionNotification struct {
	SessionID string `json:"sessionId"`
	Update    Update `json:"update"`
}

type TextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ToolCallLocation struct {
	Path string `json:"path"`
	Line *int   `json:"line,omitempty"`
}

type ToolCallContent struct {
	Type       string `json:"type"`
	Content    any    `json:"content,omitempty"`
	TerminalID string `json:"terminalId,omitempty"`
}

type ContentChunk struct {
	SessionUpdate string `json:"sessionUpdate"`
	Content       any    `json:"content"`
}

func (u ContentChunk) SessionUpdateType() string { return u.SessionUpdate }

type ToolCall struct {
	SessionUpdate string             `json:"sessionUpdate"`
	ToolCallID    string             `json:"toolCallId"`
	Title         string             `json:"title"`
	Kind          string             `json:"kind,omitempty"`
	Status        string             `json:"status,omitempty"`
	RawInput      any                `json:"rawInput,omitempty"`
	RawOutput     any                `json:"rawOutput,omitempty"`
	Content       []ToolCallContent  `json:"content,omitempty"`
	Locations     []ToolCallLocation `json:"locations,omitempty"`
}

func (u ToolCall) SessionUpdateType() string { return u.SessionUpdate }

type ToolCallUpdate struct {
	SessionUpdate string             `json:"sessionUpdate"`
	ToolCallID    string             `json:"toolCallId"`
	Title         *string            `json:"title,omitempty"`
	Kind          *string            `json:"kind,omitempty"`
	Status        *string            `json:"status,omitempty"`
	RawInput      any                `json:"rawInput,omitempty"`
	RawOutput     any                `json:"rawOutput,omitempty"`
	Content       []ToolCallContent  `json:"content,omitempty"`
	Locations     []ToolCallLocation `json:"locations,omitempty"`
}

func (u ToolCallUpdate) SessionUpdateType() string { return u.SessionUpdate }

type PlanEntry struct {
	Content  string `json:"content"`
	Status   string `json:"status"`
	Priority string `json:"priority"`
}

type PlanUpdate struct {
	SessionUpdate string      `json:"sessionUpdate"`
	Entries       []PlanEntry `json:"entries"`
}

func (u PlanUpdate) SessionUpdateType() string { return u.SessionUpdate }

type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type RequestPermissionRequest struct {
	SessionID string             `json:"sessionId"`
	ToolCall  ToolCallUpdate     `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

type PermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

type RequestPermissionResponse struct {
	Outcome PermissionOutcome `json:"outcome"`
}
