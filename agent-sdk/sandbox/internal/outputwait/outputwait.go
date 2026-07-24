// Package outputwait implements the internal level-triggered wait loop shared
// by sandbox backends. Backends remain responsible for taking one coherent
// snapshot of their publication signal, published cursors, buffered cursors,
// and terminal state.
package outputwait

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

// Snapshot is one coherent backend observation. Available is the buffered
// cursor used to reject impossible resume positions; Published is the cursor
// whose bytes are safe for consumers to observe.
type Snapshot[Status any] struct {
	Signal    <-chan struct{}
	Published sandbox.OutputCursor
	Available sandbox.OutputCursor
	Terminal  bool
	Status    Status

	// Retry asks Await to take another snapshot without waiting. Backends use
	// this when a terminal transition raced a status read.
	Retry bool
}

// Observation is returned when published output advances or the backend is
// terminal.
type Observation[Status any] struct {
	Cursor sandbox.OutputCursor
	Status Status
}

// Await blocks until published output advances beyond cursor or the backend is
// terminal. It is level-triggered, non-consuming, and never cancels backend
// execution when the caller context is canceled.
func Await[Status any](
	ctx context.Context,
	cursor sandbox.OutputCursor,
	snapshot func() Snapshot[Status],
) (Observation[Status], error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cursor = sandbox.NormalizeOutputCursor(cursor)
	for {
		state := snapshot()
		if state.Retry {
			continue
		}
		if err := sandbox.ValidateOutputCursor(cursor, state.Available); err != nil {
			return Observation[Status]{}, err
		}
		if state.Published.Stdout > cursor.Stdout ||
			state.Published.Stderr > cursor.Stderr ||
			state.Terminal {
			state.Published.Stdout = max(state.Published.Stdout, cursor.Stdout)
			state.Published.Stderr = max(state.Published.Stderr, cursor.Stderr)
			return Observation[Status]{
				Cursor: state.Published,
				Status: state.Status,
			}, nil
		}
		select {
		case <-ctx.Done():
			return Observation[Status]{}, ctx.Err()
		case <-state.Signal:
		}
	}
}
