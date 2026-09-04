// Package terminal defines command process control and bounded point-in-time
// inspection. It is not a stream API: asynchronous output delivery, cursors,
// replay, retention, and fan-out belong to product Control.
package terminal

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Ref identifies one command Task owned by one Session.
type Ref struct {
	SessionID  string `json:"session_id,omitempty"`
	TaskID     string `json:"task_id,omitempty"`
	TerminalID string `json:"terminal_id,omitempty"`
}

// Snapshot is one bounded point-in-time command observation. Output is a
// convenience view for explicit terminal inspection; FinalResult is the
// authoritative display fallback after completion.
type Snapshot struct {
	Ref           Ref       `json:"ref,omitempty"`
	State         string    `json:"state,omitempty"`
	Running       bool      `json:"running,omitempty"`
	SupportsInput bool      `json:"supports_input,omitempty"`
	Output        string    `json:"output,omitempty"`
	Truncated     bool      `json:"truncated,omitempty"`
	FinalResult   string    `json:"final_result,omitempty"`
	ExitCode      *int      `json:"exit_code,omitempty"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
}

// Controller owns explicit command inspection and process control only.
type Controller interface {
	Read(context.Context, Ref) (Snapshot, error)
	Wait(context.Context, Ref) (Snapshot, error)
	Kill(context.Context, Ref) error
	Release(context.Context, Ref) error
}

// NormalizeRef trims one reference.
func NormalizeRef(in Ref) Ref {
	return Ref{
		SessionID:  strings.TrimSpace(in.SessionID),
		TaskID:     strings.TrimSpace(in.TaskID),
		TerminalID: strings.TrimSpace(in.TerminalID),
	}
}

// ValidateRef requires the stable Session and Task address.
func ValidateRef(in Ref) error {
	ref := NormalizeRef(in)
	if ref.SessionID == "" {
		return fmt.Errorf("terminal: session_id is required")
	}
	if ref.TaskID == "" {
		return fmt.Errorf("terminal: task_id is required")
	}
	return nil
}

// CloneSnapshot returns one isolated value.
func CloneSnapshot(in Snapshot) Snapshot {
	out := in
	out.Ref = NormalizeRef(in.Ref)
	if in.ExitCode != nil {
		code := *in.ExitCode
		out.ExitCode = &code
	}
	return out
}
