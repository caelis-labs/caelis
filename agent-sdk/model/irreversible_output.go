package model

import (
	"context"
	"sync/atomic"
)

type irreversibleOutputKey struct{}

// WithIrreversibleOutputTracking lets the stream owner revoke transport retry
// once a text fragment has crossed an append-only consumer boundary.
func WithIrreversibleOutputTracking(ctx context.Context) context.Context {
	return context.WithValue(ctx, irreversibleOutputKey{}, new(atomic.Bool))
}

// RecordIrreversibleOutput marks an accepted consumer handoff. It does not
// persist a fragment or establish model/tool execution authority.
func RecordIrreversibleOutput(ctx context.Context) {
	if value, _ := ctx.Value(irreversibleOutputKey{}).(*atomic.Bool); value != nil {
		value.Store(true)
	}
}
func hasIrreversibleOutput(ctx context.Context) bool {
	value, _ := ctx.Value(irreversibleOutputKey{}).(*atomic.Bool)
	return value != nil && value.Load()
}
