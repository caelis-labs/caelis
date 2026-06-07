package model

// Role identifies the sender of a message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
	RoleSystem    Role = "system"
)

// Message is a single message in a model conversation.
type Message struct {
	Role    Role
	Content []Part
}

// Part is a content element within a message.
type Part struct {
	// Text content.
	Text string
	// Inline data (images, audio, etc.).
	InlineData *InlineData
	// File reference.
	FileRef *FileRef
	// Tool use request (assistant only).
	ToolUse *ToolUse
	// Tool result (tool role only).
	ToolResult *ToolResult
}

// InlineData holds binary content embedded in a message.
type InlineData struct {
	MIMEType string
	Data     []byte
}

// FileRef references a file by URI.
type FileRef struct {
	URI      string
	MIMEType string
}

// ToolUse requests a tool call.
type ToolUse struct {
	CallID  string
	Name    string
	Args    map[string]any
	ArgJSON string
}

// ToolResult reports the outcome of a tool call.
type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}
