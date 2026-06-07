package model

// Request is the input to LLM.Generate.
type Request struct {
	Messages    []Message
	Tools       []ToolSpec
	Temperature *float64
	MaxTokens   int
	Stop        []string
	Output      *OutputSpec
	Reasoning   ReasoningConfig
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

// OutputMode identifies a provider-neutral desired output contract.
type OutputMode string

const (
	OutputModeText   OutputMode = "text"
	OutputModeJSON   OutputMode = "json"
	OutputModeSchema OutputMode = "schema"
)

// OutputSpec defines the desired provider-neutral output contract.
type OutputSpec struct {
	Mode            OutputMode
	JSONSchema      map[string]any
	MaxOutputTokens int
}

// ReasoningConfig controls provider reasoning/thinking behavior.
type ReasoningConfig struct {
	// Effort is provider-neutral: "none", "minimal", "low", "medium", "high",
	// "xhigh", or provider-specific values accepted by profiles.
	Effort string
	// BudgetTokens is used by providers that expose explicit thinking budgets.
	BudgetTokens int
	// Include asks providers to stream or return reasoning when supported.
	Include bool
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
	PromptTokens      int
	CachedInputTokens int
	CompletionTokens  int
	ReasoningTokens   int
	TotalTokens       int
}
