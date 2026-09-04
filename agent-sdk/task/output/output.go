// Package output defines the producer-only output hook used by asynchronous
// Tasks. It deliberately has no read, cursor, retention, or replay API.
package output

import (
	"context"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// TaskKind identifies the raw producer family without defining any retention
// or consumer behavior.
type TaskKind string

const (
	TaskKindCommand  TaskKind = "command"
	TaskKindSubagent TaskKind = "subagent"
)

// Binding is trusted Runtime identity supplied before an external effect can
// emit output. Payload events never repeat these fields.
type Binding struct {
	SessionID  string
	TaskID     string
	TerminalID string
	ActivityID string
	Kind       TaskKind
	// StartsAtTaskOrigin reports that this observer was installed before the
	// Task's first possible output. It does not define retention or replay.
	StartsAtTaskOrigin bool
}

// Event is one identity-free producer event.
type Event struct {
	OccurredAt time.Time `json:"occurred_at,omitempty"`
	Text       string    `json:"text,omitempty"`
	State      string    `json:"state,omitempty"`
	Running    bool      `json:"running,omitempty"`
	Closed     bool      `json:"closed,omitempty"`
	// ProducerClosed proves that the stable Task identity cannot emit another
	// output event. It is producer lifecycle, not a retained delivery record.
	ProducerClosed bool           `json:"producer_closed,omitempty"`
	ExitCode       *int           `json:"exit_code,omitempty"`
	Event          *session.Event `json:"event,omitempty"`
}

// Observer accepts events in producer order. An observer failure affects only
// the optional observation trace and must not change Task execution.
type Observer interface {
	ObserveTaskOutput(context.Context, Event) error
}

// Binder installs the final observer for one immutable Task/activity identity.
type Binder interface {
	BindTaskOutput(context.Context, Binding) Observer
}

type nopObserver struct{}

func (nopObserver) ObserveTaskOutput(context.Context, Event) error { return nil }

// Nop returns an observer suitable when the application trace is unavailable.
func Nop() Observer { return nopObserver{} }
