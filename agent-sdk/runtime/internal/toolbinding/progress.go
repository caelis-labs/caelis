package toolbinding

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type progressKey struct{}

// Progress is one transient Runtime observation.
type Progress struct {
	Result    tool.Result
	EventMeta map[string]any
}

// WithProgress binds transient reporting to a tool invocation. A nil report
// masks an enclosing invocation's reporter.
func WithProgress(ctx context.Context, report func(Progress)) context.Context {
	return context.WithValue(ctx, progressKey{}, report)
}

// ReportProgress publishes a transient result if the invocation still accepts it.
// Only Tool.Call's returned result can become a final tool fact.
func ReportProgress(ctx context.Context, result tool.Result) {
	if report, _ := ctx.Value(progressKey{}).(func(Progress)); report != nil && ctx.Err() == nil {
		report(Progress{Result: result, EventMeta: Observed(ctx).EventMeta()})
	}
}
