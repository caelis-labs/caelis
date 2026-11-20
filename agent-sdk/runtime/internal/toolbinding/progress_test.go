package toolbinding

import (
	"context"
	"slices"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestProgressBelongsToActiveInvocation(t *testing.T) {
	var reports []string
	parent := WithProgress(t.Context(), func(result tool.Result) {
		reports = append(reports, "parent:"+result.ID)
	})
	child, cancel := context.WithCancel(WithProgress(parent, func(result tool.Result) {
		reports = append(reports, "child:"+result.ID)
	}))
	defer cancel()

	ReportProgress(t.Context(), tool.Result{ID: "unbound"})
	ReportProgress(WithProgress(parent, nil), tool.Result{ID: "masked"})
	ReportProgress(child, tool.Result{ID: "live"})
	if _, err := hookrun.Around(child, "ToolInvoke", "test progress isolation", 0,
		func(ctx context.Context, next func(context.Context) (tool.Result, error)) (tool.Result, error) {
			ReportProgress(ctx, tool.Result{ID: "hook"})
			return next(ctx)
		},
		func(ctx context.Context) (tool.Result, error) {
			ReportProgress(ctx, tool.Result{ID: "effect"})
			return tool.Result{}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	transform := agent.Hook[tool.Result, tool.Result]{Name: "test transform isolation", Apply: func(ctx context.Context, _ agent.HookContext, result tool.Result) (tool.Result, error) {
		ReportProgress(ctx, tool.Result{ID: "transform"})
		return result, nil
	}}
	identity := func(result tool.Result) tool.Result { return result }
	if _, err := hookrun.Call(child, "ToolResult", transform, agent.HookContext{}, tool.Result{}, identity, identity); err != nil {
		t.Fatal(err)
	}
	cancel()
	ReportProgress(child, tool.Result{ID: "late"})
	ReportProgress(parent, tool.Result{ID: "live"})

	if want := []string{"child:live", "child:effect", "parent:live"}; !slices.Equal(reports, want) {
		t.Fatalf("progress reports = %v, want %v", reports, want)
	}
}
