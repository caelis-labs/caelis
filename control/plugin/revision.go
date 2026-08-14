package plugin

import "context"

type expectedRevisionKey struct{}

// WithExpectedRevision binds the Host configuration revision a plugin mutation
// must observe. Hosts enforce it with cross-process configuration CAS.
func WithExpectedRevision(ctx context.Context, revision uint64) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, expectedRevisionKey{}, revision)
}

// ExpectedRevisionFromContext returns the revision bound by WithExpectedRevision.
func ExpectedRevisionFromContext(ctx context.Context) (uint64, bool) {
	if ctx == nil {
		return 0, false
	}
	revision, ok := ctx.Value(expectedRevisionKey{}).(uint64)
	return revision, ok
}
