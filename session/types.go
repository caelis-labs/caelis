package session

import "time"

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

// ControllerBinding describes which controller (agent) owns a session turn.
type ControllerBinding struct {
	AgentName string
}

// ParticipantBinding describes a participant in a session.
type ParticipantBinding struct {
	ID       string
	Role     string
	Metadata map[string]string
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
