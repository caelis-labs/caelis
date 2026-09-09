package filesystem

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestSearchToolPreservesNormalHits(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nconst needle = true\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	searchTool, err := NewSearch(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewSearch() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{"pattern": "needle"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	result, err := searchTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	payload := filesystemToolPayload(t, result)
	hits, _ := payload["hits"].([]any)
	if len(hits) != 1 {
		t.Fatalf("hits = %#v, want one match", payload["hits"])
	}
	if _, ok := payload["message"]; ok {
		t.Fatalf("successful payload must not contain message: %#v", payload)
	}
}

func TestSearchToolPropagatesOversizedLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "huge.txt"), []byte(strings.Repeat("x", maxSearchLineBytes+1)+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	searchTool, err := NewSearch(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewSearch() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{"pattern": "x"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	_, err = searchTool.Call(context.Background(), tool.Call{Input: input})
	var toolErr *tool.ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != tool.ErrorCodeInvalidInput || !strings.Contains(err.Error(), "8MiB") {
		t.Fatalf("Call(oversized) error = %v, want invalid_input 8MiB line error", err)
	}
}

func TestSearchToolPropagatesCancellationDuringWalk(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nconst needle = true\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	searchTool, err := NewSearch(fakeRuntime{defaultFS: cancelOnWalkFileSystem{
		hostFileSystem: hostFileSystem{cwd: dir},
		cancel:         cancel,
	}})
	if err != nil {
		t.Fatalf("NewSearch() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{"pattern": "needle"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	_, err = searchTool.Call(ctx, tool.Call{Input: input})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Call(canceled) error = %v, want context.Canceled", err)
	}
}
