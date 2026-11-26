package sdkctx

import "context"

// MiddlewareKey carries the SDK-owned continuation. Extension contexts omit it.
type MiddlewareKey struct{}

type toolRepairKey struct{}

// WithToolRepair binds pre-dispatch repair to one Chat request. The model
// invocation owner rebinds it to the private provider snapshot after cloning.
func WithToolRepair[T any](ctx context.Context, request *T) context.Context {
	return context.WithValue(ctx, toolRepairKey{}, request)
}

// AllowsToolRepair reports whether Chat will validate this exact request's
// proposals before dispatch. Auxiliary and independently generated requests
// do not inherit the capability from a parent attempt.
func AllowsToolRepair[T any](ctx context.Context, request *T) bool {
	bound, _ := ctx.Value(toolRepairKey{}).(*T)
	return request != nil && bound == request
}
