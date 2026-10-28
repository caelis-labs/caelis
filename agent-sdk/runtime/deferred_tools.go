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
	for _, item := range s.source.Tools() {
		if item != nil && tool.IsMCPDefinition(item.Definition()) {
			ready = append(ready, item)
		}
	}
	return s.wrap(ready)
}

// CatalogRevision preserves an optional source invalidation seam through the
// Runtime's policy wrappers. Empty means the source supplies no revision token.
func (s *deferredToolSource) CatalogRevision() string {
	if source, ok := s.source.(interface{ CatalogRevision() string }); ok {
		return source.CatalogRevision()
	}
	return ""
}

// ValidateCatalog forwards the optional commit guard without creating a second
// catalog owner. Sources without that guard cannot validate a dynamic candidate.
func (s *deferredToolSource) ValidateCatalog(ctx context.Context, revision string) (func(), error) {
	if source, ok := s.source.(interface {
		ValidateCatalog(context.Context, string) (func(), error)
	}); ok {
		return source.ValidateCatalog(ctx, revision)
	}
	return nil, &CapabilityError{Capability: "tool.CatalogSource"}
}
