package gatewayapp

import "github.com/caelis-labs/caelis/agent-sdk/tool"

// Product presentation policy only. Runtime freezes bounded names/descriptions
// once per Session; tool discovery and execution retain their existing owners.
func selectedCodeModeTools(candidates []tool.Tool) []string {
	var names []string
	for _, one := range candidates {
		if one == nil {
			continue
		}
		def := one.Definition()
		if !tool.IsMCPDefinition(def) {
			continue
		}
		remote, _ := def.Metadata[tool.MetadataMCPTool].(string)
		server, _ := def.Metadata[tool.MetadataMCPServer].(string)
		docs := remote == "resolve-library-id" || remote == "query-docs" || remote == "get-library-docs"
		desktop := remote == "exec" && (server == "desktop-world" || server == "desktop_world")
		if docs || desktop {
			names = append(names, def.Name)
		}
	}
	return names
}
