package toolsearch

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type discoveryFilterKey struct{}

// WithDiscoveryFilter narrows discovery candidates before selection and budgets.
// The filter must check current source scope without retaining locks across tool
// I/O. It grants no execution permission and does not replace binding admission.
func WithDiscoveryFilter(ctx context.Context, filter func(context.Context, tool.Definition) (bool, error)) context.Context {
	return context.WithValue(ctx, discoveryFilterKey{}, filter)
}
