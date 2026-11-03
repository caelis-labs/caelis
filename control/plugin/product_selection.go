package plugin

import (
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/skill/fs"
)

// productOverride resolves only the fixed capability from the Host snapshot.
// Explicit Session contributions have already won before this is called. A
// broken selected override is an error, never permission to use another source.
func productOverride(configs []Config, paths RuntimePaths) (Contributions, bool, error) {
	var selected *Config
	for _, configured := range configs {
		if !configured.Enabled || !IsProductPlugin(configured) {
			continue
		}
		if selected != nil {
			return Contributions{}, true, fmt.Errorf("duplicate desktop-world override sources")
		}
		selected = &configured
	}
	if selected == nil {
		return Contributions{}, false, nil
	}
	installed, err := ParsePlugin(selected.Root)
	if err != nil {
		return Contributions{}, true, fmt.Errorf("desktop-world override: %w", err)
	}
	if localPluginID(installed) != "desktop-world" {
		return Contributions{}, true, fmt.Errorf("desktop-world override has a different manifest identity")
	}
	configured := *selected
	configured.ID = "desktop-world"
	out, err := ResolveContributions([]Config{configured}, paths)
	if err == nil {
		err = validateProductPair(out)
	}
	if err == nil {
		out.Sources[0].Status = "override"
	}
	return out, true, err
}

func validateProductPair(out Contributions) error {
	var root string
	for _, source := range out.Sources {
		if source.ID == "desktop-world" {
			if root != "" {
				return fmt.Errorf("duplicate desktop-world package contributions")
			}
			root = source.Root
		}
	}
	server := false
	for _, spec := range out.MCPServerSpecs {
		if spec.PluginID == "desktop-world" && spec.PackageRoot == root {
			server = true
		}
	}
	metas, err := fs.DiscoverPluginBundleMeta(out.SkillBundles)
	if err != nil {
		return err
	}
	for _, meta := range metas {
		if server && meta.PluginID == "desktop-world" && meta.LocalName == "desktop-world" && PathWithinRoot(root, meta.Path) {
			return nil
		}
	}
	return fmt.Errorf("desktop-world requires its paired MCP service and desktop-world Skill")
}
