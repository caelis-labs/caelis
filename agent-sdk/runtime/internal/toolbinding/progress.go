package toolbinding

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type progressKey struct{}

// WithProgress binds transient reporting to a tool invocation. A nil report
// masks an enclosing invocation's reporter.
func WithProgress(ctx context.Context, report func(tool.Result)) context.Context {
	return context.WithValue(ctx, progressKey{}, report)
}

// ReportProgress publishes a transient result if the invocation still accepts it.
// Only Tool.Call's returned result can become a final tool fact.
func ReportProgress(ctx context.Context, result tool.Result) {
	if report, _ := ctx.Value(progressKey{}).(func(tool.Result)); report != nil && ctx.Err() == nil {
		report(result)
	}
}
