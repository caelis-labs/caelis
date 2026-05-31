package tool

import "testing"

func TestRuntimeToolMetaRoundTripsUnderCanonicalNamespace(t *testing.T) {
	meta := WithRuntimeToolMeta(map[string]any{"source": "test"}, map[string]any{
		"path":       "README.md",
		"diff_hunks": []any{"hunk"},
	})

	toolMeta := RuntimeToolMeta(meta)
	if toolMeta["schema"] != RuntimeToolMetaName || toolMeta["schema_version"] != RuntimeToolMetaVersion {
		t.Fatalf("tool meta = %#v, want canonical schema metadata", toolMeta)
	}
	if toolMeta["path"] != "README.md" || RuntimeToolValue(meta, "path") != "README.md" {
		t.Fatalf("tool path meta = %#v", toolMeta)
	}
	if meta["source"] != "test" {
		t.Fatalf("base meta = %#v, want preserved source", meta)
	}
}

func TestWithRuntimeToolMetaMergesExistingToolSection(t *testing.T) {
	meta := WithRuntimeToolMeta(nil, map[string]any{"path": "old.txt"})
	meta = WithRuntimeToolMeta(meta, map[string]any{"action": "write"})

	toolMeta := RuntimeToolMeta(meta)
	if toolMeta["path"] != "old.txt" || toolMeta["action"] != "write" {
		t.Fatalf("tool meta = %#v, want merged existing values", toolMeta)
	}
}
