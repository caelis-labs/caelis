package runtime

import (
	"context"
	"sync"
)

type toolCallGatesKey struct{}

// toolCallGates belongs to one native Run, including its yielded producers and
// model snapshot replacements. Canonical names keep the same gate across schema
// changes; an old call can still be using the same underlying instance.
// Different Runs retain their independent execution owners.
type toolCallGates struct {
	mu    sync.Mutex
	gates map[string]chan struct{}
}

func (g *toolCallGates) acquire(ctx context.Context, name string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g.mu.Lock()
	if g.gates == nil {
		g.gates = make(map[string]chan struct{})
	}
	gate := g.gates[name]
	if gate == nil {
		gate = make(chan struct{}, 1)
		g.gates[name] = gate
	}
	g.mu.Unlock()

	select {
	case gate <- struct{}{}:
		release := func() { <-gate }
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		return release, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
