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
	// Reasoning/thinking content. Providers that need replay signatures should
	// store them in Reasoning.Replay rather than UI-only metadata.
	Reasoning *Reasoning
	// Inline data (images, audio, etc.).
	InlineData *InlineData
	// File reference.
	FileRef *FileRef
	// Tool use request (assistant only).
	ToolUse *ToolUse
	// Tool result (tool role only).
	ToolResult *ToolResult
	// ProviderMeta stores provider-specific part metadata that may be needed
	// to faithfully rebuild provider-native requests.
	ProviderMeta map[string]any
}

// ReasoningVisibility controls whether reasoning content is model-visible.
type ReasoningVisibility string

const (
	ReasoningVisibilityVisible  ReasoningVisibility = "visible"
	ReasoningVisibilityRedacted ReasoningVisibility = "redacted"
)

// Reasoning holds model reasoning/thinking content and replay metadata.
type Reasoning struct {
	Text       string
	Visibility ReasoningVisibility
	Replay     *ReplayMeta
}

// ReplayMeta preserves provider-native replay tokens such as Anthropic
// thinking signatures and Gemini thought signatures.
type ReplayMeta struct {
	Provider string
	Kind     string
	Token    string
	Metadata map[string]any
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
	CallID       string
	Name         string
	Args         map[string]any
	ArgJSON      string
	ProviderMeta map[string]any
}

// ToolResult reports the outcome of a tool call.
type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}
