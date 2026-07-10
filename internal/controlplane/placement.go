package controlplane

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// PlacementExecutor runs a synchronous Control operation inside the same
// session-placement envelope used for asynchronous runtime dispatch.
type PlacementExecutor interface {
	ExecutePlaced(context.Context, session.SessionRef, func(context.Context) error) error
}
