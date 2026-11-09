package tool

import (
	"encoding/json"
	"testing"
)

func TestSearchSchemaReportsActualParallelSafety(t *testing.T) {
	for _, safe := range []bool{false, true} {
		def := Definition{Name: "target", Capabilities: Capabilities{ParallelSafe: safe}, InputSchema: map[string]any{"type": "object", "required": []string{"path"}}}
		raw, err := json.Marshal(NewToolSearchResult([]Definition{def}))
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Tools []struct {
				ParallelSafe bool           `json:"parallel_safe"`
				Schema       map[string]any `json:"input_schema"`
			}
		}
		if err = json.Unmarshal(raw, &result); err != nil || len(result.Tools) != 1 || result.Tools[0].ParallelSafe != safe || result.Tools[0].Schema["required"] == nil {
			t.Fatalf("%s %v", raw, err)
		}
	}
}
