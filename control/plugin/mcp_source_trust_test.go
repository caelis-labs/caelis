package plugin

import "testing"

func TestMCPSourceTrustUsesConfiguredDesktopWorldPluginIdentity(t *testing.T) {
	root := t.TempDir()
	writeAgentPluginFile(t, root, "plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"desktop-world","version":"1.0.0"}`)
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
					if spec.PluginID != id || spec.SourceTrusted != (id == "desktop-world") {
						t.Fatalf("manifest/server name overrode configured plugin trust: %+v", spec)
					}
				}
			}
		})
	}
}
