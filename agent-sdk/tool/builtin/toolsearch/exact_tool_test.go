package toolsearch

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestExactToolSelectorMatchesRawMetadataAndBinding(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"value"}, "properties": map[string]any{"value": map[string]any{"type": "integer"}}}
	selected := mcpCandidate("unrelated_canonical_name", "read", "plugin", "raw-server", "query-docs", schema)
	search := NewSource(rankingSource{
		selected,
		mcpCandidate("lookalike", "query-docs", "plugin", "raw-server", "Query-docs", schema),
		mcpCandidate("other_server", "query-docs", "plugin", "other", "query-docs", schema),
	}, serviceRejectRanker{t})
	page := servicePage(t, search, t.Context(), map[string]any{"server": "raw-server", "tool": "query-docs"})
	if page.Count != 1 || page.Truncated || page.Next != "" || page.OmittedCount != 0 {
		t.Fatalf("unexpected exact result: %+v", page)
	}
	entry := page.Tools[0]
	binding := tool.BindingForDefinition(selected.Definition())
	if entry.Name != selected.Definition().Name || entry.Binding == nil || *entry.Binding != binding || !reflect.DeepEqual(entry.InputSchema, schema) {
		t.Fatalf("exact selector lost canonical identity or complete schema: %+v", entry)
	}
}

func TestExactToolSelectorMissDoesNotRank(t *testing.T) {
	search := NewWithRanker([]tool.Tool{mcpCandidate("canonical", "missing", "plugin", "server", "read", nil)}, serviceRejectRanker{t})
	for _, input := range []string{
		`{"server":"server","tool":"missing"}`,
		`{"server":"missing","tool":"read"}`,
		`{"server":"server","tool":"read","plugin":"missing"}`,
	} {
		_, err := search.Call(t.Context(), tool.Call{Input: []byte(input)})
		if errorcode.CodeOf(err) != tool.ErrorCodeNotFound {
			t.Fatalf("%s: expected not_found, got %v", input, err)
		}
	}
}

func TestExactToolSelectorAmbiguityPrecedesPageLimit(t *testing.T) {
	var tools []tool.Tool
	for i := range 20 {
		remote := "other"
		if i == 0 || i == 19 {
			remote = "read"
		}
		tools = append(tools, mcpCandidate(fmt.Sprintf("tool_%02d", i), "read", fmt.Sprintf("plugin_%02d", i), "server", remote, nil))
	}
	search := NewWithRanker(tools, serviceRejectRanker{t})
	for _, limit := range []int{1, 16} {
		_, err := search.Call(t.Context(), tool.Call{Input: []byte(fmt.Sprintf(`{"server":"server","tool":"read","limit":%d}`, limit))})
		if errorcode.CodeOf(err) != tool.ErrorCodeTooManyMatches || !strings.Contains(err.Error(), "plugin or an exact name") {
			t.Fatalf("limit %d: expected actionable ambiguity, got %v", limit, err)
		}
	}
	page := servicePage(t, search, t.Context(), map[string]any{"server": "server", "tool": "read", "plugin": "plugin_19", "limit": 1})
	if page.Count != 1 || page.Tools[0].Name != "tool_19" || page.Truncated {
		t.Fatalf("qualified selector did not resolve ambiguity: %+v", page)
	}
}

func TestExactToolSelectorFiltersBeforeUniqueness(t *testing.T) {
	blocked := mcpCandidate("blocked", "read", "one", "server", "read", nil).Definition()
	blocked.RequiredModelCapabilities = model.Capabilities{ImageInput: true}
	search := NewSource(rankingSource{
		tool.NamedTool{Def: blocked},
		mcpCandidate("revoked", "read", "two", "server", "read", nil),
		mcpCandidate("eligible", "read", "three", "server", "read", nil),
	}, serviceRejectRanker{t})
	ctx := WithDiscoveryFilter(t.Context(), func(_ context.Context, def tool.Definition) (bool, error) {
		return def.Name != "revoked", nil
	})
	page := servicePage(t, search, ctx, map[string]any{"server": "server", "tool": "read"})
	if page.Count != 1 || page.Tools[0].Name != "eligible" {
		t.Fatalf("ineligible candidates affected uniqueness: %+v", page)
	}
}

func TestExactToolSelectorRejectsInvalidModes(t *testing.T) {
	search := NewWithRanker([]tool.Tool{mcpCandidate("canonical", "read", "plugin", "server", "read", nil)}, serviceRejectRanker{t})
	for _, input := range []string{
		`{"tool":"read"}`, `{"tool":"read","query":"read"}`, `{"tool":"read","name":"canonical"}`,
		`{"server":"server","tool":""}`, `{"server":"server","tool":null}`, `{"server":"server","tool":123}`,
		`{"server":"server","tool":" read"}`, `{"server":"server","tool":"read "}`,
		`{"server":"server","tool":"read","after":"canonical"}`, `{"server":"server","tool":"read","after":""}`,
		`{"server":"server","tool":"read","query":"read"}`, `{"server":"server","tool":"read","name":"canonical"}`,
		`{"server":"server","tool":"read","plugin":""}`,
		fmt.Sprintf(`{"server":"server","tool":%q}`, strings.Repeat("x", 1025)),
	} {
		if _, err := search.Call(t.Context(), tool.Call{Input: []byte(input)}); err == nil {
			t.Fatalf("accepted invalid exact selector: %s", input)
		}
	}
}

func TestExactToolSelectorRechecksSourceScope(t *testing.T) {
	source := &checkedSearchSource{stale: true}
	source.tools = []tool.Tool{mcpCandidate("canonical", "read", "plugin", "server", "read", nil)}
	if _, err := NewSource(source, serviceRejectRanker{t}).Call(t.Context(), tool.Call{Input: []byte(`{"server":"server","tool":"read"}`)}); err == nil {
		t.Fatal("exact selector accepted stale source scope")
	}
}

func TestExactToolSelectorOversizedSchemaFails(t *testing.T) {
	search := NewWithRanker([]tool.Tool{mcpCandidate("canonical", "read", "plugin", "server", "read", map[string]any{"type": "object", "description": strings.Repeat("schema", 30000)})}, serviceRejectRanker{t})
	_, err := search.Call(t.Context(), tool.Call{Input: []byte(`{"server":"server","tool":"read"}`)})
	if errorcode.CodeOf(err) != errorcode.ResourceExhausted {
		t.Fatalf("expected complete-schema budget failure, got %v", err)
	}
}
