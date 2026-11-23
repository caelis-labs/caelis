package chat

import (
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

func TestToolVisibilityKeepsOnlyDirectTools(t *testing.T) {
	t.Parallel()

	const searchName = mcp.SearchToolName
	search := tool.NamedTool{Def: tool.Definition{
		Name:     searchName,
		Metadata: map[string]any{mcp.MetadataKind: searchName},
	}}
	visible := newToolVisibility([]tool.Tool{
		tool.NamedTool{Def: tool.Definition{Name: "Read"}},
		nil,
		tool.NamedTool{},
		search,
		tool.NamedTool{Def: tool.Definition{Name: "calendar_create_event", Metadata: map[string]any{mcp.MetadataKind: mcp.KindTool}}},
	})
	var names []string
	for _, spec := range visible.modelSpecs() {
		names = append(names, spec.Function.Name)
	}
	if want := []string{"Read"}; !slices.Equal(names, want) {
		t.Fatalf("model tool names = %v, want %v", names, want)
	}
	for _, name := range names {
		if got, ok := visible.lookupTool(name); !ok || got.Definition().Name != name {
			t.Errorf("lookupTool(%q) = %v, %v; want matching tool", name, got, ok)
		}
	}
	for _, name := range []string{"", searchName, "calendar_create_event"} {
		if _, ok := visible.lookupTool(name); ok {
			t.Errorf("lookupTool(%q) succeeded for a hidden tool", name)
		}
	}

	search.Def.Metadata = nil
	visible = newToolVisibility([]tool.Tool{search})
	if got := visible.modelSpecs(); len(got) != 1 || got[0].Function.Name != searchName {
		t.Errorf("unmarked search model specs = %v, want one function named %q", got, searchName)
	}
	if _, ok := visible.lookupTool(searchName); !ok {
		t.Errorf("lookupTool rejected %q without search metadata", searchName)
	}
}
