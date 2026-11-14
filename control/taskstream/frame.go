package taskstream

import (
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
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
