package stream

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
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
	Ref    Ref    `json:"ref,omitempty"`
	Text   string `json:"text,omitempty"`
	State  string `json:"state,omitempty"`
	Cursor Cursor `json:"cursor,omitempty"`
	// TruncatedBefore is the earliest absolute output byte still retained when
	// the requested cursor predates a bounded live buffer.
	TruncatedBefore int64 `json:"truncated_before,omitempty"`
	// EventsTruncatedBefore is the earliest absolute event cursor still
	// retained by this task stream. It is independent from command bytes.
	EventsTruncatedBefore int64          `json:"events_truncated_before,omitempty"`
	Running               bool           `json:"running,omitempty"`
	Closed                bool           `json:"closed,omitempty"`
	ExitCode              *int           `json:"exit_code,omitempty"`
	Event                 *session.Event `json:"event,omitempty"`
	UpdatedAt             time.Time      `json:"updated_at,omitempty"`
}

// Snapshot is one point-in-time stream read result.
type Snapshot struct {
	Ref       Ref     `json:"ref,omitempty"`
	Cursor    Cursor  `json:"cursor,omitempty"`
	Frames    []Frame `json:"frames,omitempty"`
	FinalText string  `json:"final_text,omitempty"`
	State     string  `json:"state,omitempty"`
	// TruncatedBefore is copied to delivered frames so consumers can make a
	// missing prefix visible instead of treating a retained suffix as complete.
	TruncatedBefore       int64 `json:"truncated_before,omitempty"`
	EventsTruncatedBefore int64 `json:"events_truncated_before,omitempty"`
	Running               bool  `json:"running,omitempty"`
	// SupportsInput is the owning Task's Task-plane input capability. For a
	// completed subagent it means TASK write may Continue the same Task; it is
	// not a terminal stdin capability.
	SupportsInput bool `json:"supports_input,omitempty"`
	// TerminalFramed means the producer owns explicit terminal-frame delivery;
	// consumers must not infer another close from Running=false.
	TerminalFramed bool      `json:"terminal_framed,omitempty"`
	ExitCode       *int      `json:"exit_code,omitempty"`
	StartedAt      time.Time `json:"started_at,omitempty"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
}

// ReadRequest asks for one incremental stream read from one cursor.
type ReadRequest struct {
	Ref    Ref    `json:"ref,omitempty"`
	Cursor Cursor `json:"cursor,omitempty"`
}

// SubscribeRequest asks for one stream subscription.
type SubscribeRequest struct {
	Ref    Ref    `json:"ref,omitempty"`
	Cursor Cursor `json:"cursor,omitempty"`
	// FollowContinues keeps a completed Task whose Task-plane SupportsInput
	// capability is true open so a later Continue is delivered on the same
	// Task stream. It does not imply terminal stdin ownership.
	FollowContinues bool `json:"follow_continues,omitempty"`
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

// ValidateRef requires the canonical task-stream address. TerminalID is
// display and turn metadata; it is never an alternative resource identity.
func ValidateRef(in Ref) error {
	ref := NormalizeRef(in)
	if ref.SessionID == "" {
		return fmt.Errorf("task stream: session_id is required")
	}
	if ref.TaskID == "" {
		return fmt.Errorf("task stream: task_id is required")
	}
	return nil
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
	if out.TruncatedBefore < 0 {
		out.TruncatedBefore = 0
	}
	if out.EventsTruncatedBefore < 0 {
		out.EventsTruncatedBefore = 0
	}
	if in.ExitCode != nil {
		code := *in.ExitCode
		out.ExitCode = &code
	}
	out.Event = session.CloneEvent(in.Event)
	return out
}

// CloneSnapshot returns one isolated snapshot copy.
func CloneSnapshot(in Snapshot) Snapshot {
	out := in
	out.Ref = NormalizeRef(in.Ref)
	out.Cursor = CloneCursor(in.Cursor)
	if out.TruncatedBefore < 0 {
		out.TruncatedBefore = 0
	}
	if out.EventsTruncatedBefore < 0 {
		out.EventsTruncatedBefore = 0
	}
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
