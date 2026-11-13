package toolsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func servicePage(t *testing.T, search tool.Tool, ctx context.Context, args map[string]any) tool.ToolSearchResult {
	t.Helper()
	raw, _ := json.Marshal(args)
	result, err := search.Call(ctx, tool.Call{Input: raw})
	if err != nil {
		t.Fatal(err)
	}
	var page tool.ToolSearchResult
	if err := json.Unmarshal(result.Content[0].JSON.Value, &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestServicePaginationIncludesEveryCompleteSchema(t *testing.T) {
	for _, heavy := range []bool{false, true} {
		t.Run(fmt.Sprint(heavy), func(t *testing.T) {
			var tools []tool.Tool
			for i := range 37 {
				schema := map[string]any{"type": "object"}
				if heavy {
					schema["description"] = strings.Repeat("complete schema ", 400)
				}
				tools = append(tools, mcpCandidate(fmt.Sprintf("tool_%02d", i), "read", "plugin", "server", fmt.Sprint(i), schema))
			}
			search := NewWithRanker(tools, serviceRejectRanker{t})
			args := map[string]any{"server": "server", "plugin": "plugin", "limit": 16}
			seen := map[string]bool{}
			pages := 0
			for {
				page := servicePage(t, search, t.Context(), args)
				pages++
				if page.Count == 0 || page.Count > 16 {
					t.Fatalf("non-progressing page: %+v", page)
				}
				for _, entry := range page.Tools {
					if seen[entry.Name] {
						t.Fatalf("repeated name %s", entry.Name)
					}
					seen[entry.Name] = true
					if heavy && entry.InputSchema["description"] != strings.Repeat("complete schema ", 400) {
						t.Fatal("partial schema")
					}
				}
				if page.Next == "" {
					if page.Truncated {
						t.Fatal("truncated page lacks continuation")
					}
					break
				}
				if page.Next != page.Tools[len(page.Tools)-1].Name || !page.Truncated {
					t.Fatalf("invalid continuation: %+v", page)
				}
				if page.Count+page.OmittedCount != 37-len(seen)+page.Count {
					t.Fatalf("wrong remaining count: %+v", page)
				}
				args["after"] = page.Next
				if pages > 37 {
					t.Fatal("pagination loop")
				}
			}
			if len(seen) != 37 || pages < 3 {
				t.Fatalf("seen=%d pages=%d", len(seen), pages)
			}
		})
	}
}

func TestServicePaginationFiltersBeforeLimit(t *testing.T) {
	var tools []tool.Tool
	for i := range 20 {
		one := mcpCandidate(fmt.Sprintf("tool_%02d", i), "read", "plugin", "server", fmt.Sprint(i), map[string]any{"type": "object"})
		if i < 16 {
			def := one.Definition()
			def.RequiredModelCapabilities = model.Capabilities{ImageInput: true}
			one = tool.NamedTool{Def: def}
		}
		tools = append(tools, one)
	}
	page := servicePage(t, NewWithRanker(tools, serviceRejectRanker{t}), t.Context(), map[string]any{"server": "server", "limit": 16})
	if page.Count != 4 || page.Truncated || page.Next != "" || page.OmittedCount != 0 {
		t.Fatalf("eligibility applied after budget: %+v", page)
	}
}

func TestServiceContinuationRechecksScope(t *testing.T) {
	source := &checkedSearchSource{}
	for i := range 4 {
		source.tools = append(source.tools, mcpCandidate(fmt.Sprintf("tool_%02d", i), "read", "plugin", "server", fmt.Sprint(i), map[string]any{"type": "object"}))
	}
	search := NewSource(source, serviceRejectRanker{t})
	page := servicePage(t, search, t.Context(), map[string]any{"server": "server", "plugin": "plugin", "limit": 2})
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		input string
	}{
		{"wrong plugin", t.Context(), fmt.Sprintf(`{"server":"server","plugin":"other","after":%q}`, page.Next)},
		{"wrong server", t.Context(), fmt.Sprintf(`{"server":"other","after":%q}`, page.Next)},
		{"invented anchor", t.Context(), `{"server":"server","after":"tool_00x"}`},
		{"revoked anchor", WithDiscoveryFilter(t.Context(), func(_ context.Context, def tool.Definition) (bool, error) { return def.Name != page.Next, nil }), fmt.Sprintf(`{"server":"server","after":%q}`, page.Next)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := search.Call(tc.ctx, tool.Call{Input: []byte(tc.input)}); err == nil {
				t.Fatal("accepted invalid continuation")
			}
		})
	}
	source.tools = append(source.tools[:1], source.tools[2:]...)
	input := []byte(fmt.Sprintf(`{"server":"server","after":%q}`, page.Next))
	if _, err := search.Call(t.Context(), tool.Call{Input: input}); err == nil {
		t.Fatal("accepted removed anchor")
	}
	source.stale = true
	if _, err := search.Call(t.Context(), tool.Call{Input: []byte(`{"server":"server","after":"tool_00"}`)}); err == nil {
		t.Fatal("accepted stale source")
	}
}

func TestServicePaginationOversizedSchemaFailsWithoutContinuation(t *testing.T) {
	search := NewWithRanker([]tool.Tool{mcpCandidate("huge", "read", "plugin", "server", "huge", map[string]any{"type": "object", "description": strings.Repeat("schema", 30000)})}, serviceRejectRanker{t})
	if _, err := search.Call(t.Context(), tool.Call{Input: []byte(`{"server":"server"}`)}); err == nil {
		t.Fatal("oversized schema produced an empty successful page")
	}
	for _, input := range []string{`{"after":"tool"}`, `{"server":"server","after":""}`, `{"server":"server","after":null}`, `{"server":"server","after":" tool"}`, `{"query":"read","after":"tool"}`} {
		if _, err := parseRequest([]byte(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}

func TestServiceEligibilityDoesNotInspectOtherPlugin(t *testing.T) {
	tools := []tool.Tool{
		mcpCandidate("a_other", "read", "other", "server", "a", map[string]any{"type": "object"}),
		mcpCandidate("b_target", "read", "target", "server", "b", map[string]any{"type": "object"}),
		mcpCandidate("c_other_server", "read", "target", "other", "c", map[string]any{"type": "object"}),
	}
	ctx := WithDiscoveryFilter(t.Context(), func(_ context.Context, def tool.Definition) (bool, error) {
		if def.Name != "b_target" {
			t.Fatalf("eligibility lookup escaped exact service scope: %s", def.Name)
		}
		return true, nil
	})
	page := servicePage(t, NewWithRanker(tools, serviceRejectRanker{t}), ctx, map[string]any{"server": "server", "plugin": "target"})
	if page.Count != 1 || page.Tools[0].Name != "b_target" {
		t.Fatalf("wrong scope: %+v", page)
	}
}
