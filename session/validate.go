package session

import (
	"fmt"
	"time"
)

// ValidateEvent checks that an event is well-formed for durable persistence.
// It is called at the store boundary before AppendEvent.
func ValidateEvent(e *Event) error {
	if e == nil {
		return fmt.Errorf("event: nil")
	}
	if e.Kind == "" {
		return fmt.Errorf("event: kind is required")
	}
	if e.Visibility == "" {
		return fmt.Errorf("event %s: visibility is required", e.Kind)
	}

	// Payload-kind matching: exactly one payload must match the kind.
	switch e.Kind {
	case EventKindUser:
		if e.UserPayload == nil {
			return fmt.Errorf("event %s: UserPayload required", e.Kind)
		}
		if len(e.UserPayload.Parts) == 0 {
			return fmt.Errorf("event %s: at least one part required", e.Kind)
		}
	case EventKindAssistant:
		if e.AssistantPayload == nil {
			return fmt.Errorf("event %s: AssistantPayload required", e.Kind)
		}
	case EventKindToolCall:
		if e.ToolCallPayload == nil {
			return fmt.Errorf("event %s: ToolCallPayload required", e.Kind)
		}
		if e.ToolCallPayload.CallID == "" {
			return fmt.Errorf("event %s: CallID required", e.Kind)
		}
		if e.ToolCallPayload.Name == "" {
			return fmt.Errorf("event %s: Name required", e.Kind)
		}
	case EventKindToolResult:
		if e.ToolResultPayload == nil {
			return fmt.Errorf("event %s: ToolResultPayload required", e.Kind)
		}
		if e.ToolResultPayload.CallID == "" {
			return fmt.Errorf("event %s: CallID required", e.Kind)
		}
	case EventKindPlan:
		if e.PlanPayload == nil {
			return fmt.Errorf("event %s: PlanPayload required", e.Kind)
		}
	case EventKindCompaction:
		if e.CompactionPayload == nil {
			return fmt.Errorf("event %s: CompactionPayload required", e.Kind)
		}
	case EventKindSystem:
		if e.SystemPayload == nil {
			return fmt.Errorf("event %s: SystemPayload required", e.Kind)
		}
	case EventKindLifecycle:
		if e.LifecyclePayload == nil {
			return fmt.Errorf("event %s: LifecyclePayload required", e.Kind)
		}
	case EventKindNotice:
		if e.NoticePayload == nil {
			return fmt.Errorf("event %s: NoticePayload required", e.Kind)
		}
	case EventKindHandoff:
		if e.HandoffPayload == nil {
			return fmt.Errorf("event %s: HandoffPayload required", e.Kind)
		}
		if e.FromAgent == "" || e.ToAgent == "" {
			return fmt.Errorf("event %s: FromAgent and ToAgent required", e.Kind)
		}
	case EventKindParticipant:
		if e.ParticipantPayload == nil {
			return fmt.Errorf("event %s: ParticipantPayload required", e.Kind)
		}
		if e.ParticipantID == "" {
			return fmt.Errorf("event %s: ParticipantID required", e.Kind)
		}
	}

	// Canonical/mirror events must be durable.
	if e.Visibility.IsPersisted() && !e.IsPersisted() {
		return fmt.Errorf("event %s: canonical/mirror events must be durable", e.Kind)
	}

	return nil
}

// CanonicalizeEvent applies zero-value defaults and normalizes an event
// before persistence. It does NOT preserve old v1 compatibility fields.
func CanonicalizeEvent(e *Event) {
	if e.Visibility == "" {
		e.Visibility = VisibilityCanonical
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = timeNow()
	}
	// Normalize empty metadata to nil.
	if len(e.ProviderMeta) == 0 {
		e.ProviderMeta = nil
	}
	// Normalize empty display arrays to nil.
	if e.ToolCallPayload != nil && len(e.ToolCallPayload.Display) == 0 {
		e.ToolCallPayload.Display = nil
	}
	if e.ToolResultPayload != nil && len(e.ToolResultPayload.Display) == 0 {
		e.ToolResultPayload.Display = nil
	}
}

// timeNow is a variable so tests can override it.
var timeNow = func() time.Time { return time.Now() }
