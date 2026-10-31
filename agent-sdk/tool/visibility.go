package tool

import "github.com/caelis-labs/caelis/agent-sdk/model"

// ToolVisibility filters the assembled, fixed tools by model capabilities.
// MCP capabilities are called only through mcp_call and never join model tools.
type ToolVisibility struct {
	tools  []Tool
	byName map[string]Tool
}

func NewToolVisibility(tools []Tool) ToolVisibility { return NewToolVisibilityForModel(tools, nil) }
func NewToolVisibilityForModel(tools []Tool, llm model.LLM) ToolVisibility {
	v := ToolVisibility{byName: map[string]Tool{}}
	for _, one := range WithMCPCallTool(tools) {
		if one == nil {
			continue
		}
		def := one.Definition()
		if def.Name == "" || IsMCPDefinition(def) || (llm != nil && !AvailableForModel(one, llm)) {
			continue
		}
		v.tools = append(v.tools, one)
		v.byName[def.Name] = one
	}
	return v
}
func (v *ToolVisibility) LookupTool(name string) (Tool, bool) {
	if v == nil {
		return nil, false
	}
	one, ok := v.byName[name]
	return one, ok
}
func (v ToolVisibility) ModelSpecs() []model.ToolSpec {
	defs := make([]Definition, 0, len(v.tools))
	for _, one := range v.tools {
		defs = append(defs, one.Definition())
	}
	return modelSpecsFromDefinitions(defs)
}
