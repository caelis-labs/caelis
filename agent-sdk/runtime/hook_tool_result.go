package runtime

import (
	"context"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type resultHookTool struct{ base tool.Tool }

func (t resultHookTool) Definition() tool.Definition {
	return tool.CloneDefinition(t.base.Definition())
}
func (t resultHookTool) RuntimeTaskResultSource(toolbinding.Token) bool {
	return toolbinding.IsTaskResultSource(t.base)
}
func (t resultHookTool) RuntimePrepareInvocation(_ toolbinding.Token, c tool.Call) tool.Call {
	return toolbinding.Prepare(t.base, c)
}
func (t resultHookTool) Call(ctx context.Context, c tool.Call) (tool.Result, error) {
	var r tool.Result
	e := hookrun.FromContext(ctx).Evidence
	err := agent.TraceLifecycle(ctx, agent.LifecycleEvent{Operation: agent.LifecycleTool, SessionRef: e.SessionRef, RunID: e.RunID, TurnID: e.TurnID, StepID: c.ID, Name: c.Name}, hookrun.FromContext(ctx).Trace, func(ctx context.Context) error { var err error; r, err = t.base.Call(ctx, c); return err })
	return hookrun.FullResult(ctx, t.Definition(), c, r, err)
}

func wrapToolResults(tools []tool.Tool) []tool.Tool {
	out := make([]tool.Tool, 0, len(tools))
	for _, t := range tools {
		if t != nil {
			out = append(out, resultHookTool{base: t})
		}
	}
	return out
}
