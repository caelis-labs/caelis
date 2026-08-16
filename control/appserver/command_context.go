package appserver

import "context"

type operationIntentContextKey struct{}

func withOperationIntent(ctx context.Context, intent OperationIntent) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, operationIntentContextKey{}, intent)
}

// OperationIntentFromContext returns the durable intent associated with the
// current CommandBackend invocation. It is backend evidence only; presentation
// callers must not synthesize or depend on this context value.
func OperationIntentFromContext(ctx context.Context) (OperationIntent, bool) {
	if ctx == nil {
		return OperationIntent{}, false
	}
	intent, ok := ctx.Value(operationIntentContextKey{}).(OperationIntent)
	return intent, ok
}
