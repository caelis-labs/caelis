package chat

import (
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

// toolVisibility keeps model declarations and direct lookup on the same fixed
// tool set. Direct MCP functions require explicit embedder selection.
type toolVisibility struct {
	tools  []tool.Tool
	byName map[string]tool.Tool
}

func newToolVisibility(tools []tool.Tool) toolVisibility {
	v := toolVisibility{byName: make(map[string]tool.Tool)}
	for _, one := range tools {
		if one == nil {
			continue
		}
		def := one.Definition()
		if def.Name == "" || mcp.IsSearchDefinition(def) || (mcp.IsDefinition(def) && !mcp.IsModelFunction(def)) {
			continue
		}
		v.tools = append(v.tools, one)
		v.byName[def.Name] = one
	}
	return v
}

func (v *toolVisibility) lookupTool(name string) (tool.Tool, bool) {
	one, ok := v.byName[name]
	return one, ok
}

func (v toolVisibility) modelSpecs() []model.ToolSpec {
	return tool.ModelSpecs(v.tools)
}

func (a *Agent) lookupModelTool(name string, visibility *toolVisibility) (tool.Tool, *tool.Binding, error) {
	target, ok := a.lookupRunTool(name, visibility)
	if !ok {
		return nil, nil, tool.NewError(tool.ErrorCodeNotFound, fmt.Sprintf("tool %q not found", name))
	}
	def := target.Definition()
	if mcp.IsModelFunction(def) {
		binding := mcp.BindingForDefinition(def)
		if binding.Identity == "" {
			return nil, nil, tool.NewError(tool.ErrorCodeStaleRevision, "MCP source identity is incomplete")
		}
		return target, &binding, nil
	}
	return target, nil, nil
}
