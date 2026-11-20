// Package invocationctx carries Runtime-issued tool occurrence data.
package invocationctx

import "context"

type ExecutionKey struct{}
type ParentKey struct{}

type occurrenceValue[T any] struct{ value T }

// With stores a Runtime-issued value in the private occurrence representation.
func With[T any](ctx context.Context, key any, value T) context.Context {
	return context.WithValue(ctx, key, occurrenceValue[T]{value})
}

// Read unwraps a Runtime-issued value. It ignores bare values from caller contexts.
func Read[T any](ctx context.Context, key any) T {
	value, _ := ctx.Value(key).(occurrenceValue[T])
	return value.value
}
