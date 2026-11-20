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
