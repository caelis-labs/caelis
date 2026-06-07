package session

import "context"

// ControllerManager is optionally implemented by session stores that
// support controller binding for multi-agent turn ownership.
type ControllerManager interface {
	// BindController sets or updates the controller for a session.
	BindController(context.Context, Ref, ControllerBinding) error
}

// ParticipantManager is optionally implemented by session stores that
// support participant lifecycle for ACP and subagent sessions.
type ParticipantManager interface {
	// PutParticipant adds or updates a participant in a session.
	PutParticipant(context.Context, Ref, ParticipantBinding) error

	// RemoveParticipant removes a participant from a session.
	RemoveParticipant(context.Context, Ref, string) error
}

// StructuredState is optionally implemented by session stores that
// support JSON-structured state values beyond flat strings.
type StructuredState interface {
	// SnapshotState returns a deep copy of the structured state.
	SnapshotState(context.Context, Ref) (map[string]any, error)

	// ReplaceState replaces the entire structured state atomically.
	ReplaceState(context.Context, Ref, map[string]any) error
}
