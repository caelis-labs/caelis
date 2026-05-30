package filesystem

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/tool"
	sandboxhost "github.com/OnslaughtSnail/caelis/internal/adapters/sandbox/host"
)

func TestReadFileToolReadsNumberedSlice(t *testing.T) {
	rt := newHostRuntime(t)
	if err := rt.FileSystem().WriteFile("notes.txt", []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readTool, err := NewReadFileTool(rt)
	if err != nil {
		t.Fatal(err)
	}
	result := callTool(t, readTool, map[string]any{
		"path":   "notes.txt",
		"offset": 1,
		"limit":  1,
	})
	payload := resultPayload(t, result)
	if payload["start_line"] != float64(2) || payload["end_line"] != float64(2) || payload["has_more"] != true {
		t.Fatalf("payload = %#v, want second line with more content", payload)
	}
	if got, _ := payload["content"].(string); got != "2: two" {
		t.Fatalf("content = %q, want numbered line", got)
	}
}

func TestListDirectoryToolOmitsGitDirectoryAndIncludesMetadata(t *testing.T) {
	rt := newHostRuntime(t)
	if err := rt.FileSystem().WriteFile("a.txt", []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(mustCWD(t, rt), ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	listTool, err := NewListDirectoryTool(rt)
	if err != nil {
		t.Fatal(err)
	}
	result := callTool(t, listTool, map[string]any{"path": ".", "metadata": true})
	payload := resultPayload(t, result)
	entries, ok := payload["entries"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("entries = %#v, want only a.txt", payload["entries"])
	}
	entry := entries[0].(map[string]any)
	if entry["name"] != "a.txt" || entry["type"] != "file" || entry["size"] == nil {
		t.Fatalf("entry = %#v, want file metadata", entry)
	}
}

func TestGlobFilesToolSupportsRecursivePatternsAndExcludes(t *testing.T) {
	rt := newHostRuntime(t)
	if err := os.MkdirAll(filepath.Join(mustCWD(t, rt), "src", "vendor"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"src/main.go", "src/vendor/skip.go", "README.md"} {
		if err := rt.FileSystem().WriteFile(name, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	globTool, err := NewGlobFilesTool(rt)
	if err != nil {
		t.Fatal(err)
	}
	result := callTool(t, globTool, map[string]any{
		"pattern": "**/*.go",
		"exclude": []string{"**/vendor/**"},
	})
	payload := resultPayload(t, result)
	matches := stringSlicePayload(t, payload["matches"])
	if len(matches) != 1 || !strings.HasSuffix(filepath.ToSlash(matches[0]), "src/main.go") {
		t.Fatalf("matches = %#v, want src/main.go only", matches)
	}
}

func TestSearchFilesToolFindsTextAndHonorsGitignore(t *testing.T) {
	rt := newHostRuntime(t)
	if err := os.MkdirAll(filepath.Join(mustCWD(t, rt), "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"keep.txt":      "Alpha\nBeta\n",
		"logs/skip.log": "Alpha\n",
		".gitignore":    "logs/\n",
	}
	for name, content := range files {
		if err := rt.FileSystem().WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	searchTool, err := NewSearchFilesTool(rt)
	if err != nil {
		t.Fatal(err)
	}
	result := callTool(t, searchTool, map[string]any{
		"path":  ".",
		"query": "alpha",
	})
	payload := resultPayload(t, result)
	if payload["count"] != float64(1) || payload["file_count"] != float64(1) {
		t.Fatalf("payload = %#v, want one non-ignored hit", payload)
	}
	hits := payload["hits"].([]any)
	hit := hits[0].(map[string]any)
	if !strings.HasSuffix(filepath.ToSlash(hit["path"].(string)), "keep.txt") || hit["line"] != float64(1) {
		t.Fatalf("hit = %#v, want keep.txt line 1", hit)
	}
}

func TestWriteFileToolCreatesParentDirectories(t *testing.T) {
	rt := newHostRuntime(t)
	writeTool, err := NewWriteFileTool(rt)
	if err != nil {
		t.Fatal(err)
	}
	result := callTool(t, writeTool, map[string]any{
		"path":        "nested/out.txt",
		"content":     "hello\nworld\n",
		"create_dirs": true,
	})
	payload := resultPayload(t, result)
	if payload["created"] != true || payload["changed"] != true {
		t.Fatalf("payload = %#v, want created changed file", payload)
	}
	raw, err := rt.FileSystem().ReadFile("nested/out.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "hello\nworld\n" {
		t.Fatalf("written content = %q", raw)
	}
}

func TestPatchFileToolAppliesExactBatchEditsAtomically(t *testing.T) {
	rt := newHostRuntime(t)
	if err := rt.FileSystem().WriteFile("notes.txt", []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patchTool, err := NewPatchFileTool(rt)
	if err != nil {
		t.Fatal(err)
	}
	result := callTool(t, patchTool, map[string]any{
		"path": "notes.txt",
		"edits": []map[string]any{
			{"old": "alpha", "new": "one"},
			{"old": "gamma", "new": "three"},
		},
	})
	payload := resultPayload(t, result)
	if payload["replacement_count"] != float64(2) || payload["changed"] != true {
		t.Fatalf("payload = %#v, want two replacements", payload)
	}
	raw, err := rt.FileSystem().ReadFile("notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "one\nbeta\nthree\n" {
		t.Fatalf("patched content = %q", raw)
	}
}

func TestPatchFileToolRejectsAmbiguousReplacement(t *testing.T) {
	rt := newHostRuntime(t)
	if err := rt.FileSystem().WriteFile("notes.txt", []byte("x + x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patchTool, err := NewPatchFileTool(rt)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{
		"path": "notes.txt",
		"edits": []map[string]any{
			{"old": "x", "new": "y"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := patchTool.Call(context.Background(), tool.Call{Input: raw}); err == nil {
		t.Fatal("patch error = nil, want ambiguous replacement error")
	}
	content, err := rt.FileSystem().ReadFile("notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "x + x\n" {
		t.Fatalf("content = %q, want unchanged file after rejected patch", content)
	}
}

func newHostRuntime(t *testing.T) sandbox.Runtime {
	t.Helper()
	rt, err := sandboxhost.New(context.Background(), sandbox.Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

func mustCWD(t *testing.T, rt sandbox.Runtime) string {
	t.Helper()
	cwd, err := rt.FileSystem().Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return cwd
}

func callTool(t *testing.T, toolImpl tool.Tool, input map[string]any) tool.Result {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := toolImpl.Call(context.Background(), tool.Call{ID: "call-1", Input: raw})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("result IsError = true: %#v", result)
	}
	return result
}

func resultPayload(t *testing.T, result tool.Result) map[string]any {
	t.Helper()
	if len(result.Content) != 1 || result.Content[0].JSON == nil {
		t.Fatalf("result content = %#v, want one json part", result.Content)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func stringSlicePayload(t *testing.T, raw any) []string {
	t.Helper()
	values, ok := raw.([]any)
	if !ok {
		t.Fatalf("raw = %#v, want []any", raw)
	}
	out := make([]string, 0, len(values))
	for _, item := range values {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("item = %#v, want string", item)
		}
		out = append(out, text)
	}
	return out
}
