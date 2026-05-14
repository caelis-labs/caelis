package filesystem

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func TestListToolOmitsMetadataByDefault(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	listTool, err := NewList(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewList() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{"path": "."})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	result, err := listTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	entries := filesystemToolMetaEntries(t, result)
	if len(entries) != 1 {
		t.Fatalf("metadata entries = %d, want 1", len(entries))
	}
	if _, ok := entries[0]["size"]; ok {
		t.Fatalf("default metadata unexpectedly included size: %#v", entries[0])
	}
	if _, ok := entries[0]["mod_time"]; ok {
		t.Fatalf("default metadata unexpectedly included mod_time: %#v", entries[0])
	}
}

func TestListToolIncludesMetadataWhenRequested(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	listTool, err := NewList(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewList() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{"path": ".", "metadata": true})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	result, err := listTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	payload := filesystemToolPayload(t, result)
	rawEntries, _ := payload["entries"].([]any)
	if len(rawEntries) != 1 {
		t.Fatalf("payload entries = %d, want 1", len(rawEntries))
	}
	entry, _ := rawEntries[0].(map[string]any)
	if got := numericMetaValue(entry["size"]); got != 5 {
		t.Fatalf("payload size = %v, want 5", entry["size"])
	}
	if _, ok := entry["mod_time"]; !ok {
		t.Fatalf("payload metadata missing mod_time: %#v", entry)
	}
}

func TestGlobToolStopsAfterLimitPlusOneMatches(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	globTool, err := NewGlob(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewGlob() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{
		"pattern": "*.txt",
		"limit":   2,
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	result, err := globTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	payload := filesystemToolPayload(t, result)
	if got, _ := payload["truncated"].(bool); !got {
		t.Fatalf("truncated = %v, want true", payload["truncated"])
	}
	meta := filesystemToolMeta(t, result)
	if got := numericMetaValue(meta["total_count"]); got != 3 {
		t.Fatalf("total_count = %v, want limit+1 sentinel count 3", meta["total_count"])
	}
}

func filesystemToolPayload(t *testing.T, result tool.Result) map[string]any {
	t.Helper()
	if len(result.Content) != 1 || result.Content[0].Kind != model.PartKindJSON {
		t.Fatalf("result.Content = %+v, want json", result.Content)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSONValue(), &payload); err != nil {
		t.Fatalf("Unmarshal(payload) error = %v", err)
	}
	return payload
}

func filesystemToolMeta(t *testing.T, result tool.Result) map[string]any {
	t.Helper()
	caelis, _ := result.Metadata["caelis"].(map[string]any)
	runtime, _ := caelis["runtime"].(map[string]any)
	meta, _ := runtime["tool"].(map[string]any)
	if meta == nil {
		t.Fatalf("missing tool metadata: %#v", result.Metadata)
	}
	return meta
}

func filesystemToolMetaEntries(t *testing.T, result tool.Result) []map[string]any {
	t.Helper()
	rawEntries, _ := filesystemToolMeta(t, result)["entries"].([]map[string]any)
	if rawEntries != nil {
		return rawEntries
	}
	entriesAny, _ := filesystemToolMeta(t, result)["entries"].([]any)
	out := make([]map[string]any, 0, len(entriesAny))
	for _, item := range entriesAny {
		entry, _ := item.(map[string]any)
		if entry != nil {
			out = append(out, entry)
		}
	}
	return out
}

func numericMetaValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	default:
		return 0
	}
}
