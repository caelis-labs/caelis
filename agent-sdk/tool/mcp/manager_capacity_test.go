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
