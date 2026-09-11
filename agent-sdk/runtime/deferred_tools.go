package runtime

import "github.com/caelis-labs/caelis/agent-sdk/tool"

// Deferred MCP tools pass through the same wrappers as static tools before a
// chat run can pin them. Sources cannot inject builtins through this path.
type deferredToolSource struct {
	source tool.Source
	wrap   func([]tool.Tool) []tool.Tool
}

func (s *deferredToolSource) Tools() []tool.Tool {
	var ready []tool.Tool
	for _, item := range s.source.Tools() {
		if item != nil && tool.IsMCPDefinition(item.Definition()) {
			ready = append(ready, item)
		}
	}
	return s.wrap(ready)
}
