package gatewayapp

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/skill"
	skillfs "github.com/caelis-labs/caelis/agent-sdk/skill/fs"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/plugin"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

// Explicit profiles select native packages independently of Host registrations.
// The ordinary Control parser remains the sole owner of package identity and
// policy. Host proxies change only launch, never package identity or policy.
func applicationPluginContributions(profile sessionprofile.Profile, paths plugin.RuntimePaths) (plugin.Contributions, error) {
	var out plugin.Contributions
	seen := map[string]bool{}
	for _, selected := range profile.Plugins {
		canonical, err := filepath.EvalSymlinks(selected.Root)
		if err != nil || canonical != selected.Root {
			return out, fmt.Errorf("native plugin root %q must remain its canonical package directory", selected.Root)
		}
		contributions, err := plugin.ResolveContributions([]plugin.Config{{Root: selected.Root, Enabled: true}}, paths)
		if err != nil {
			return out, err
		}
		source := contributions.Sources[0]
		if source.Root != selected.Root {
			return out, fmt.Errorf("native plugin root changed during assembly")
		}
		if source.ID == "application" || seen[source.ID] {
			return out, fmt.Errorf("duplicate or reserved native plugin identity %q", source.ID)
		}
		seen[source.ID] = true
		for _, plain := range profile.MCPServers {
			if plain.WorkDir != "" && plugin.PathWithinRoot(source.Root, plain.WorkDir) {
				return out, fmt.Errorf("plugin %q also selected as an ordinary MCP service", source.ID)
			}
		}
		for _, proxy := range selected.MCPServers {
			found := false
			for i := range contributions.MCPServerSpecs {
				spec := &contributions.MCPServerSpecs[i]
				if spec.Name != proxy.Name {
					continue
				}
				found = true
				spec.Transport, spec.Command = proxy.Transport, proxy.Command
				spec.Args, spec.WorkDir = append([]string(nil), proxy.Args...), proxy.WorkDir
				spec.URL, spec.Headers, spec.Env, spec.DataDir = "", nil, nil, ""
				spec.CleanEnvironment = true
			}
			if !found {
				return out, fmt.Errorf("native plugin %q does not declare proxy server %q", source.ID, proxy.Name)
			}
		}
		out.Sources = append(out.Sources, source)
		out.SkillBundles = append(out.SkillBundles, contributions.SkillBundles...)
		out.MCPServerSpecs = append(out.MCPServerSpecs, contributions.MCPServerSpecs...)
	}
	// Explicit Session selection has no priority fallback between two sources
	// with the same projected namespace. Reject ambiguity before starting either.
	if len(profile.Plugins) != 0 {
		if err := validateApplicationMCPNamespaces(profile, out); err != nil {
			return out, err
		}
	}
	return out, nil
}

func validateApplicationMCPNamespaces(profile sessionprofile.Profile, out plugin.Contributions) error {
	specs, err := mergeRuntimeMCPSpecs(applicationMCPSpecs(profile), out.MCPServerSpecs)
	if err != nil {
		return err
	}
	for i, spec := range specs {
		prefix := mcp.ServerToolNamePrefix(spec.Name)
		for _, prior := range specs[:i] {
			other := mcp.ServerToolNamePrefix(prior.Name)
			if strings.HasPrefix(prefix, other) || strings.HasPrefix(other, prefix) {
				return fmt.Errorf("explicit MCP sources have overlapping namespaces: %q and %q", spec.Name, prior.Name)
			}
		}
	}
	return nil
}

func applicationPluginPaths(storeDir, sessionID, workspace string) plugin.RuntimePaths {
	// Native profile extensions never share PLUGIN_DATA with standalone Host
	// registrations or another Session. Package files remain externally owned.
	return plugin.RuntimePaths{ProductStoreDir: storeDir, StoreDir: filepath.Join(storeDir, "session-plugins", fmt.Sprintf("%x", sha256.Sum256([]byte(sessionID)))), WorkspaceDir: workspace}
}

// Explicit package selection has no Skill overlay priority. Scan through the
// existing SDK parser, but reject colliding canonical names before deduplication.
func applicationPluginSkills(bundles []skill.PluginBundle) ([]skill.Meta, error) {
	var out []skill.Meta
	seen := map[string]bool{}
	for _, bundle := range bundles {
		metas, err := skillfs.DiscoverPluginBundleMeta([]skill.PluginBundle{bundle})
		if err != nil {
			return nil, err
		}
		for _, meta := range metas {
			key := strings.ToLower(meta.Name)
			if seen[key] {
				return nil, fmt.Errorf("native plugin Skill name %q is duplicated", meta.Name)
			}
			seen[key] = true
			out = append(out, meta)
		}
	}
	return out, nil
}

// Runtime product capabilities do not belong to the optional Profile plugin list.
func applicationRuntimePluginContributions(profile sessionprofile.Profile, paths plugin.RuntimePaths) (plugin.Contributions, error) {
	selected := profile
	selected.Plugins = optionalApplicationPlugins(profile.Plugins)
	optional, err := applicationPluginContributions(selected, paths)
	if err != nil {
		return plugin.Contributions{}, err
	}
	combined, err := plugin.WithProductContributions(optional, paths)
	if err != nil {
		return combined, err
	}
	if len(selected.Plugins) != len(profile.Plugins) {
		combined.Sources[0].Status += "; user desktop-world package omitted"
	}
	if err := validateApplicationMCPNamespaces(profile, combined); err != nil {
		return plugin.Contributions{}, err
	}
	return combined, nil
}

func pluginSourceStatus(source plugin.Source) string {
	if source.Status != "" {
		return source.Status
	}
	return "assembled"
}

func optionalApplicationPlugins(configured []sessionprofile.Plugin) []sessionprofile.Plugin {
	var selected []sessionprofile.Plugin
	for _, entry := range configured {
		if !plugin.IsProductPlugin(plugin.Config{Root: entry.Root}) {
			selected = append(selected, entry)
		}
	}
	return selected
}
