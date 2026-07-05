package agentsdk

import (
	"iter"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// EventSource is the minimal turn event stream required for external controller
// event forwarding. Cancel and close remain on concrete handles such as
// controller.TurnHandle at the call site.
type EventSource interface {
	Events() iter.Seq2[*session.Event, error]
}
