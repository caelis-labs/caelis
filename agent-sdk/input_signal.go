package agentsdk

import "context"

type inputReadyContextKey struct{}

// InputReady returns the current invocation's queued-input signal, or nil when
// its Runtime does not provide one. The signal is closed while accepted input
// awaits a safe-point drain, including input queued before this call. After a
// drain, a new call returns a fresh signal. Observing it neither consumes input
// nor cancels the invocation; a waiting tool can return to let its Agent drain.
func InputReady(ctx context.Context) <-chan struct{} {
	observe, _ := ctx.Value(inputReadyContextKey{}).(func() <-chan struct{})
	if observe == nil {
		return nil
	}
	return observe()
}
