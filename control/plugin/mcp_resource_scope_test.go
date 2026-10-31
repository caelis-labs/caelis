package plugin

import (
	"maps"
	"slices"
	"testing"
)

func TestMCPResourceScopeUsesConfiguredDesktopWorldPluginIdentity(t *testing.T) {
	root := t.TempDir()
	writeAgentPluginFile(t, root, "plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"desktop-world","version":"0.2.0"}`)
	writeAgentPluginFile(t, root, "mcp.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"desktop-world":{"type":"stdio","command":"./bin/server"},"other-server":{"type":"stdio","command":"./bin/server"}}}`)
	writeAgentPluginFile(t, root, "bin/server", "launcher")
	for _, id := range []string{"desktop-world", "desktop_world", "other-plugin"} {
		t.Run(id, func(t *testing.T) {
			for _, resolve := range []func([]Config, ...RuntimePaths) (Contributions, error){ResolveContributions, ResolveAvailableContributions} {
				got, err := resolve([]Config{{ID: id, Root: root, Enabled: true}}, RuntimePaths{StoreDir: t.TempDir()})
				if err != nil || len(got.MCPServerSpecs) != 2 {
					t.Fatalf("contributions = %+v, %v", got, err)
				}
				for _, spec := range got.MCPServerSpecs {
					if spec.PluginID != id || spec.SourceTrusted {
						t.Fatalf("manifest/server name changed plugin identity or enabled blanket trust: %+v", spec)
					}
					if id == "desktop-world" {
						if !maps.Equal(spec.ToolResourceParameters, map[string]string{"exec": "app"}) || !slices.Equal(spec.SourceTrustedTools, []string{"apps", "api", "result"}) {
							t.Fatalf("configured DTW rules = %+v", spec)
						}
					} else if len(spec.ToolResourceParameters) != 0 || len(spec.SourceTrustedTools) != 0 {
						t.Fatalf("a different plugin inherited DTW rules: %+v", spec)
					}
				}
				if id == "desktop-world" {
					got.MCPServerSpecs[0].ToolResourceParameters["exec"] = "source"
					got.MCPServerSpecs[0].SourceTrustedTools[0] = "exec"
					if got.MCPServerSpecs[1].ToolResourceParameters["exec"] != "app" || got.MCPServerSpecs[1].SourceTrustedTools[0] != "apps" {
						t.Fatal("independent server rules share mutable state")
					}
				}
			}
		})
	}
}
