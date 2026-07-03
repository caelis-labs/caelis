package filesystem

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/ports/model"
	"github.com/caelis-labs/caelis/ports/tool"
)

func TestReadToolDoesNotScanOversizedTailPastLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "generated.txt")
	oversizedLine := strings.Repeat("x", 9*1024*1024)
	if err := os.WriteFile(path, []byte("first\n"+oversizedLine+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	readTool, err := NewRead(DefaultReadConfig(), fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewRead() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{
		"path":  "generated.txt",
		"limit": 1,
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	result, err := readTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].Kind != model.PartKindJSON {
		t.Fatalf("result.Content = %+v, want json", result.Content)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSONValue(), &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got, _ := payload["content"].(string); got != "1: first" {
		t.Fatalf("content = %q, want first line only", got)
	}
	if got, _ := payload["has_more"].(bool); !got {
		t.Fatalf("has_more = %v, want true", payload["has_more"])
	}
	if got, _ := payload["revision"].(string); got == "" {
		t.Fatal("revision is empty")
	}
	if got, _ := payload["revision"].(string); !strings.HasPrefix(got, "stat:") {
		t.Fatalf("truncated read revision = %q, want stat revision without tail scan", got)
	}
	patchTool, err := NewPatch(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewPatch() error = %v", err)
	}
	patchInput, err := json.Marshal(map[string]any{
		"path": "generated.txt",
		"edits": []map[string]any{
			{
				"old":                   "first",
				"new":                   "updated",
				"expected_replacements": 1,
			},
		},
		"if_revision": payload["revision"],
	})
	if err != nil {
		t.Fatalf("Marshal(patch) error = %v", err)
	}
	if _, err := patchTool.Call(context.Background(), tool.Call{Input: patchInput}); err != nil {
		t.Fatalf("PATCH with stat revision error = %v", err)
	}
}
