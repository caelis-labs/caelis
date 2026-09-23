package gatewayapp

import (
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin"
	"github.com/caelis-labs/caelis/control/application"
)

// applicationNativeToolNames is the ordinary SDK filesystem/command catalog,
// excluding product-global Skill, web, and Plan tools. Its order is stable in
// model requests; resource transfer remains the existing scoped bridge.
var applicationNativeToolNames = []string{
	"Read", "ViewImage", "Write", "Patch", "Glob", "Grep", "RunCommand", "Task",
}

func newApplicationNativeTools(exec *applicationExecutionRuntime, store *application.Store, binding application.Binding, workspace string, nativeTools []string) ([]tool.Tool, error) {
	if exec == nil || store == nil {
		return nil, fmt.Errorf("gatewayapp: application native execution is unavailable")
	}
	selected := make(map[string]bool, len(applicationNativeToolNames))
	if nativeTools == nil {
		for _, name := range applicationNativeToolNames {
			selected[name] = true
		}
	} else {
		for _, name := range nativeTools {
			selected[name] = true
		}
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
		// filesystem. A pending approval must recheck the live lease before any
		// materialization or publication after it resumes.
		result = append(result, applicationLeasedTool{Tool: resource, store: store, scope: binding.Scope})
	}
	return result, nil
}
