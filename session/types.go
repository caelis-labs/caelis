package session

import (
	"time"
)

// Ref identifies a session uniquely within an application.
type Ref struct {
	AppName      string
	UserID       string
	WorkspaceKey string
	SessionID    string
}

// Workspace describes the workspace boundary for a session.
type Workspace struct {
	Root    string
	Context map[string]string
}

// ControllerKind classifies the controller role.
type ControllerKind string

const (
	ControllerKindMain      ControllerKind = "main"
	ControllerKindSidecar   ControllerKind = "sidecar"
	ControllerKindDelegated ControllerKind = "delegated"
)

// ControllerBinding describes which controller (agent) owns a session turn.
type ControllerBinding struct {
	AgentName          string         `json:"agent_name"`
	Kind               ControllerKind `json:"kind,omitempty"`
	Label              string         `json:"label,omitempty"`
	RemoteACPSessionID string         `json:"remote_acp_session_id,omitempty"`
	ParentTurnID       string         `json:"parent_turn_id,omitempty"`
	DelegationID       string         `json:"delegation_id,omitempty"`
	ContextSyncSeq     int64          `json:"context_sync_seq,omitempty"`
	Source             string         `json:"source,omitempty"` // "builtin", "acp_agent", "acp_loopback"
	Metadata           map[string]any `json:"metadata,omitempty"`
}

// ParticipantRole classifies the participant's role.
type ParticipantRole string

const (
	ParticipantRoleDelegated ParticipantRole = "delegated"
	ParticipantRoleSidecar   ParticipantRole = "sidecar"
	ParticipantRoleController ParticipantRole = "controller"
)

// ParticipantKind classifies the participant type.
type ParticipantKind string

const (
	ParticipantKindSubagent   ParticipantKind = "subagent"
	ParticipantKindParticipant ParticipantKind = "participant"
)

// ParticipantBinding describes a participant in a session.
type ParticipantBinding struct {
	ID                 string            `json:"id"`
	Role               ParticipantRole   `json:"role,omitempty"`
	Kind               ParticipantKind   `json:"kind,omitempty"`
	AgentName          string            `json:"agent_name,omitempty"`
	Label              string            `json:"label,omitempty"`
	RemoteACPSessionID string            `json:"remote_acp_session_id,omitempty"`
	ParentTurnID       string            `json:"parent_turn_id,omitempty"`
	DelegationID       string            `json:"delegation_id,omitempty"`
	ContextSyncSeq     int64             `json:"context_sync_seq,omitempty"`
	Source             string            `json:"source,omitempty"`
	CreatedAt          time.Time         `json:"created_at,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty"`
}

// Session is the aggregate root for durable session state.
type Session struct {
	Ref          Ref
	Workspace    Workspace
	Title        string
	State        State
	Controller   ControllerBinding
	Participants []ParticipantBinding
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
