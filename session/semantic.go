package session

// This file defines the semantic payload types for canonical session events.
//
// Design rules:
//   - One event = one semantic unit (message, tool call, tool result, plan, etc.)
//   - Payloads store semantic data only; projections are computed
//   - Tool calls and results are separate events linked by CallID
//   - EventPart is the unified content atom across all payload types
//   - Provider-specific replay data lives in Event.ProviderMeta, not in payloads

// ─── Message payloads ────────────────────────────────────────────────

// UserPayload carries the user's input.
type UserPayload struct {
	Parts []EventPart `json:"parts"`
}

// AssistantPayload carries the model's output.
type AssistantPayload struct {
	Parts []EventPart `json:"parts"`
}

// SystemPayload carries system context injected into the conversation.
type SystemPayload struct {
	Parts []EventPart `json:"parts"`
}

// ─── EventPart ───────────────────────────────────────────────────────

// PartKind identifies the type of content in an EventPart.
type PartKind string

const (
	PartKindText       PartKind = "text"
	PartKindReasoning  PartKind = "reasoning"
	PartKindToolUse    PartKind = "tool_use"
	PartKindToolResult PartKind = "tool_result"
	PartKindMedia      PartKind = "media"
	PartKindFileRef    PartKind = "file_ref"
	PartKindJSON       PartKind = "json"
)

// EventPart is the unified content atom. Exactly one content field should
// be populated based on Kind.
type EventPart struct {
	Kind PartKind `json:"kind"`

	// Text content (PartKindText, PartKindReasoning).
	Text string `json:"text,omitempty"`

	// ToolUse references a tool call request (PartKindToolUse).
	// Links to a separate tool_call event via CallID.
	ToolUse *PartToolUse `json:"tool_use,omitempty"`

	// ToolResult references a tool result (PartKindToolResult).
	// Links to a separate tool_result event via CallID.
	ToolResultRef *PartToolResult `json:"tool_result_ref,omitempty"`

	// Media is inline binary content (PartKindMedia).
	Media *PartMedia `json:"media,omitempty"`

	// FileRef references a file by URI (PartKindFileRef).
	FileRef *PartFileRef `json:"file_ref,omitempty"`

	// JSON is structured JSON content (PartKindJSON).
	JSON any `json:"json,omitempty"`
}

// PartToolUse is a tool call request embedded in an assistant message part.
type PartToolUse struct {
	CallID string         `json:"call_id"`
	Name   string         `json:"name"`
	Args   map[string]any `json:"args,omitempty"`
}

// PartToolResult is a tool result embedded in a tool-role message part.
type PartToolResult struct {
	CallID  string `json:"call_id"`
	Name    string `json:"name,omitempty"`
	Content string `json:"content,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
}

// PartMedia is inline binary content.
type PartMedia struct {
	Modality string `json:"modality"` // "image", "audio", "video"
	MIMEType string `json:"mime_type"`
	Data     []byte `json:"data,omitempty"`
	URI      string `json:"uri,omitempty"`
}

// PartFileRef references a file.
type PartFileRef struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mime_type,omitempty"`
	Name     string `json:"name,omitempty"`
}

// ─── Tool payloads ───────────────────────────────────────────────────

// ToolCallPayload carries a single tool call request.
// This is a separate event from the assistant message that triggered it.
type ToolCallPayload struct {
	CallID string         `json:"call_id"`
	Name   string         `json:"name"`
	Kind   string         `json:"kind,omitempty"`  // tool category
	Title  string         `json:"title,omitempty"` // human-readable title
	Status string         `json:"status"`          // "pending", "running", "completed", "error"
	Args   map[string]any `json:"args,omitempty"`  // decoded arguments
	// ArgJSON preserves the raw JSON for lossless round-trip.
	// When Args is sufficient, ArgJSON may be omitted.
	ArgJSON string `json:"arg_json,omitempty"`
	// Display contains UI-only content (terminal output, diffs, etc.).
	// Never included in model context.
	Display []EventPart `json:"display,omitempty"`
	// Truncation records how tool output was truncated.
	Truncation *TruncationMeta `json:"truncation,omitempty"`
}

// ToolResultPayload carries the result of a tool call.
// This is a separate event linked to the tool_call event via CallID.
type ToolResultPayload struct {
	CallID  string `json:"call_id"`
	Name    string `json:"name"`
	Kind    string `json:"kind,omitempty"`
	Title   string `json:"title,omitempty"`
	Status  string `json:"status"` // "completed", "error"
	IsError bool   `json:"is_error"`
	// Content is the model-visible output parts.
	Content []EventPart `json:"content"`
	// Display is the UI-only output (terminal, diffs, rich rendering).
	// Never included in model context.
	Display []EventPart `json:"display,omitempty"`
	// Truncation records how tool output was truncated.
	Truncation *TruncationMeta `json:"truncation,omitempty"`
}

// TruncationMeta records how content was truncated.
type TruncationMeta struct {
	OriginalSize int    `json:"original_size"`
	TruncatedTo  int    `json:"truncated_to"`
	Strategy     string `json:"strategy"` // "head", "tail", "middle", "none"
}

// ─── Plan payload ────────────────────────────────────────────────────

// PlanPayload carries structured plan state.
type PlanPayload struct {
	Entries     []PlanEntry `json:"entries"`
	Explanation string      `json:"explanation,omitempty"`
}

// PlanEntry is a single plan step.
type PlanEntry struct {
	Content string `json:"content"`
	Status  string `json:"status"` // "pending", "in_progress", "completed"
}

// ─── Control payloads ────────────────────────────────────────────────

// CompactionPayload records that earlier events were compacted.
type CompactionPayload struct {
	Reason      string `json:"reason"`
	Previous    int    `json:"previous"`     // events before compaction
	Remaining   int    `json:"remaining"`    // events after compaction
	SummaryText string `json:"summary_text"` // human-readable summary
}

// LifecyclePayload records session lifecycle transitions.
type LifecyclePayload struct {
	Action  string         `json:"action"` // "created", "forked", "deleted", "started", "ended"
	Details map[string]any `json:"details,omitempty"`
}

// NoticePayload carries a runtime notice.
type NoticePayload struct {
	Level   string         `json:"level"` // "info", "warning", "error"
	Text    string         `json:"text"`
	Details map[string]any `json:"details,omitempty"`
}

// HandoffPayload records a control handoff between agents.
type HandoffPayload struct {
	FromAgent string `json:"from_agent"`
	ToAgent   string `json:"to_agent"`
	Reason    string `json:"reason,omitempty"`
}

// ─── Helper methods ──────────────────────────────────────────────────

// TextContent returns the concatenated text from all text parts in the
// event's payload. Works for user, assistant, and system payloads.
func (e *Event) TextContent() string {
	var parts []EventPart
	switch {
	case e.UserPayload != nil:
		parts = e.UserPayload.Parts
	case e.AssistantPayload != nil:
		parts = e.AssistantPayload.Parts
	case e.SystemPayload != nil:
		parts = e.SystemPayload.Parts
	default:
		return ""
	}
	var buf []byte
	for _, p := range parts {
		if p.Kind == PartKindText || p.Kind == PartKindReasoning {
			buf = append(buf, p.Text...)
		}
	}
	return string(buf)
}

// ToolCallIDs returns all tool call IDs referenced in this event.
func (e *Event) ToolCallIDs() []string {
	if e.ToolCallPayload != nil {
		return []string{e.ToolCallPayload.CallID}
	}
	var ids []string
	if e.AssistantPayload != nil {
		for _, p := range e.AssistantPayload.Parts {
			if p.Kind == PartKindToolUse && p.ToolUse != nil {
				ids = append(ids, p.ToolUse.CallID)
			}
		}
	}
	return ids
}

// IsCanonical reports whether this event is a canonical history event.
func (e *Event) IsCanonical() bool {
	return e.Visibility == VisibilityCanonical
}

// IsModelVisible reports whether this event should be in model context.
func (e *Event) IsModelVisible() bool {
	return e.Visibility.IsModelVisible()
}

// IsPersisted reports whether this event should be written to storage.
func (e *Event) IsPersisted() bool {
	return e.Visibility.IsPersisted()
}
