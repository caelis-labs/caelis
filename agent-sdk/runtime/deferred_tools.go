package runtime

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// Deferred MCP tools pass through the same wrappers as static tools before a
// chat run can pin them. Sources cannot inject builtins through this path.
type deferredToolSource struct {
	source tool.Source
	wrap   func([]tool.Tool) []tool.Tool
}

func (s *deferredToolSource) Tools() []tool.Tool {
	var ready []tool.Tool
	var candidates []tool.Tool
	if source, ok := s.source.(interface{ SearchTools() []tool.Tool }); ok {
		candidates = source.SearchTools()
	} else {
		candidates = s.source.Tools()
	}
	for _, item := range candidates {
		if item != nil && tool.IsMCPDefinition(item.Definition()) {
			ready = append(ready, item)
		}
	}
	return s.wrap(ready)
}

// Lookup filters and wraps the current candidate through the ordinary chain.
// Tools-only embedders keep a linear fallback, not a second cached directory.
func (s *deferredToolSource) Lookup(ctx context.Context, name string) (tool.Tool, bool, error) {
	var target tool.Tool
	var def tool.Definition
	if named, ok := s.source.(tool.NamedSource); ok {
		one, found, err := named.Lookup(ctx, name)
		if err != nil || !found {
			return nil, false, err
		}
		target = one
		if target != nil {
			def = target.Definition()
		}
	} else {
		var candidates []tool.Tool
		if ready, ok := s.source.(interface{ SearchTools() []tool.Tool }); ok {
			candidates = ready.SearchTools()
		} else {
			candidates = s.source.Tools()
		}
		for _, one := range candidates {
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
			if one == nil {
				continue
			}
			candidate := one.Definition()
			if candidate.Name == name {
				target = one
				def = candidate
				break
			}
		}
	}
	if target == nil {
		return nil, false, nil
	}
	if def.Name != name || !tool.IsMCPDefinition(def) {
		return nil, false, nil
	}
	wrapped := s.wrap([]tool.Tool{target})
	if len(wrapped) != 1 {
		return nil, false, nil
	}
	return wrapped[0], true, nil
}
