package mcpdiscovery

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestServiceAdmissionDoesNotSkipSchemasThatExceedBudget(t *testing.T) {
	def := tool.Definition{
		Name: "docs__read", InputSchema: map[string]any{"type": "object"},
		Metadata: map[string]any{
			tool.MetadataToolKind:             tool.MetadataToolKindMCP,
			tool.MetadataMCPTool:              "read",
			tool.MetadataMCPServer:            "docs",
			tool.MetadataMCPSourceFingerprint: "version",
		},
	}
	selected := tool.NewToolSearchResult([]tool.Definition{def})
	selected.Truncated, selected.Next, selected.OmittedCount = true, def.Name, 1
	def.Description = strings.Repeat("schema", tool.MaxToolSearchResultPromptTokens)
	current := map[string]tool.Tool{def.Name: tool.NamedTool{Def: def}}
	if _, err := Admit(selected, current, tool.Result{}, json.RawMessage(`{"server":"docs"}`)); !errorcode.Is(err, errorcode.ResourceExhausted) {
		t.Fatalf("service admission error=%v, want resource_exhausted", err)
	}

	// Ordinary query results retain their existing bounded omission contract.
	selected.Next = ""
	result, err := Admit(selected, current, tool.Result{}, json.RawMessage(`{"query":"read"}`))
	if err != nil {
		t.Fatal(err)
	}
	var admitted tool.ToolSearchResult
	if err := json.Unmarshal(result.Content[0].JSON.Value, &admitted); err != nil {
		t.Fatal(err)
	}
	if admitted.Count != 0 || !admitted.Truncated || admitted.OmittedCount != 2 {
		t.Fatalf("query omission behavior changed: %+v", admitted)
	}
}
