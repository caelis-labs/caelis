package runtime

import (
	"context"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type resultHookTool struct {
	base     tool.Tool
	concrete tool.Tool
}

func (t resultHookTool) RuntimeViewImage(toolbinding.Token) bool {
	return toolbinding.IsViewImage(t.concrete)
}
func (t resultHookTool) RuntimeMayCompleteTurn(_ toolbinding.Token, ctx context.Context, call tool.Call) bool {
	return toolbinding.MayCompleteTurn(ctx, t.concrete, call)
}

func (t resultHookTool) Definition() tool.Definition {
	return tool.CloneDefinition(t.base.Definition())
}
func (t resultHookTool) RuntimeTaskResultSource(toolbinding.Token) bool {
	return toolbinding.IsTaskResultSource(t.base)
}
func (t resultHookTool) RuntimePrepareInvocation(_ toolbinding.Token, c tool.Call) tool.InvocationContext {
	return toolbinding.Allocate(t.base, c)
}
func (t resultHookTool) Call(ctx context.Context, c tool.Call) (tool.Result, error) {
	return t.RuntimeCall(toolbinding.Token{}, ctx, c, toolbinding.Dispatch{})
}

func (t resultHookTool) RuntimeCall(_ toolbinding.Token, ctx context.Context, c tool.Call, dispatch toolbinding.Dispatch) (tool.Result, error) {
	var r tool.Result
	e := hookrun.FromContext(ctx).Evidence
	err := agent.TraceLifecycle(ctx, agent.LifecycleEvent{Operation: agent.LifecycleTool, SessionRef: e.SessionRef, RunID: e.RunID, TurnID: e.TurnID, StepID: c.ID, Name: c.Name}, hookrun.FromContext(ctx).Trace, func(ctx context.Context) error {
		var err error
		r, err = toolbinding.Call(ctx, t.base, c, dispatch)
		return err
	})
	return hookrun.FullResult(ctx, t.Definition(), c, r, err)
}

func wrapToolResults(concrete, tools []tool.Tool) []tool.Tool {
	out := make([]tool.Tool, 0, len(tools))
	// The wrapper chain preserves concrete registration order and drops nils.
	for _, one := range concrete {
		if one != nil {
			out = append(out, resultHookTool{base: tools[len(out)], concrete: one})
		}
	}
	return out
}
