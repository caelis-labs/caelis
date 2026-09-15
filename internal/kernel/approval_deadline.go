package kernel

import (
	"context"
	"time"
)

// AutoReviewTimeout bounds one automatic approval, including both Control and
// reviewer queues. Evidence and format repair share this deadline.
const AutoReviewTimeout = 90 * time.Second

type autoReviewStartKey struct{}

// WithAutoReviewBudget starts the product approval clock once. Calling it again
// at the reviewer boundary preserves the original start and caller deadline.
func WithAutoReviewBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Value(autoReviewStartKey{}).(time.Time); ok {
		return context.WithCancel(ctx)
	}
	ctx = context.WithValue(ctx, autoReviewStartKey{}, time.Now())
	return context.WithTimeout(ctx, AutoReviewTimeout)
}

// AutoReviewStarted returns the admission time for end-to-end latency metrics.
func AutoReviewStarted(ctx context.Context) time.Time {
	started, ok := ctx.Value(autoReviewStartKey{}).(time.Time)
	if !ok {
		return time.Now()
	}
	return started
}
