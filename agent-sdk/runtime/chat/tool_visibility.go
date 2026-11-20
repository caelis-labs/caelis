package chat

import (
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

// toolVisibility keeps model declarations and direct lookup on the same fixed
// tool set. MCP capabilities stay behind the mcp_call declaration.
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
		if def.Name == "" || mcp.IsSearchDefinition(def) || mcp.IsDefinition(def) {
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
