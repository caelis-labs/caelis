package model

// Request is the input to LLM.Generate.
type Request struct {
	Messages    []Message
	Tools       []ToolSpec
	Temperature *float64
	MaxTokens   int
	Stop        []string
	Metadata    map[string]any
}

// ToolSpec describes a tool available to the model.
type ToolSpec struct {
	Name        string
	Description string
	Schema      Schema
}

// Schema is a JSON Schema description for tool arguments.
type Schema struct {
	Type        string
	Properties  map[string]Schema
	Required    []string
	Items       *Schema
	Enum        []any
	Format      string
	Description string
}

// ResponseEvent is a streamed event from LLM.Generate.
type ResponseEvent struct {
	// TextDelta is a text chunk (may be empty).
	TextDelta string
	// ReasoningDelta is a reasoning/thinking chunk.
	ReasoningDelta string
	// ToolCall is a tool call request (complete, not streamed).
	ToolCall *ToolCallDelta
	// Usage reports token usage.
	Usage *Usage
	// FinishReason indicates why generation stopped.
	FinishReason string
	// Metadata is provider-specific metadata.
	Metadata map[string]any
}

// ToolCallDelta represents a tool call in a streamed response.
type ToolCallDelta struct {
	CallID string
	Name   string
	Args   map[string]any
}

// Usage reports token consumption.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}
