package toolsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type serviceRejectRanker struct{ t *testing.T }

func (r serviceRejectRanker) Rank(context.Context, string, []tool.Definition, int, SearchModel) ([]string, error) {
	r.t.Fatal("service discovery invoked the model selector")
	return nil, nil
}

func TestServiceSearchMatchesCanonicalMetadata(t *testing.T) {
	tools := []tool.Tool{
		mcpCandidate("z_first", "misleading description", "alpha", "service", "first", map[string]any{"type": "object"}),
		mcpCandidate("a_second", "unrelated", "beta", "service", "second", map[string]any{"type": "object", "required": []string{"value"}, "properties": map[string]any{"value": map[string]any{"type": "integer"}}}),
		mcpCandidate("service_lookalike", "service", "alpha", "service-other", "other", map[string]any{"type": "object"}),
		mcpCandidate("case_lookalike", "service", "alpha", "Service", "other", map[string]any{"type": "object"}),
	}
	search := NewWithRanker(tools, serviceRejectRanker{t})
	for _, tc := range []struct {
		input string
		names []string
	}{
		{`{"server":"service"}`, []string{"a_second", "z_first"}},
		{`{"server":"service","plugin":"alpha"}`, []string{"z_first"}},
		{`{"server":"service","plugin":"missing"}`, []string{}},
		{`{"server":"missing"}`, []string{}},
	} {
		t.Run(tc.input, func(t *testing.T) {
			result, err := search.Call(t.Context(), tool.Call{Input: []byte(tc.input)})
			if err != nil {
				t.Fatal(err)
			}
			var payload tool.ToolSearchResult
			if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
				t.Fatal(err)
			}
			names := make([]string, 0, len(payload.Tools))
			for _, entry := range payload.Tools {
				names = append(names, entry.Name)
				if entry.Binding == nil || entry.Binding.Identity == "" || entry.Binding.Version == "" || entry.InputSchema["type"] != "object" {
					t.Fatalf("incomplete discovery contract: %+v", entry)
				}
			}
			if !reflect.DeepEqual(names, tc.names) || payload.Count != len(names) || payload.Truncated || payload.OmittedCount != 0 {
				t.Fatalf("result = %+v, want names %v", payload, tc.names)
			}
		})
	}
}

func TestServiceSearchCountsLimitAndSchemaOmissions(t *testing.T) {
	for _, heavy := range []bool{false, true} {
		t.Run(fmt.Sprint(heavy), func(t *testing.T) {
			var tools []tool.Tool
			for i := 0; i < 20; i++ {
				schema := map[string]any{"type": "object"}
				if heavy {
					schema["description"] = strings.Repeat("complete schema ", 400)
				}
				tools = append(tools, mcpCandidate(fmt.Sprintf("tool_%02d", i), "read", "plugin", "server", fmt.Sprint(i), schema))
			}
			search := NewWithRanker(tools, serviceRejectRanker{t})
			result, err := search.Call(t.Context(), tool.Call{Input: []byte(`{"server":"server","limit":16}`)})
			if err != nil {
				t.Fatal(err)
			}
			var payload tool.ToolSearchResult
			if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Count == 0 || payload.Count > 16 || payload.Count+payload.OmittedCount != 20 || !payload.Truncated {
				t.Fatalf("missing omissions: %+v", payload)
			}
			if heavy && payload.Count == 16 {
				t.Fatal("large schemas did not exercise result budget")
			}
			if tool.EstimateToolSearchResultPromptTokens(payload) > tool.MaxToolSearchResultPromptTokens {
				t.Fatal("result exceeds schema budget")
			}
			for _, entry := range payload.Tools {
				if heavy && entry.InputSchema["description"] != strings.Repeat("complete schema ", 400) {
					t.Fatal("schema was shortened")
				}
			}
		})
	}
}

func TestServiceSearchRejectsInvalidScope(t *testing.T) {
	for _, input := range []string{
		`{"server":""}`, `{"server":" server"}`, `{"server":"server "}`,
		`{"plugin":"plugin"}`, `{"server":"server","plugin":""}`,
		`{"server":"server","plugin":" plugin"}`, `{"server":"server","query":"query"}`,
		`{"server":"server","name":"name"}`, `{"server":"server","limit":17}`,
		`{"server":"server","limit":0}`, `{"server":123}`,
	} {
		if _, err := parseRequest([]byte(input)); err == nil {
			t.Fatalf("accepted invalid service scope: %s", input)
		}
	}
}
