package taskstream

import (
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
)

// Frame is one Control-owned normalized Task delivery record. Spool offsets,
// not payload fields, define ordering and resume.
type Frame struct {
	TerminalID string         `json:"terminal_id,omitempty"`
	Text       string         `json:"text,omitempty"`
	State      string         `json:"state,omitempty"`
	ActivityID string         `json:"activity_id,omitempty"`
	Running    bool           `json:"running,omitempty"`
	Closed     bool           `json:"closed,omitempty"`
	ExitCode   *int           `json:"exit_code,omitempty"`
	Event      *session.Event `json:"event,omitempty"`
	UpdatedAt  time.Time      `json:"updated_at,omitempty"`
}

type fallbackSnapshot struct {
	Frames         []Frame
	FinalText      string
	State          string
	ActivityID     string
	Running        bool
	TerminalFramed bool
	ExitCode       *int
	UpdatedAt      time.Time
}

func cloneFrame(in Frame) Frame {
	out := in
	out.TerminalID = strings.TrimSpace(in.TerminalID)
	out.ActivityID = strings.TrimSpace(in.ActivityID)
	if in.ExitCode != nil {
		code := *in.ExitCode
		out.ExitCode = &code
	}
	out.Event = session.CloneEvent(in.Event)
	return out
}

func framesForFallback(snapshot fallbackSnapshot) []Frame {
	frames := make([]Frame, 0, len(snapshot.Frames)+1)
	hasClosed := false
	for _, frame := range snapshot.Frames {
		cloned := cloneFrame(frame)
		if cloned.ActivityID == "" {
			cloned.ActivityID = strings.TrimSpace(snapshot.ActivityID)
		}
		frames = append(frames, cloned)
		hasClosed = hasClosed || cloned.Closed
	}
	if hasClosed || snapshot.TerminalFramed || !task.IsTerminalState(task.State(snapshot.State)) {
		return frames
	}
	frames = append(frames, Frame{
		ActivityID: strings.TrimSpace(snapshot.ActivityID),
		Text:       snapshot.FinalText,
		State:      normalizedClosedState(snapshot.State, snapshot.ExitCode),
		Closed:     true,
		ExitCode:   cloneExitCode(snapshot.ExitCode),
		UpdatedAt:  snapshot.UpdatedAt,
	})
	return frames
}

func normalizedClosedState(state string, exitCode *int) string {
	state = strings.ToLower(strings.TrimSpace(state))
	if task.IsTerminalState(task.State(state)) {
		return state
	}
	if exitCode != nil && *exitCode != 0 {
		if *exitCode < 0 {
			return string(task.StateCancelled)
		}
		return string(task.StateFailed)
	}
	return string(task.StateUnknownOutcome)
}

func cloneExitCode(in *int) *int {
	if in == nil {
		return nil
	}
	code := *in
	return &code
}
