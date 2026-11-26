package sdkctx

import "context"

type ExecutionKey struct{}
type ParentKey struct{}

type occurrenceValue[T any] struct{ value T }

// WithOccurrence stores a Runtime-issued value in the private occurrence representation.
func WithOccurrence[T any](ctx context.Context, key any, value T) context.Context {
	return context.WithValue(ctx, key, occurrenceValue[T]{value})
}

// ReadOccurrence unwraps a Runtime-issued value. It ignores bare values from caller contexts.
func ReadOccurrence[T any](ctx context.Context, key any) T {
	value, _ := ctx.Value(key).(occurrenceValue[T])
	return value.value
}
