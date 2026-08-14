package agentsdk

import (
	"errors"
	"fmt"
	"iter"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// ErrEventStreamGap classifies a loss in one Runner's observer-only event
// stream. The execution and its durable Session facts continue independently.
var ErrEventStreamGap = errors.New("agent-sdk: runner event stream has a delivery gap")

// EventStreamGapError reports how many observer events were overwritten before
// the currently retained suffix. It is never a Runtime execution failure or a
// cancellation signal.
type EventStreamGapError struct {
	Dropped uint64
}

func (e *EventStreamGapError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("agent-sdk: runner event stream dropped %d event(s) before the retained suffix", e.Dropped)
}

func (e *EventStreamGapError) Unwrap() error { return ErrEventStreamGap }

// AsEventStreamGap returns the typed observer gap carried by err. Callers that
// only observe a run should continue after a gap; execution owners must obtain
// final success or failure from the Runner lifecycle or durable Session facts.
func AsEventStreamGap(err error) (*EventStreamGapError, bool) {
	var gap *EventStreamGapError
	if !errors.As(err, &gap) || gap == nil {
		return nil, false
	}
	return gap, true
}

// EventSource is the minimal turn event stream required for external controller
// event forwarding. Cancel and close remain on concrete handles such as
// controller.TurnHandle at the call site.
type EventSource interface {
	Events() iter.Seq2[*session.Event, error]
}
