package task

import (
	"context"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// OutputBinding is trusted Runtime identity supplied before an external effect
// can emit output. Payload events never repeat these fields.
type OutputBinding struct {
	SessionID  string
	TaskID     string
	TerminalID string
	ActivityID string
	Kind       Kind
	// StartsAtTaskOrigin reports that this observer was installed before the
	// Task's first possible output. It does not define retention or replay.
	StartsAtTaskOrigin bool
}

// OutputEvent is one identity-free producer event.
type OutputEvent struct {
	OccurredAt time.Time `json:"occurred_at,omitempty"`
	Text       string    `json:"text,omitempty"`
	State      string    `json:"state,omitempty"`
	Running    bool      `json:"running,omitempty"`
	Closed     bool      `json:"closed,omitempty"`
	// ScriptCompletion records VM settlement on a final Code Mode event. A script
	// can finish normally after catching a failed tool invocation.
	ScriptCompletion string `json:"script_completion,omitempty"`
	// ProducerClosed proves that the stable Task identity cannot emit another
	// output event. It is producer lifecycle, not a retained delivery record.
	ProducerClosed bool           `json:"producer_closed,omitempty"`
	ExitCode       *int           `json:"exit_code,omitempty"`
	Event          *session.Event `json:"event,omitempty"`
}

// OutputObserver accepts events in producer order. An observer failure affects
// only the optional observation trace and must not change Task execution.
type OutputObserver interface {
	ObserveTaskOutput(context.Context, OutputEvent) error
}

// OutputBinder installs the final observer for one immutable Task/activity
// identity.
type OutputBinder interface {
	BindTaskOutput(context.Context, OutputBinding) OutputObserver
}

type nopOutputObserver struct{}

func (nopOutputObserver) ObserveTaskOutput(context.Context, OutputEvent) error { return nil }

// NopOutputObserver returns an observer suitable when the application trace is
// unavailable.
func NopOutputObserver() OutputObserver { return nopOutputObserver{} }

// HistoryObserver accepts an ordered provider display snapshot before new live
// output on the same producer connection. It replaces only observation history,
// never Session model context or Task lifecycle. Acceptance is not disk durability.
// Display snapshots may omit old detail or prefixes; implementations bound
// retained work and report inability to accept a replacement atomically.
type HistoryObserver interface {
	ReplaceTaskHistory(context.Context, []*session.Event) error
}

// HistorySource emits one ordered observation snapshot. The consumer must
// finish using each event before returning from yield. Source ownership remains
// with the caller until ReplaceTaskHistoryStream returns, including cancellation.
type HistorySource func(context.Context, func(*session.Event) error) error

// StreamingHistoryObserver accepts history without retaining the whole replay
// in memory. Returning success means the bounded observation replacement was
// accepted; failure must not expose a partial replacement. This does not change
// canonical Session state, model context, or Task lifecycle.
type StreamingHistoryObserver interface {
	ReplaceTaskHistoryStream(context.Context, HistorySource) error
}
