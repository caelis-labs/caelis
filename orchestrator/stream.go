package orchestrator

import (
	"time"

	"github.com/OnslaughtSnail/caelis/session"
)

// StreamFrame represents a streaming update from a child agent to the parent.
type StreamFrame struct {
	// TaskID identifies the child task.
	TaskID string

	// SessionID is the child's session ID.
	SessionID string

	// Text is the running text content (may be partial).
	Text string

	// State is the child's current state.
	State DelegationState

	// Running indicates whether the child is still executing.
	Running bool

	// Closed indicates this is the final frame.
	Closed bool

	// Event is an optional embedded child session event (for subagent panels).
	Event *session.Event

	// UpdatedAt is the frame timestamp.
	UpdatedAt time.Time
}

// StreamSink receives streaming frames from child agents.
type StreamSink interface {
	PublishStream(StreamFrame)
}

// ConvertACPUpdateToFrame converts an ACP update notification from a child
// agent into a StreamFrame for the parent's stream sink.
func ConvertACPUpdateToFrame(taskID string, sessionID string, text string, running bool, closed bool) StreamFrame {
	state := DelegationRunning
	if closed {
		state = DelegationCompleted
	}
	return StreamFrame{
		TaskID:    taskID,
		SessionID: sessionID,
		Text:      text,
		State:     state,
		Running:   running,
		Closed:    closed,
		UpdatedAt: time.Now(),
	}
}

// FinalFrame creates a closed stream frame with the final output.
func FinalFrame(taskID string, sessionID string, output string) StreamFrame {
	return StreamFrame{
		TaskID:    taskID,
		SessionID: sessionID,
		Text:      output,
		State:     DelegationCompleted,
		Running:   false,
		Closed:    true,
		UpdatedAt: time.Now(),
	}
}

// ErrorFrame creates a closed stream frame with an error.
func ErrorFrame(taskID string, sessionID string, err error) StreamFrame {
	text := ""
	if err != nil {
		text = "error: " + err.Error()
	}
	return StreamFrame{
		TaskID:    taskID,
		SessionID: sessionID,
		Text:      text,
		State:     DelegationFailed,
		Running:   false,
		Closed:    true,
		UpdatedAt: time.Now(),
	}
}
