package client

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
	UpdateToolCallState = "tool_call_update"
	UpdateAvailableCmds = "available_commands_update"
	UpdatePlan          = "plan"
	UpdateUsage         = "usage_update"
	UpdateCurrentMode   = "current_mode_update"
	UpdateConfigOption  = "config_option_update"
	UpdateSessionInfo   = "session_info_update"
)

const (
	toolKindRead    = string(acpsdk.ToolKindRead)
	toolKindEdit    = string(acpsdk.ToolKindEdit)
	toolKindSearch  = string(acpsdk.ToolKindSearch)
	toolKindExecute = string(acpsdk.ToolKindExecute)
	toolKindOther   = string(acpsdk.ToolKindOther)
)

// SessionNotification retains the external Agent's raw update until the
// Host-private compatibility decoder has classified it.
type SessionNotification struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

// ContentChunk retains the raw content union for delayed decoding. External
// Agents may emit future content variants that the current SDK cannot decode.
type ContentChunk struct {
	SessionUpdate string          `json:"sessionUpdate"`
	Content       json.RawMessage `json:"content"`
	MessageID     string          `json:"messageId,omitempty"`
	Meta          map[string]any  `json:"_meta,omitempty"`
}

type TextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type TextChunk struct {
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

// ToolCall is the tolerant external-Agent tool-call representation. Standard
// values are projected into Control eventstream types only after ingress
// compatibility and trust normalization have run.
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

// ToolCallUpdate is the tolerant sparse external-Agent tool update.
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

type PlanEntry struct {
	Content  string `json:"content"`
	Status   string `json:"status"`
	Priority string `json:"priority"`
}

type PlanUpdate struct {
	SessionUpdate string      `json:"sessionUpdate"`
	Entries       []PlanEntry `json:"entries"`
}

type UsageUpdate struct {
	SessionUpdate string         `json:"sessionUpdate"`
	Size          uint64         `json:"size"`
	Used          uint64         `json:"used"`
	Cost          *acpsdk.Cost   `json:"cost,omitempty"`
	Meta          map[string]any `json:"_meta,omitempty"`
}

// RawUpdate preserves an unknown external-Agent update without giving it
// Control semantics. The controller boundary may later wrap it in the Control
// eventstream RawUpdate used for Surface delivery.
type RawUpdate struct {
	SessionUpdate string          `json:"sessionUpdate"`
	Raw           json.RawMessage `json:"-"`
}

func (u RawUpdate) MarshalJSON() ([]byte, error) {
	if len(u.Raw) > 0 && string(u.Raw) != "null" {
		return append([]byte(nil), u.Raw...), nil
	}
	type rawUpdate RawUpdate
	return json.Marshal(rawUpdate(u))
}

type Update any

type UpdateEnvelope struct {
	SessionID string
	Update    Update
	Raw       json.RawMessage
}

func decodeUpdate(raw json.RawMessage) (Update, error) {
	var probe struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	switch probe.SessionUpdate {
	case UpdateUserMessage, UpdateAgentMessage, UpdateAgentThought:
		var update ContentChunk
		if err := json.Unmarshal(raw, &update); err != nil {
			return nil, err
		}
		return update, nil
	case UpdateToolCall:
		var update ToolCall
		if err := json.Unmarshal(raw, &update); err != nil {
			return nil, err
		}
		return update, nil
	case UpdateToolCallState:
		var update ToolCallUpdate
		if err := json.Unmarshal(raw, &update); err != nil {
			return nil, err
		}
		return update, nil
	case UpdatePlan:
		var update PlanUpdate
		if err := json.Unmarshal(raw, &update); err != nil {
			return nil, err
		}
		return update, nil
	case UpdateUsage:
		var update UsageUpdate
		if err := json.Unmarshal(raw, &update); err != nil {
			return nil, err
		}
		return update, nil
	case UpdateAvailableCmds, UpdateConfigOption, UpdateCurrentMode, UpdateSessionInfo:
		return decodeStandardSessionStateUpdate(raw, probe.SessionUpdate)
	default:
		return RawUpdate{
			SessionUpdate: strings.TrimSpace(probe.SessionUpdate),
			Raw:           append(json.RawMessage(nil), raw...),
		}, nil
	}
}
