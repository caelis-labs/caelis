package mcp

import (
	"fmt"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestManagerPublishesAllReadyToolsAcrossServersAndDiscovery(t *testing.T) {
	mgr := &Manager{warnings: make(map[string][]string)}
	for serverIndex := range 4 {
		serverName := fmt.Sprintf("s%02d", serverIndex)
		listed := make([]*mcpsdk.Tool, 300)
		for toolIndex := range listed {
			listed[toolIndex] = &mcpsdk.Tool{
				Name: fmt.Sprintf("item_%03d", toolIndex), Description: "Synthetic read-only fixture",
				InputSchema: map[string]any{"type": "object"},
			}
		}
		mgr.servers = append(mgr.servers, &managedServer{
			spec: ServerSpec{PluginID: "fixture", Name: serverName}, client: &Client{catalog: &clientCatalog{}}, catalogReady: true, listed: listed,
		})
	}
	mgr.rebuildToolsLocked()
	if got := len(mgr.Tools()); got != 1200 {
		t.Fatalf("Manager published %d ready tools, want 1200", got)
	}
	for _, warnings := range mgr.warnings {
		if len(warnings) != 0 {
			t.Fatalf("valid tools were quarantined: %v", warnings)
		}
	}
	if len(mgr.SearchTools()) != 1200 {
		t.Fatal("incomplete directory")
	}
}

func TestManagerMCPSourceFingerprintChangesWithConnectionAndSchema(t *testing.T) {
	definition := func(spec ServerSpec, schema map[string]any) string {
		mgr := &Manager{warnings: map[string][]string{}, servers: []*managedServer{{spec: spec, client: &Client{catalog: &clientCatalog{}}, catalogReady: true, listed: []*mcpsdk.Tool{{Name: "read", InputSchema: schema}}}}}
		mgr.rebuildToolsLocked()
		ready := mgr.Tools()
		if len(ready) != 1 {
			t.Fatalf("ready tools = %d", len(ready))
		}
		source, _ := ready[0].Definition().Metadata[MetadataSourceFingerprint].(string)
		if len(source) != 64 {
			t.Fatalf("source fingerprint = %q", source)
		}
		return source
	}
	base := ServerSpec{PluginID: "fixture", Name: "docs", Transport: TransportStreamableHTTP, URL: "https://mcp.test/v1"}
	first := definition(base, map[string]any{"type": "object"})
	for _, packaged := range []ServerSpec{
		{PluginID: base.PluginID, Name: base.Name, Transport: base.Transport, URL: base.URL, PackageRoot: "/verified/package"},
		{PluginID: base.PluginID, Name: base.Name, Transport: base.Transport, URL: base.URL, PackageVersion: "0.2.1"},
	} {
		if first == definition(packaged, map[string]any{"type": "object"}) {
			t.Fatal("native package provenance did not change source identity")
		}
	}
	changed := base
	changed.URL = "https://mcp.test/v2"
	if first == definition(changed, map[string]any{"type": "object"}) {
		t.Fatal("changed MCP connection retained source identity")
	}
	if first == definition(base, map[string]any{"type": "object", "properties": map[string]any{"key": map[string]any{"type": "string"}}}) {
		t.Fatal("changed MCP schema retained source identity")
	}
}
