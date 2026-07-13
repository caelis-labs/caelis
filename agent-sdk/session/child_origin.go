package session

import (
	"fmt"
	"strings"
)

// EventChildScope identifies the client-visible child relationship of a
// durable mirror event. It is deliberately smaller than product Envelope
// scopes so the reusable SDK does not depend on the ACP wire implementation.
type EventChildScope string

const (
	EventChildScopeSubagent    EventChildScope = "subagent"
	EventChildScopeParticipant EventChildScope = "participant"
)

// EventParentTool identifies the real parent tool call that created delegated
// child work. It never represents a rendered text mirror.
type EventParentTool struct {
	CallID string `json:"call_id,omitempty"`
	Name   string `json:"name,omitempty"`
}

// EventChildOrigin is the durable, transport-neutral relation for one child
// semantic event stored under its parent Session. SourceEventID is the stable
// identity assigned by the child source; TaskID and DelegationID keep sibling
// streams isolated even when their ACP message and tool IDs are identical.
type EventChildOrigin struct {
	Scope         EventChildScope `json:"scope,omitempty"`
	ScopeID       string          `json:"scope_id,omitempty"`
	TaskID        string          `json:"task_id,omitempty"`
	DelegationID  string          `json:"delegation_id,omitempty"`
	ParticipantID string          `json:"participant_id,omitempty"`
	ACPSessionID  string          `json:"acp_session_id,omitempty"`
	SourceEventID string          `json:"source_event_id,omitempty"`
	ParentTool    EventParentTool `json:"parent_tool,omitempty"`
}

// CloneEventChildOrigin returns a normalized isolated child origin copy.
func CloneEventChildOrigin(in EventChildOrigin) EventChildOrigin {
	return EventChildOrigin{
		Scope:         in.Scope,
		ScopeID:       strings.TrimSpace(in.ScopeID),
		TaskID:        strings.TrimSpace(in.TaskID),
		DelegationID:  strings.TrimSpace(in.DelegationID),
		ParticipantID: strings.TrimSpace(in.ParticipantID),
		ACPSessionID:  strings.TrimSpace(in.ACPSessionID),
		SourceEventID: strings.TrimSpace(in.SourceEventID),
		ParentTool: EventParentTool{
			CallID: strings.TrimSpace(in.ParentTool.CallID),
			Name:   strings.TrimSpace(in.ParentTool.Name),
		},
	}
}

// ValidateEventChildOrigin checks the minimum durable relation needed to
// replay a child event without consulting display metadata.
func ValidateEventChildOrigin(in EventChildOrigin) error {
	in = CloneEventChildOrigin(in)
	switch in.Scope {
	case EventChildScopeSubagent, EventChildScopeParticipant:
	default:
		return fmt.Errorf("child origin scope is invalid: %w", ErrInvalidEvent)
	}
	if in.ScopeID == "" {
		return fmt.Errorf("child origin scope_id is required: %w", ErrInvalidEvent)
	}
	if in.SourceEventID == "" {
		return fmt.Errorf("child origin source_event_id is required: %w", ErrInvalidEvent)
	}
	if in.Scope == EventChildScopeSubagent {
		if in.TaskID == "" && in.DelegationID == "" {
			return fmt.Errorf("subagent child origin task or delegation id is required: %w", ErrInvalidEvent)
		}
		if in.ParentTool.CallID == "" {
			return fmt.Errorf("subagent child origin parent tool call is required: %w", ErrInvalidEvent)
		}
	}
	return nil
}
