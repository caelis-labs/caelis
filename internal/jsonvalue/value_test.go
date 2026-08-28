package jsonvalue

import (
	"encoding/json"
	"testing"
)

func TestCloneMapAndMergeMapIsolateNestedValues(t *testing.T) {
	t.Parallel()

	base := map[string]any{
		"nested": map[string]any{"kept": true},
		"list":   []any{map[string]any{"value": "base"}},
	}
	merged := MergeMap(base, map[string]any{
		"nested": map[string]any{"added": true},
	})
	merged["nested"].(map[string]any)["kept"] = false
	merged["list"].([]any)[0].(map[string]any)["value"] = "changed"
	if base["nested"].(map[string]any)["kept"] != true ||
		base["list"].([]any)[0].(map[string]any)["value"] != "base" {
		t.Fatalf("merged map leaked mutation into base: %#v", base)
	}
	if merged["nested"].(map[string]any)["added"] != true {
		t.Fatalf("merged nested value missing: %#v", merged)
	}
}

func TestPathReadersPreserveCanonicalNumericRules(t *testing.T) {
	t.Parallel()

	values := map[string]any{"meta": map[string]any{
		"name":      "  RunCommand  ",
		"zero":      json.Number("0"),
		"fraction":  1.5,
		"too_large": float64(1 << 63),
		"string":    "12",
	}}
	if got := StringAt(values, "meta", "name"); got != "RunCommand" {
		t.Fatalf("StringAt() = %q, want RunCommand", got)
	}
	if got, ok := Int64At(values, "meta", "zero"); !ok || got != 0 {
		t.Fatalf("Int64At(zero) = %d, %v; want 0, true", got, ok)
	}
	for _, key := range []string{"fraction", "too_large", "string", "missing"} {
		if _, ok := Int64At(values, "meta", key); ok {
			t.Fatalf("Int64At(%s) accepted a non-canonical value", key)
		}
	}
}
