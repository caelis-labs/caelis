package stream

import (
	"context"
	"iter"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/session"
)

// Ref identifies one output stream owned by one task in one session.
type Ref struct {
	SessionID  string `json:"session_id,omitempty"`
	TaskID     string `json:"task_id,omitempty"`
	TerminalID string `json:"terminal_id,omitempty"`
}

// Cursor identifies the caller's last consumed terminal-text position.
type Cursor struct {
	Output int64 `json:"output,omitempty"`
	Events int64 `json:"events,omitempty"`
}

// Frame is one terminal text fragment delivered to one UI or adapter. Runtime
// stdout/stderr/result details are normalized before entering this stream.
type Frame struct {
	Ref       Ref            `json:"ref,omitempty"`
	Text      string         `json:"text,omitempty"`
	State     string         `json:"state,omitempty"`
	Cursor    Cursor         `json:"cursor,omitempty"`
	Running   bool           `json:"running,omitempty"`
	Closed    bool           `json:"closed,omitempty"`
	Event     *session.Event `json:"event,omitempty"`
	UpdatedAt time.Time      `json:"updated_at,omitempty"`
}

// Snapshot is one point-in-time stream read result.
type Snapshot struct {
	Ref           Ref       `json:"ref,omitempty"`
	Cursor        Cursor    `json:"cursor,omitempty"`
	Frames        []Frame   `json:"frames,omitempty"`
	FinalText     string    `json:"final_text,omitempty"`
	State         string    `json:"state,omitempty"`
	Running       bool      `json:"running,omitempty"`
	SupportsInput bool      `json:"supports_input,omitempty"`
	ExitCode      *int      `json:"exit_code,omitempty"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
}

// ReadRequest asks for one incremental stream read from one cursor.
type ReadRequest struct {
	Ref    Ref    `json:"ref,omitempty"`
	Cursor Cursor `json:"cursor,omitempty"`
}

// SubscribeRequest asks for one polling stream subscription.
type SubscribeRequest struct {
	Ref          Ref           `json:"ref,omitempty"`
	Cursor       Cursor        `json:"cursor,omitempty"`
	PollInterval time.Duration `json:"poll_interval,omitempty"`
}

// Service is the unified output read/subscribe surface used by app-layer
// renderers and protocol adapters. Control actions remain on the task plane.
type Service interface {
	Read(context.Context, ReadRequest) (Snapshot, error)
	Subscribe(context.Context, SubscribeRequest) iter.Seq2[*Frame, error]
}

// Sink receives output frames produced by a runtime component and writes them
// into the owning task stream.
type Sink interface {
	PublishStream(Frame)
}

// Controller is one optional terminal control surface used by app adapters.
// Agent-facing task control remains on the TASK tool plane.
type Controller interface {
	Service
	Wait(context.Context, Ref) (Snapshot, error)
	Kill(context.Context, Ref) error
	Release(context.Context, Ref) error
}

// NormalizeRef returns one trimmed terminal ref.
func NormalizeRef(in Ref) Ref {
	return Ref{
		SessionID:  strings.TrimSpace(in.SessionID),
		TaskID:     strings.TrimSpace(in.TaskID),
		TerminalID: strings.TrimSpace(in.TerminalID),
	}
}

// CloneCursor returns one normalized cursor copy.
func CloneCursor(in Cursor) Cursor {
	if in.Output < 0 {
		in.Output = 0
	}
	if in.Events < 0 {
		in.Events = 0
	}
	return in
}

// CloneFrame returns one isolated frame copy.
func CloneFrame(in Frame) Frame {
	out := in
	out.Ref = NormalizeRef(in.Ref)
	out.Cursor = CloneCursor(in.Cursor)
	out.Event = session.CloneEvent(in.Event)
	return out
}

// CloneSnapshot returns one isolated snapshot copy.
func CloneSnapshot(in Snapshot) Snapshot {
	out := in
	out.Ref = NormalizeRef(in.Ref)
	out.Cursor = CloneCursor(in.Cursor)
	if in.ExitCode != nil {
		code := *in.ExitCode
		out.ExitCode = &code
	}
	if len(in.Frames) > 0 {
		out.Frames = make([]Frame, 0, len(in.Frames))
		for _, frame := range in.Frames {
			out.Frames = append(out.Frames, CloneFrame(frame))
		}
	}
	return out
}
