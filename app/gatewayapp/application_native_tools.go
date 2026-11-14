package gatewayapp

import (
	"context"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func newApplicationNativeTools(exec *applicationExecutionRuntime, store *sessionprofile.Store, binding sessionprofile.Binding, workspace string, nativeTools []string) ([]tool.Tool, error) {
	if exec == nil || store == nil {
		return nil, fmt.Errorf("gatewayapp: application native execution is unavailable")
	}
	if nativeTools == nil {
		nativeTools = sessionprofile.NativeToolNames()
	}
	selected := make(map[string]bool, len(nativeTools))
	for _, name := range nativeTools {
		selected[name] = true
	}
	all, err := builtin.BuildCoreTools(builtin.CoreToolsConfig{Runtime: exec})
	if err != nil {
		return nil, err
	}
	result := make([]tool.Tool, 0, len(selected)+2)
	for _, one := range all {
		if selected[one.Definition().Name] {
			// In particular RunCommand stays the concrete SDK shell tool: the
			// Runtime uses its type to bind asynchronous Task and approval state.
			result = append(result, one)
		}
	}
	for _, resource := range newApplicationResourceTools(store, binding, workspace) {
		// Resource transfer uses Host filesystem calls, not the SDK sandbox
		// filesystem. Recheck the lease and directory bindings before any
		// materialization or publication, including after approval resumes.
		result = append(result, applicationBoundResourceTool{Tool: resource, runtime: exec})
	}
	return result, nil
}

type applicationBoundResourceTool struct {
	tool.Tool
	runtime *applicationExecutionRuntime
}

func (t applicationBoundResourceTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := t.runtime.checkActive(ctx); err != nil {
		return tool.Result{}, err
	}
	return t.Tool.Call(ctx, call)
}
