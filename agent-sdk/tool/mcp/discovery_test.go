package mcp

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestSearchSchemaOmitsDefinitionMetadata(t *testing.T) {
	def := tool.Definition{Name: "target", Metadata: map[string]any{"fixture.private": true}, InputSchema: map[string]any{"type": "object", "required": []string{"path"}}}
	raw, err := json.Marshal(NewToolSearchResult([]tool.Definition{def}))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Tools []map[string]json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || len(result.Tools) != 1 || result.Tools[0]["metadata"] != nil || result.Tools[0]["fixture.private"] != nil || result.Tools[0]["input_schema"] == nil {
		t.Fatalf("%s %v", raw, err)
	}
}

func TestToolSearchProjectionOwnsSchemas(t *testing.T) {
	def := tool.Definition{
		Name: "docs__read", Description: " Read a document ",
		InputSchema:  map[string]any{"required": []string{"path"}},
		OutputSchema: map[string]any{"properties": map[string]any{"content": map[string]any{"type": "string"}}},
		Metadata: map[string]any{
			MetadataKind: KindTool, MetadataServer: "docs",
			MetadataTool: "read", MetadataSourceFingerprint: "v1",
		},
	}
	result := NewToolSearchResult([]tool.Definition{def})
	if result.Count != 1 || len(result.Tools) != 1 {
		t.Fatalf("unexpected discovery result: %+v", result)
	}
	entry := result.Tools[0]
	if entry.Name != def.Name || entry.Description != def.Description ||
		entry.Binding == nil || *entry.Binding != BindingForDefinition(def) {
		t.Fatalf("unexpected discovery entry: %+v", entry)
	}

	entry.InputSchema["required"].([]string)[0] = "other"
	if got := def.InputSchema["required"].([]string)[0]; got != "path" {
		t.Fatalf("discovery changed the canonical input schema: %q", got)
	}
	def.OutputSchema["properties"].(map[string]any)["content"].(map[string]any)["type"] = "number"
	if got := entry.OutputSchema["properties"].(map[string]any)["content"].(map[string]any)["type"]; got != "string" {
		t.Fatalf("canonical output schema changed the discovery result: %q", got)
	}
}

func TestParseToolSearchOutput(t *testing.T) {
	t.Parallel()

	result := ParseToolSearchOutput(map[string]any{
		"tools": []any{
			map[string]any{"name": "direct"},
			map[string]any{"function": map[string]any{"name": "nested"}},
			map[string]any{"name": "   "},
		},
	})
	if got, want := result.DiscoveredToolNames(), []string{"direct", "nested"}; !slices.Equal(got, want) {
		t.Fatalf("DiscoveredToolNames = %v, want %v", got, want)
	}

	malformed := ParseToolSearchOutput(map[string]any{"tools": "not-an-array"})
	if got := malformed.DiscoveredToolNames(); len(got) != 0 {
		t.Fatalf("malformed DiscoveredToolNames = %v, want empty", got)
	}
}
