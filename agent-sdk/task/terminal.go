package task

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// TerminalRef identifies one command Task owned by one Session.
type TerminalRef struct {
	SessionID  string `json:"session_id,omitempty"`
	TaskID     string `json:"task_id,omitempty"`
	TerminalID string `json:"terminal_id,omitempty"`
}

// TerminalSnapshot is one bounded point-in-time command observation. Output is
// a convenience view for explicit terminal inspection; FinalResult is the
// authoritative display fallback after completion.
type TerminalSnapshot struct {
	Ref           TerminalRef `json:"ref,omitempty"`
	State         string      `json:"state,omitempty"`
	Running       bool        `json:"running,omitempty"`
	SupportsInput bool        `json:"supports_input,omitempty"`
	Output        string      `json:"output,omitempty"`
	Truncated     bool        `json:"truncated,omitempty"`
	FinalResult   string      `json:"final_result,omitempty"`
	ExitCode      *int        `json:"exit_code,omitempty"`
	StartedAt     time.Time   `json:"started_at,omitempty"`
	UpdatedAt     time.Time   `json:"updated_at,omitempty"`
}

// TerminalProvider exposes optional command inspection and process control to
// the host.
type TerminalProvider interface {
	Terminals() TerminalController
}

// TerminalController owns explicit command inspection and process control only.
type TerminalController interface {
	Read(context.Context, TerminalRef) (TerminalSnapshot, error)
	Wait(context.Context, TerminalRef) (TerminalSnapshot, error)
	Kill(context.Context, TerminalRef) error
	Release(context.Context, TerminalRef) error
}

// NormalizeTerminalRef trims one terminal reference.
func NormalizeTerminalRef(in TerminalRef) TerminalRef {
	return TerminalRef{
		SessionID:  strings.TrimSpace(in.SessionID),
		TaskID:     strings.TrimSpace(in.TaskID),
		TerminalID: strings.TrimSpace(in.TerminalID),
	}
}

// ValidateTerminalRef requires the stable Session and Task address.
func ValidateTerminalRef(in TerminalRef) error {
	ref := NormalizeTerminalRef(in)
	if ref.SessionID == "" {
		return fmt.Errorf("agent-sdk/task: terminal session_id is required")
	}
	if ref.TaskID == "" {
		return fmt.Errorf("agent-sdk/task: terminal task_id is required")
	}
	return nil
}

// CloneTerminalSnapshot returns one isolated terminal snapshot value.
func CloneTerminalSnapshot(in TerminalSnapshot) TerminalSnapshot {
	out := in
	out.Ref = NormalizeTerminalRef(in.Ref)
	if in.ExitCode != nil {
		code := *in.ExitCode
		out.ExitCode = &code
	}
	return out
}
