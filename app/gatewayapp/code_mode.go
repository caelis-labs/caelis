package gatewayapp

import (
	"context"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

func codeModeHintsForSession(ctx context.Context, sessions session.Service, ref session.SessionRef, source tool.Source) ([]string, error) {
	state, err := sessions.SnapshotState(ctx, ref)
	if err != nil {
		return nil, err
	}
	if _, frozen := state[codemode.MetadataCatalog].(string); frozen || source == nil {
		return nil, nil
	}
	return selectedCodeModeTools(source.Tools()), nil
}

// Product presentation policy only. Runtime freezes bounded names/descriptions
// once per Session; tool discovery and execution retain their existing owners.
func selectedCodeModeTools(candidates []tool.Tool) []string {
	var names []string
	for i, one := range candidates {
		// Hints are partial presentation, never a catalog search. Bound the
		// first snapshot's definition work even for very large directories.
		if i >= 256 || len(names) >= 6 {
			break
		}
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
