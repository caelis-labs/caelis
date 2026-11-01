package gatewayapp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func TestFixedProductCapabilitiesSurviveOptionalEmptySelection(t *testing.T) {
	store := t.TempDir()
	for _, plugins := range [][]sessionprofile.Plugin{nil, {}} {
		got, err := applicationRuntimePluginContributions(sessionprofile.Profile{Plugins: plugins}, applicationPluginPaths(store, "native-session", t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Sources) != 1 || got.Sources[0].ID != "desktop-world" {
			t.Fatal("optional selection controlled the fixed product capability")
		}
	}
	inherited, err := resolveGatewayPluginContributions(nil, store, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(inherited.Sources) != 1 || inherited.Sources[0].ID != "desktop-world" {
		t.Fatal("ordinary Worker did not receive the product capability")
	}
}

func TestFixedProductIdentityShadowsUserPackageAndProxy(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(`{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"desktop-world","version":"0.1.3"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := applicationPluginPaths(t.TempDir(), "profile", t.TempDir())
	selected := sessionprofile.Profile{Plugins: []sessionprofile.Plugin{{Root: root, MCPServers: []sessionprofile.MCPServer{{Name: "desktop-world", Transport: "stdio", Command: "must-never-launch", WorkDir: root}}}}}
	got, err := applicationRuntimePluginContributions(selected, paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sources) != 1 || got.Sources[0].Root == root {
		t.Fatal("user package displaced or duplicated the fixed identity")
	}
	for _, spec := range got.MCPServerSpecs {
		if spec.Command == "must-never-launch" {
			t.Fatal("user proxy replaced the fixed launch")
		}
	}
	ordinary, err := resolveGatewayPluginContributions([]PluginConfig{{ID: "user-alias", Root: root, Enabled: true}}, paths.ProductStoreDir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(ordinary.Sources) != 1 || ordinary.Sources[0].Root == root {
		t.Fatal("ordinary source alias escaped fixed identity ownership")
	}
}

func TestFixedProductNamespaceCannotBeShadowedByOrdinaryMCP(t *testing.T) {
	for _, name := range []string{"desktop-world", "desktop_world", "desktop-world__extra"} {
		_, err := mergeRuntimeMCPSpecs([]mcp.ServerSpec{{PluginID: "user", Name: name}}, nil)
		if err == nil {
			t.Fatalf("ordinary MCP source %q shadowed the fixed tool namespace", name)
		}
	}
}
