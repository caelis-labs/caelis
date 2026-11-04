package model

import (
	"context"
	"errors"
	"iter"
	"time"
)

// DefaultRequestTimeout bounds one actual generation, never retry waiting or a
// whole Agent Turn. Provider and caller budgets may impose shorter bounds.
const DefaultRequestTimeout = 10 * time.Minute

type requestAttemptTimeoutKey struct{}

// WithRequestAttemptTimeout supplies an owner's per-generation budget to
// Generate. Each attempt receives a fresh child context. Parent cancellation
// and deadlines remain authoritative; this does not extend authorization.
func WithRequestAttemptTimeout(ctx context.Context, timeout time.Duration) context.Context {
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	return context.WithValue(ctx, requestAttemptTimeoutKey{}, timeout)
}

func requestAttemptTimeout(ctx context.Context, llm LLM) time.Duration {
	if timeout, ok := ctx.Value(requestAttemptTimeoutKey{}).(time.Duration); ok {
		return timeout
	}
	if provider, ok := llm.(interface{ RequestTimeout() time.Duration }); ok && provider.RequestTimeout() > 0 {
		return provider.RequestTimeout()
	}
	return DefaultRequestTimeout
}

// RetryAfterError supplies a server's minimum delay before another request.
// The retry owner respects it even when it exceeds the local backoff cap.
type RetryAfterError interface {
	RetryAfter() time.Duration
}

// generateAttempt is the single actual-generation budget boundary, shared by
// accounted and unobserved calls. Retry wrappers never time their wait here.
func generateAttempt(parent context.Context, llm LLM, req *Request) iter.Seq2[*StreamEvent, error] {
	return func(yield func(*StreamEvent, error) bool) {
		ctx, cancel := context.WithTimeout(parent, requestAttemptTimeout(parent, llm))
		defer cancel()
		completed := false
		for event, err := range llm.Generate(ctx, req) {
			// Consumer processing runs inside yield. Once a complete response
			// arrived, its latency cannot turn success into an attempt timeout.
			if completed && parent.Err() == nil && ctx.Err() == context.DeadlineExceeded &&
				(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
				return
			}
			if err == nil && event != nil && event.Response != nil && isFinalResponse(event) {
				completed = true
			}
			if parent.Err() == nil && ctx.Err() == context.DeadlineExceeded &&
				(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
				err = context.DeadlineExceeded
			}
			if !yield(event, err) {
				return
			}
			if err != nil {
				return
			}
		}
		if err := parent.Err(); err != nil {
			yield(nil, err)
		} else if err := ctx.Err(); err != nil && !completed {
			yield(nil, err)
		}
	}
}
