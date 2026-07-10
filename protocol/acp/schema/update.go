package schema

import "encoding/json"

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
	UpdateCompact      = "compact"
	UpdateUsage        = "usage_update"
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

// Update is the ACP wire union used by the product transport. The normalized
// reusable semantics are owned by agent-sdk/session; protocol/acp/semantic is
// the only codec boundary between those semantics and these wire DTOs.
type Update interface {
	SessionUpdateType() string
}

type RawUpdate struct {
	SessionUpdate string          `json:"sessionUpdate"`
	Raw           json.RawMessage `json:"-"`
}

func (u RawUpdate) SessionUpdateType() string { return u.SessionUpdate }

func (u RawUpdate) MarshalJSON() ([]byte, error) {
	if len(u.Raw) > 0 && string(u.Raw) != "null" {
		return append([]byte(nil), u.Raw...), nil
	}
	type rawUpdate RawUpdate
	return json.Marshal(rawUpdate(u))
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
	Type       string  `json:"type"`
	Content    any     `json:"content,omitempty"`
	TerminalID string  `json:"terminalId,omitempty"`
	Path       string  `json:"path,omitempty"`
	OldText    *string `json:"oldText,omitempty"`
	NewText    string  `json:"newText,omitempty"`
}

type ContentChunk struct {
	SessionUpdate string         `json:"sessionUpdate"`
	Content       any            `json:"content"`
	MessageID     string         `json:"messageId,omitempty"`
	Meta          map[string]any `json:"_meta,omitempty"`
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
	Meta          map[string]any     `json:"_meta,omitempty"`
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
	Meta          map[string]any     `json:"_meta,omitempty"`
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

type UsageCost struct {
	Input      float64 `json:"input,omitempty"`
	Output     float64 `json:"output,omitempty"`
	CacheRead  float64 `json:"cache_read,omitempty"`
	CacheWrite float64 `json:"cache_write,omitempty"`
	Total      float64 `json:"total,omitempty"`
	Currency   string  `json:"currency,omitempty"`
}

type UsageUpdate struct {
	SessionUpdate string         `json:"sessionUpdate"`
	Size          int            `json:"size"`
	Used          int            `json:"used"`
	Cost          *UsageCost     `json:"cost,omitempty"`
	Meta          map[string]any `json:"_meta,omitempty"`
}

func (u UsageUpdate) SessionUpdateType() string { return u.SessionUpdate }

type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type RequestPermissionRequest struct {
	SessionID string             `json:"sessionId"`
	ToolCall  ToolCallUpdate     `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
	Meta      map[string]any     `json:"_meta,omitempty"`
}

type PermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

type RequestPermissionResponse struct {
	Outcome PermissionOutcome `json:"outcome"`
}
