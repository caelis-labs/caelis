package runner

import (
	"context"
	"fmt"

	"github.com/OnslaughtSnail/caelis/tool"
)

type toolExecutor struct {
	ctx   tool.Context
	tools map[string]tool.Tool
}

func newToolExecutor(ctx tool.Context, tools []tool.Tool) tool.Executor {
	byName := make(map[string]tool.Tool, len(tools))
	for _, one := range tools {
		if one == nil {
			continue
		}
		def := one.Definition()
		if def.Name == "" {
			continue
		}
		byName[def.Name] = one
	}
	return &toolExecutor{ctx: ctx, tools: byName}
}

func (e *toolExecutor) Execute(_ context.Context, call tool.Call) (tool.Result, error) {
	if e == nil {
		return tool.Result{Output: "tool executor unavailable", IsError: true}, nil
	}
	selected, ok := e.tools[call.Name]
	if !ok {
		return tool.Result{Output: fmt.Sprintf("tool not found: %s", call.Name), IsError: true}, nil
	}
	return selected.Run(e.ctx, call)
}
