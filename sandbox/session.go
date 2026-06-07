package sandbox

import (
	"context"
	"time"
)

// SessionRef identifies one async execution session owned by a sandbox backend.
type SessionRef struct {
	Backend   string `json:"backend,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// TerminalRef identifies a terminal-like output stream owned by one async
// execution session.
type TerminalRef struct {
	Backend    string `json:"backend,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	TerminalID string `json:"terminal_id,omitempty"`
}

// SessionStatus reports the current state of an async execution session.
type SessionStatus struct {
	SessionRef
	Terminal      TerminalRef `json:"terminal,omitempty"`
	Running       bool        `json:"running,omitempty"`
	SupportsInput bool        `json:"supports_input,omitempty"`
	ExitCode      int         `json:"exit_code,omitempty"`
	StartedAt     time.Time   `json:"started_at,omitempty"`
	UpdatedAt     time.Time   `json:"updated_at,omitempty"`
	EndedAt       time.Time   `json:"ended_at,omitempty"`
}

// Session is a durable, resumable command execution session.
type Session interface {
	Ref() SessionRef
	Terminal() TerminalRef
	WriteInput(context.Context, []byte) error
	ReadOutput(context.Context, int64, int64) ([]byte, []byte, int64, int64, error)
	Status(context.Context) (SessionStatus, error)
	Wait(context.Context, time.Duration) (SessionStatus, error)
	Result(context.Context) (CommandResult, error)
	Terminate(context.Context) error
}

// AsyncBackend is optionally implemented by backends that can create and
// reopen durable async command sessions.
type AsyncBackend interface {
	Start(context.Context, CommandRequest) (Session, error)
	OpenSessionRef(SessionRef) (Session, error)
}
