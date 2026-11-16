package plugin

import (
	"fmt"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/caelis-labs/caelis/agent-sdk/skill/fs"
)

// productOverride validates the enabled Host package before comparing it with
// the embedded release. Explicit Session contributions have already won. A
// broken override is an error, never permission to use another source.
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
	if err == nil && builtinDesktopWorldTarget != "" && productSemver(out.Sources[0].Version) != "" {
		_, _, version, bundleErr := desktopWorldBundle()
		if bundleErr != nil {
			return Contributions{}, true, bundleErr
		}
		if newerProductVersion(version, out.Sources[0].Version) {
			out, err = ProductContributions(paths)
		}
	}
	return out, true, err
}

// Only complete semantic versions are ordered. Equal versions (including build
// metadata) and opaque versions preserve the enabled Host override contract.
func productSemver(version string) string {
	version = "v" + strings.TrimPrefix(strings.TrimSpace(version), "v")
	base, _, _ := strings.Cut(version, "-")
	base, _, _ = strings.Cut(base, "+")
	if strings.Count(base, ".") != 2 || !semver.IsValid(version) {
		return ""
	}
	return version
}

func newerProductVersion(candidate, current string) bool {
	a, b := productSemver(candidate), productSemver(current)
	return a != "" && b != "" && semver.Compare(a, b) > 0
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
	if root == "" {
		return fmt.Errorf("desktop-world requires one selected package root")
	}
	var service string
	for _, spec := range out.MCPServerSpecs {
		if spec.PluginID != "desktop-world" {
			continue
		}
		if spec.PackageRoot != root || (spec.Name != "dtw" && spec.Name != "desktop-world") || service != "" {
			return fmt.Errorf("desktop-world requires one dtw or desktop-world MCP service from the selected package")
		}
		service = spec.Name
	}
	metas, err := fs.DiscoverPluginBundleMeta(out.SkillBundles)
	if err != nil {
		return err
	}
	var skill string
	for _, meta := range metas {
		if meta.PluginID != "desktop-world" || (meta.LocalName != "dtw" && meta.LocalName != "desktop-world") {
			continue
		}
		if skill != "" || !PathWithinRoot(root, meta.Path) || meta.Name != "desktop-world:"+meta.LocalName {
			return fmt.Errorf("desktop-world requires one paired Skill from the selected package")
		}
		skill = meta.LocalName
	}
	if service != "" && skill == service {
		return nil
	}
	return fmt.Errorf("desktop-world requires a coherent dtw MCP/Skill pair or desktop-world MCP/Skill pair")
}
