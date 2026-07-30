package display

import (
	"reflect"
	"testing"
)

func TestHydrateToolSummaryOutputFillsOnlyMissingDisplayFields(t *testing.T) {
	output := map[string]any{
		"hits":      []any{},
		"count":     7,
		"truncated": false,
	}
	meta := runtimeToolMetaForTest(map[string]any{
		"pattern":    "needle",
		"count":      3,
		"file_count": 2,
		"diagnostic": "internal",
	})

	got := HydrateToolSummaryOutput("Grep", output, meta)
	want := map[string]any{
		"hits":       []any{},
		"pattern":    "needle",
		"count":      7,
		"file_count": 2,
		"truncated":  false,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("HydrateToolSummaryOutput() = %#v, want %#v", got, want)
	}
	if _, ok := output["pattern"]; ok {
		t.Fatalf("input output was mutated: %#v", output)
	}
}

func TestHydrateToolSummaryOutputHandlesEmptyAndUnknownTools(t *testing.T) {
	if got := HydrateToolSummaryOutput("Grep", nil, nil); got != nil {
		t.Fatalf("empty output = %#v, want nil", got)
	}
	output := map[string]any{"value": "kept"}
	got := HydrateToolSummaryOutput("third_party", output, runtimeToolMetaForTest(map[string]any{"count": 2}))
	if !reflect.DeepEqual(got, output) {
		t.Fatalf("unknown output = %#v, want %#v", got, output)
	}
}

func TestHydrateToolSummaryOutputUsesReadTraitsForViewImage(t *testing.T) {
	meta := runtimeToolMetaForTest(map[string]any{
		"path":       "/workspace/screens/pixel.png",
		"file_path":  "/workspace/screens/pixel.png",
		"diagnostic": "internal",
	})
	got := HydrateToolSummaryOutput("ViewImage", nil, meta)
	want := map[string]any{
		"path":      "/workspace/screens/pixel.png",
		"file_path": "/workspace/screens/pixel.png",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("HydrateToolSummaryOutput(ViewImage) = %#v, want %#v", got, want)
	}
}

func TestPluralizeHandlesSharedIrregularUnits(t *testing.T) {
	for _, test := range []struct {
		count int
		unit  string
		want  string
	}{
		{count: 1, unit: "entry", want: "1 entry"},
		{count: 2, unit: "entry", want: "2 entries"},
		{count: 2, unit: "match", want: "2 matches"},
		{count: 2, unit: "search", want: "2 searches"},
		{count: 2, unit: "file", want: "2 files"},
	} {
		if got := Pluralize(test.count, test.unit); got != test.want {
			t.Fatalf("Pluralize(%d, %q) = %q, want %q", test.count, test.unit, got, test.want)
		}
	}
}

func runtimeToolMetaForTest(values map[string]any) map[string]any {
	return map[string]any{
		"caelis": map[string]any{
			"runtime": map[string]any{
				"tool": values,
			},
		},
	}
}
