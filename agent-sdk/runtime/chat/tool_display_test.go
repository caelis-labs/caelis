package chat

import (
	"reflect"
	"testing"
)

func TestSearchAndGlobSummariesUseInternalMetadata(t *testing.T) {
	meta := func(values map[string]any) map[string]any {
		return map[string]any{
			"caelis": map[string]any{
				"runtime": map[string]any{
					"tool": values,
				},
			},
		}
	}

	if got := globResultSummary(
		map[string]any{"pattern": "*.go"},
		map[string]any{"matches": []any{"a.go", "b.go"}, "truncated": false},
		meta(map[string]any{"count": 2}),
	); got != "*.go 2 matches" {
		t.Fatalf("globResultSummary() = %q, want %q", got, "*.go 2 matches")
	}

	if got := searchResultSummary(
		map[string]any{"pattern": "needle"},
		map[string]any{"hits": []any{}, "truncated": false},
		meta(map[string]any{"count": 3, "file_count": 2}),
	); got != `"needle" 3 hits` {
		t.Fatalf("searchResultSummary() = %q, want %q", got, `"needle" 3 hits`)
	}

	output := map[string]any{"hits": []any{}, "truncated": false}
	if got := toolResultDisplayOutput(
		"Grep",
		output,
		meta(map[string]any{"pattern": "needle", "count": 3, "file_count": 2}),
	); !reflect.DeepEqual(got, map[string]any{
		"hits":       []any{},
		"truncated":  false,
		"pattern":    "needle",
		"count":      3,
		"file_count": 2,
	}) {
		t.Fatalf("toolResultDisplayOutput() = %#v, want hydrated search summary fields", got)
	}
}
