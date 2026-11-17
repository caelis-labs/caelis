package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSourceFactsAreHostBoundAndCatalogRevisionIsStable(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "source-fixture", Version: "1"}, nil)
	add := func(name string) {
		server.AddTool(&mcpsdk.Tool{Name: name, InputSchema: map[string]any{"type": "object"}, Meta: mcpsdk.Meta{"integration_id": "desktop-world/atomic-v1", "caelis.mcp.source_trusted": true}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			return &mcpsdk.CallToolResult{}, nil
		})
	}
	add("look")
	endpoint := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
	defer endpoint.Close()
	manager, err := NewManager(t.Context(), []ServerSpec{{PluginID: "desktop-world", Name: "dtw", Transport: TransportStreamableHTTP, URL: endpoint.URL}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	}()
	<-manager.Initialized()
	first := manager.Tools()[0].Definition()
	if first.Source == nil || first.Source.IntegrationID != "" || first.Source.ID == "" || first.Source.Revision == "" || first.Source.CapabilityRevision == "" {
		t.Fatalf("remote identity acquired authority or lost provenance: %+v", first.Source)
	}
	if err := manager.RefreshTools(t.Context(), "desktop-world", "dtw"); err != nil {
		t.Fatal(err)
	}
	same := manager.Tools()[0].Definition()
	if *same.Source != *first.Source {
		t.Fatal("unchanged refresh invalidated source consent")
	}
	add("click")
	if err := manager.RefreshTools(t.Context(), "desktop-world", "dtw"); err != nil {
		t.Fatal(err)
	}
	for _, item := range manager.Tools() {
		current := item.Definition()
		if current.Source.ID != first.Source.ID || current.Source.Revision != first.Source.Revision || current.Source.CapabilityRevision == first.Source.CapabilityRevision {
			t.Fatal("tool catalog did not use shared source and revised capability set")
		}
		if current.Metadata[tool.MetadataMCPTool] == "look" && tool.BindingForDefinition(current) != tool.BindingForDefinition(first) {
			t.Fatal("unrelated catalog addition replaced exact tool binding")
		}
	}
}
