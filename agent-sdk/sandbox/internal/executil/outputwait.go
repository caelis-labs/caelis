package executil

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

// OutputSnapshot is one coherent backend observation.
type OutputSnapshot[Status any] struct {
	Signal    <-chan struct{}
	Published sandbox.OutputCursor
	Available sandbox.OutputCursor
	Terminal  bool
	Status    Status
	Retry     bool
}

// OutputObservation is returned when published output advances or the backend is
// terminal.
type OutputObservation[Status any] struct {
	Cursor sandbox.OutputCursor
	Status Status
}

// AwaitOutput blocks until published output advances beyond cursor or the
// backend is terminal.
func AwaitOutput[Status any](
	ctx context.Context,
	cursor sandbox.OutputCursor,
	snapshot func() OutputSnapshot[Status],
) (OutputObservation[Status], error) {
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
			return OutputObservation[Status]{}, err
		}
		if state.Published.Stdout > cursor.Stdout ||
			state.Published.Stderr > cursor.Stderr ||
			state.Terminal {
			state.Published.Stdout = max(state.Published.Stdout, cursor.Stdout)
			state.Published.Stderr = max(state.Published.Stderr, cursor.Stderr)
			return OutputObservation[Status]{
				Cursor: state.Published,
				Status: state.Status,
			}, nil
		}
		select {
		case <-ctx.Done():
			return OutputObservation[Status]{}, ctx.Err()
		case <-state.Signal:
		}
	}
}
