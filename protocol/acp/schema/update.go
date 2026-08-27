package schema

import (
	"encoding/json"
	"strings"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
)

const (
	UpdateUserMessage   = "user_message_chunk"
	UpdateAgentMessage  = "agent_message_chunk"
	UpdateAgentThought  = "agent_thought_chunk"
	UpdateToolCall      = "tool_call"
	UpdateToolCallInfo  = "tool_call_update"
	UpdatePlan          = "plan"
	UpdateCompact       = "compact"
	UpdateUsage         = "usage_update"
	UpdateAvailableCmds = "available_commands_update"
	UpdateCurrentMode   = "current_mode_update"
	UpdateConfigOption  = "config_option_update"
	UpdateSessionInfo   = "session_info_update"
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

// Update is the ACP wire union retained by transitional product adapters.
// Host-private ingress and presentation projectors translate these values at
// their owning boundaries; reusable semantics remain in agent-sdk/session.
type Update interface {
	SessionUpdateType() string
}

// DecodeUpdateJSON decodes one transitional projection update while preserving
// standard members owned by acp-go-sdk and unknown vendor members as RawUpdate.
func DecodeUpdateJSON(raw json.RawMessage) (Update, error) {
	var probe struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	var target Update
	switch probe.SessionUpdate {
	case UpdateUserMessage, UpdateAgentMessage, UpdateAgentThought:
		target = &ContentChunk{}
	case UpdateToolCall:
		target = &ToolCall{}
	case UpdateToolCallInfo:
		target = &ToolCallUpdate{}
	case UpdatePlan:
		target = &PlanUpdate{}
	case UpdateUsage:
		target = &UsageUpdate{}
	default:
		return RawUpdate{
			SessionUpdate: strings.TrimSpace(probe.SessionUpdate),
			Raw:           append(json.RawMessage(nil), raw...),
		}, nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return nil, err
	}
	switch typed := target.(type) {
	case *ContentChunk:
		return *typed, nil
	case *ToolCall:
		return *typed, nil
	case *ToolCallUpdate:
		return *typed, nil
	case *PlanUpdate:
		return *typed, nil
	case *UsageUpdate:
		return *typed, nil
	default:
		panic("unreachable ACP update target")
	}
}

// RawUpdate preserves an ACP update that this transitional schema does not own.
// Its raw object remains authoritative so SDK-owned and vendor fields round-trip.
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

type UsageUpdate struct {
	SessionUpdate string         `json:"sessionUpdate"`
	Size          uint64         `json:"size"`
	Used          uint64         `json:"used"`
	Cost          *acpsdk.Cost   `json:"cost,omitempty"`
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
