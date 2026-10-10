package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type fullCatalogRanker struct {
	t *testing.T
}

func (r fullCatalogRanker) Rank(_ context.Context, _ string, definitions []tool.Definition, _ int, _ toolsearch.SearchModel) ([]string, error) {
	r.t.Helper()
	if len(definitions) != 1200 {
		r.t.Fatalf("ToolSearch source catalog = %d, want all 1200 ready tools", len(definitions))
	}
	if definitions[0].Name != "s00__item_000" || definitions[len(definitions)-1].Name != "s03__item_299" {
		r.t.Fatalf("ToolSearch source lost first or last server: %q ... %q", definitions[0].Name, definitions[len(definitions)-1].Name)
	}
	return []string{"s03__item_299"}, nil
}

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
			spec: ServerSpec{PluginID: "fixture", Name: serverName}, client: &Client{}, listed: listed,
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
	search := toolsearch.NewSource(mgr, fullCatalogRanker{t: t})
	result, err := search.Call(t.Context(), tool.Call{Input: json.RawMessage(`{"query":"last synthetic item"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var output tool.ToolSearchResult
	if len(result.Content) != 1 || result.Content[0].JSON == nil || json.Unmarshal(result.Content[0].JSON.Value, &output) != nil || output.Count != 1 || output.Tools[0].Name != "s03__item_299" {
		t.Fatalf("ToolSearch discovery lost final server tool: %+v", output)
	}
}

func TestManagerMCPSourceFingerprintChangesWithConnectionAndSchema(t *testing.T) {
	definition := func(spec ServerSpec, schema map[string]any) string {
		mgr := &Manager{warnings: map[string][]string{}, servers: []*managedServer{{spec: spec, client: &Client{}, listed: []*mcpsdk.Tool{{Name: "read", InputSchema: schema}}}}}
		mgr.rebuildToolsLocked()
		ready := mgr.Tools()
		if len(ready) != 1 {
			t.Fatalf("ready tools = %d", len(ready))
		}
		source, _ := ready[0].Definition().Metadata[tool.MetadataMCPSourceFingerprint].(string)
		if len(source) != 64 {
			t.Fatalf("source fingerprint = %q", source)
		}
		return source
	}
	base := ServerSpec{PluginID: "fixture", Name: "docs", Transport: TransportStreamableHTTP, URL: "https://mcp.test/v1"}
	first := definition(base, map[string]any{"type": "object"})
	changed := base
	changed.URL = "https://mcp.test/v2"
	if first == definition(changed, map[string]any{"type": "object"}) {
		t.Fatal("changed MCP connection retained source identity")
	}
	if first == definition(base, map[string]any{"type": "object", "properties": map[string]any{"key": map[string]any{"type": "string"}}}) {
		t.Fatal("changed MCP schema retained source identity")
	}
}
