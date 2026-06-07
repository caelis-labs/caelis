package acp

// Role identifies the sender of an ACP message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ContentType identifies the type of content in an ACP message.
type ContentType string

const (
	ContentTypeText       ContentType = "text"
	ContentTypeToolUse    ContentType = "tool_use"
	ContentTypeToolResult ContentType = "tool_result"
)

// Content is a piece of content in an ACP message.
type Content struct {
	Type ContentType `json:"type"`
	Text string      `json:"text,omitempty"`
	// ToolUse fields
	ToolUseID string         `json:"toolUseId,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	// ToolResult fields
	Content string `json:"content,omitempty"`
	IsError bool   `json:"isError,omitempty"`
}

// Message is an ACP protocol message.
type Message struct {
	Role    Role      `json:"role"`
	Content []Content `json:"content"`
}

// SessionID identifies an ACP session.
type SessionID string

// RunID identifies an ACP run.
type RunID string

// Request is a generic ACP request.
type Request struct {
	Method string         `json:"method"`
	Params map[string]any `json:"params,omitempty"`
}

// Response is a generic ACP response.
type Response struct {
	Result any    `json:"result,omitempty"`
	Error  *Error `json:"error,omitempty"`
}

// Error is an ACP protocol error.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ─── request_permission ──────────────────────────────────────────────

// RequestPermissionRequest is sent to the client when a tool call
// requires user approval.
type RequestPermissionRequest struct {
	SessionID string             `json:"sessionId"`
	ToolCall  ToolCallUpdate     `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

// PermissionOption is a choice the user can make.
type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // "allow_once", "always", "reject_once", "always_reject"
}

// RequestPermissionResponse is the client's response.
type RequestPermissionResponse struct {
	Outcome PermissionOutcome `json:"outcome"`
}

// PermissionOutcome is the selected permission outcome.
type PermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

// Permission option kinds.
const (
	PermAllowOnce    = "allow_once"
	PermAllowAlways  = "allow_always"
	PermAlways       = PermAllowAlways
	PermRejectOnce   = "reject_once"
	PermRejectAlways = "reject_always"
	PermAlwaysReject = PermRejectAlways
)

// PermissionSelectedOutcome returns a standard ACP permission response for a
// selected option.
func PermissionSelectedOutcome(optionID string) RequestPermissionResponse {
	return RequestPermissionResponse{
		Outcome: PermissionOutcome{
			Outcome:  "selected",
			OptionID: optionID,
		},
	}
}
