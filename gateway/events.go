package gateway

// EventEnvelope wraps a gateway event for transport to surfaces.
type EventEnvelope struct {
	Kind      string
	SessionID string
	RunID     string
	TurnID    string
	Payload   any
	Metadata  map[string]any
}

// ApprovalRequest is sent when a tool call needs user approval.
type ApprovalRequest struct {
	CallID   string
	ToolName string
	Args     map[string]any
	Reason   string
}

// ApprovalResponse is the user's approval decision.
type ApprovalResponse struct {
	CallID   string
	Approved bool
	Reason   string
}

// UsageReport reports token usage for a turn.
type UsageReport struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}
