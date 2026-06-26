package toolsearch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func TestNewReturnsNilWithoutMCPTools(t *testing.T) {
	t.Parallel()

	plain := tool.NamedTool{Def: tool.Definition{Name: "READ"}}
	if got := New([]tool.Tool{plain}); got != nil {
		t.Fatalf("New(non-MCP) = %#v, want nil", got)
	}
}

func TestToolSearchFindsDeferredMCPTools(t *testing.T) {
	t.Parallel()

	searchTool := New([]tool.Tool{
		mcpCandidate("mcp__calendar__demo__create_event", "Create calendar events", "calendar", "demo", "create_event", map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"start_time": map[string]any{"type": "string", "description": "Event start time"},
				"attendees":  map[string]any{"type": "string"},
			},
		}),
		mcpCandidate("mcp__drive__demo__read_file", "Read drive files", "drive", "demo", "read_file", map[string]any{
			"type": "object",
		}),
	})
	if searchTool == nil {
		t.Fatal("New(MCP tools) = nil")
	}
	def := searchTool.Definition()
	if def.Name != tool.ToolSearchToolName {
		t.Fatalf("Definition.Name = %q, want %q", def.Name, tool.ToolSearchToolName)
	}
	if !strings.Contains(def.Description, "calendar/demo") || !strings.Contains(def.Description, "drive/demo") {
		t.Fatalf("Definition.Description = %q, want source list", def.Description)
	}

	result, err := searchTool.Call(context.Background(), tool.Call{
		Name:  tool.ToolSearchToolName,
		Input: []byte(`{"query":"attendees event","limit":1}`),
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	var output map[string]any
	if err := json.Unmarshal(result.Content[0].JSON.Value, &output); err != nil {
		t.Fatalf("decode result JSON: %v", err)
	}
	payload := tool.ParseToolSearchOutput(output)
	if payload.Count != 1 || len(payload.Tools) != 1 {
		t.Fatalf("payload count/tools = %d/%d, want 1/1", payload.Count, len(payload.Tools))
	}
	got := payload.Tools[0]
	if got.Type != "function" || got.Name != "mcp__calendar__demo__create_event" || !got.DeferLoading {
		t.Fatalf("returned tool = %+v, want deferred calendar function", got)
	}
	if got.Source["plugin_id"] != "calendar" || got.Source["mcp_server"] != "demo" || got.Source["mcp_tool"] != "create_event" {
		t.Fatalf("source = %#v, want MCP provenance", got.Source)
	}
}

func mcpCandidate(name, description, pluginID, server, mcpTool string, schema map[string]any) tool.Tool {
	return tool.NamedTool{Def: tool.Definition{
		Name:        name,
		Description: description,
		InputSchema: schema,
		Metadata: map[string]any{
			tool.MetadataToolKind:  tool.MetadataToolKindMCP,
			tool.MetadataPluginID:  pluginID,
			tool.MetadataMCPServer: server,
			tool.MetadataMCPTool:   mcpTool,
		},
	}}
}
