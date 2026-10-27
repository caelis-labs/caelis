package eventstream

import (
	"encoding/json"
	"strings"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
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

// Update is the Control-to-Surface ACP-shaped update union carried by Envelope.
// Host-private ingress and presentation projectors translate external or SDK
// semantics into these values at their owning boundaries.
type Update interface {
	SessionUpdateType() string
}

// DecodeUpdateJSON decodes one Control Envelope update while preserving
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
		typed.Content = restoreDecodedTextContent(typed.Content)
		return *typed, nil
	case *ToolCall:
		restoreDecodedToolContent(typed.Content)
		return *typed, nil
	case *ToolCallUpdate:
		restoreDecodedToolContent(typed.Content)
		return *typed, nil
	case *PlanUpdate:
		return *typed, nil
	case *UsageUpdate:
		return *typed, nil
	default:
		panic("unreachable ACP update target")
	}
}

// restoreDecodedTextContent recovers Control's typed text projection after a
// JSON-backed transport such as the local stream spool. Other standard and
// extension content blocks stay structurally lossless as generic JSON values.
func restoreDecodedTextContent(content any) any {
	object, ok := content.(map[string]any)
	if !ok {
		return content
	}
	typeName, _ := object["type"].(string)
	if strings.TrimSpace(typeName) != "text" {
		return content
	}
	raw, err := json.Marshal(object)
	if err != nil {
		return content
	}
	var text TextContent
	if err := json.Unmarshal(raw, &text); err != nil {
		return content
	}
	return text
}

func restoreDecodedToolContent(content []ToolCallContent) {
	for index := range content {
		if strings.TrimSpace(content[index].Type) == "content" {
			content[index].Content = restoreDecodedTextContent(content[index].Content)
		}
	}
}

// RawUpdate preserves an ACP update that Control does not project structurally.
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
	Type        string                     `json:"type"`
	Text        string                     `json:"text"`
	Annotations *acpsdk.Annotations        `json:"annotations,omitempty"`
	Meta        map[string]json.RawMessage `json:"_meta,omitempty"`
}

type ToolCallLocation struct {
	Path string `json:"path"`
	Line *int   `json:"line,omitempty"`
}

type ToolCallContent struct {
	Type       string            `json:"type"`
	Content    any               `json:"content,omitempty"`
	TerminalID string            `json:"terminalId,omitempty"`
	Path       string            `json:"path,omitempty"`
	OldText    *string           `json:"oldText,omitempty"`
	NewText    string            `json:"newText,omitempty"`
	Changes    *[]ToolDiffChange `json:"changes,omitempty"`
	Patch      *ToolDiffPatch    `json:"patch,omitempty"`
	Meta       map[string]any    `json:"_meta,omitempty"`
}

type ToolDiffChange = session.ProtocolDiffChange
type ToolDiffPatch = session.ProtocolDiffPatch

type ContentChunk struct {
	SessionUpdate string `json:"sessionUpdate"`
	Content       any    `json:"content"`
	MessageID     string `json:"messageId,omitempty"`
	// Replacement carries the complete current narrative for MessageID. Empty
	// content clears that message; Final independently owns settlement.
	Replacement bool           `json:"replacement,omitempty"`
	Meta        map[string]any `json:"_meta,omitempty"`
	// ReplacedFields recognizes "_meta" as a present metadata patch, including
	// a clear carried by nil Meta and an empty content delta.
	ReplacedFields []string `json:"replacedFields,omitempty"`
	// PreviouslyObserved is a process-local message-owner observation. It never
	// enters the public wire vocabulary or grants lifecycle authority.
	PreviouslyObserved bool `json:"-"`
}

func (u ContentChunk) SessionUpdateType() string { return u.SessionUpdate }

type ToolCall struct {
	// PreviouslyObserved is process-local Session observation, never wire authority.
	PreviouslyObserved bool               `json:"-"`
	SessionUpdate      string             `json:"sessionUpdate"`
	ToolCallID         string             `json:"toolCallId"`
	Name               *string            `json:"name,omitempty"`
	Title              string             `json:"title"`
	Kind               string             `json:"kind,omitempty"`
	Status             string             `json:"status,omitempty"`
	RawInput           any                `json:"rawInput,omitempty"`
	RawOutput          any                `json:"rawOutput,omitempty"`
	Content            []ToolCallContent  `json:"content,omitempty"`
	Locations          []ToolCallLocation `json:"locations,omitempty"`
	Meta               map[string]any     `json:"_meta,omitempty"`
}

func (u ToolCall) SessionUpdateType() string { return u.SessionUpdate }

// ToolCallUpdate is an ACP v1 sparse patch. An absent or null Name leaves the
// current tool name unchanged.
type ToolCallUpdate struct {
	// PreviouslyObserved is process-local Session observation, never wire authority.
	PreviouslyObserved bool               `json:"-"`
	SessionUpdate      string             `json:"sessionUpdate"`
	ToolCallID         string             `json:"toolCallId"`
	Name               *string            `json:"name,omitempty"`
	Title              *string            `json:"title,omitempty"`
	Kind               *string            `json:"kind,omitempty"`
	Status             *string            `json:"status,omitempty"`
	RawInput           any                `json:"rawInput,omitempty"`
	RawOutput          any                `json:"rawOutput,omitempty"`
	Content            []ToolCallContent  `json:"content,omitempty"`
	Locations          []ToolCallLocation `json:"locations,omitempty"`
	Meta               map[string]any     `json:"_meta,omitempty"`
	// ReplacedFields preserves present display patches when their value is empty.
	ReplacedFields []string `json:"replacedFields,omitempty"`
}

func (u ToolCallUpdate) SessionUpdateType() string { return u.SessionUpdate }

func (u ToolCallUpdate) MarshalJSON() ([]byte, error) {
	var content *[]ToolCallContent
	if u.Content != nil {
		content = &u.Content
	}
	return json.Marshal(struct {
		SessionUpdate  string             `json:"sessionUpdate"`
		ToolCallID     string             `json:"toolCallId"`
		Name           *string            `json:"name,omitempty"`
		Title          *string            `json:"title,omitempty"`
		Kind           *string            `json:"kind,omitempty"`
		Status         *string            `json:"status,omitempty"`
		RawInput       any                `json:"rawInput,omitempty"`
		RawOutput      any                `json:"rawOutput,omitempty"`
		Content        *[]ToolCallContent `json:"content,omitempty"`
		Locations      []ToolCallLocation `json:"locations,omitempty"`
		Meta           map[string]any     `json:"_meta,omitempty"`
		ReplacedFields []string           `json:"replacedFields,omitempty"`
	}{
		SessionUpdate:  u.SessionUpdate,
		ToolCallID:     u.ToolCallID,
		Name:           u.Name,
		Title:          u.Title,
		Kind:           u.Kind,
		Status:         u.Status,
		RawInput:       u.RawInput,
		RawOutput:      u.RawOutput,
		Content:        content,
		Locations:      u.Locations,
		Meta:           u.Meta,
		ReplacedFields: u.ReplacedFields,
	})
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

func (u PlanUpdate) SessionUpdateType() string { return u.SessionUpdate }

type UsageUpdate struct {
	SessionUpdate string         `json:"sessionUpdate"`
	Size          uint64         `json:"size"`
	Used          uint64         `json:"used"`
	Cost          *acpsdk.Cost   `json:"cost,omitempty"`
	Meta          map[string]any `json:"_meta,omitempty"`
}

func (u UsageUpdate) SessionUpdateType() string { return u.SessionUpdate }

type RequestPermissionRequest struct {
	SessionID string         `json:"sessionId"`
	ToolCall  ToolCallUpdate `json:"toolCall,omitzero"`
	// Title and Description belong to the permission prompt independently of
	// an optional tool call; they do not change a tool's displayed title.
	Title       string                    `json:"title,omitempty"`
	Description *string                   `json:"description,omitempty"`
	Options     []acpsdk.PermissionOption `json:"options"`
	Meta        map[string]any            `json:"_meta,omitempty"`
}
