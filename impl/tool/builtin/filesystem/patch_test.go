package filesystem

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func TestPatchToolPreservesExactPythonWhitespace(t *testing.T) {
	dir := t.TempDir()
	before := "def enabled():\n    if ready:\n        return True\n    return False\n"
	path := filepath.Join(dir, "feature.py")
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	patchTool := newTestPatchTool(t, dir)

	runPatch(t, patchTool, map[string]any{
		"path": "feature.py",
		"edits": []map[string]any{
			{
				"old": "    if ready:\n        return True\n",
				"new": "    if ready:\n        return False\n",
			},
		},
	})

	got := readTestFile(t, path)
	want := "def enabled():\n    if ready:\n        return False\n    return False\n"
	if got != want {
		t.Fatalf("patched content = %q, want exact whitespace-preserving replacement", got)
	}
}

func TestPatchToolAllowsWhitespaceOnlyReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blank.txt")
	if err := os.WriteFile(path, []byte("before\nremove me\nafter\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	patchTool := newTestPatchTool(t, dir)

	runPatch(t, patchTool, map[string]any{
		"path": "blank.txt",
		"edits": []map[string]any{
			{
				"old": "remove me",
				"new": "   ",
			},
		},
	})

	if got, want := readTestFile(t, path), "before\n   \nafter\n"; got != want {
		t.Fatalf("patched content = %q, want %q", got, want)
	}
}

func TestPatchToolAppliesBatchEditsAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	patchTool := newTestPatchTool(t, dir)

	payload := runPatch(t, patchTool, map[string]any{
		"path": "notes.txt",
		"edits": []map[string]any{
			{"old": "alpha", "new": "one"},
			{"old": "gamma", "new": "three"},
		},
	})

	if got := payload["replacements"]; got != float64(2) {
		t.Fatalf("replacements = %v, want 2", got)
	}
	if got := payload["edit_count"]; got != float64(2) {
		t.Fatalf("edit_count = %v, want 2", got)
	}
	if got, want := readTestFile(t, path), "one\nbeta\nthree\n"; got != want {
		t.Fatalf("patched content = %q, want %q", got, want)
	}
}

func TestPatchToolPreservesPathWhitespace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, " spaced.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	patchTool := newTestPatchTool(t, dir)

	runPatch(t, patchTool, map[string]any{
		"path": " spaced.txt",
		"edits": []map[string]any{
			{"old": "old", "new": "new"},
		},
	})

	if got, want := readTestFile(t, path), "new\n"; got != want {
		t.Fatalf("patched content = %q, want %q", got, want)
	}
}

func TestPatchToolReplaceAllWithExpectedCount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "symbols.txt")
	if err := os.WriteFile(path, []byte("x + x + x\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	patchTool := newTestPatchTool(t, dir)

	runPatch(t, patchTool, map[string]any{
		"path": "symbols.txt",
		"edits": []map[string]any{
			{
				"old":                   "x",
				"new":                   "y",
				"replace_all":           true,
				"expected_replacements": 3,
			},
		},
	})

	if got, want := readTestFile(t, path), "y + y + y\n"; got != want {
		t.Fatalf("patched content = %q, want %q", got, want)
	}
}

func TestPatchToolRejectsDuplicateMatchesWithoutReplaceAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "duplicates.txt")
	if err := os.WriteFile(path, []byte("x + x\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	patchTool := newTestPatchTool(t, dir)

	err := callPatch(patchTool, map[string]any{
		"path": "duplicates.txt",
		"edits": []map[string]any{
			{"old": "x", "new": "y"},
		},
	})
	requireToolErrorCode(t, err, tool.ErrorCodeTooManyMatches)
	if got, want := readTestFile(t, path), "x + x\n"; got != want {
		t.Fatalf("content changed after failed PATCH: %q", got)
	}
}

func TestPatchToolRejectsExpectedCountMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "symbols.txt")
	if err := os.WriteFile(path, []byte("x + x\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	patchTool := newTestPatchTool(t, dir)

	err := callPatch(patchTool, map[string]any{
		"path": "symbols.txt",
		"edits": []map[string]any{
			{
				"old":                   "x",
				"new":                   "y",
				"replace_all":           true,
				"expected_replacements": 3,
			},
		},
	})
	requireToolErrorCode(t, err, tool.ErrorCodeUnexpectedMatchCount)
	if got, want := readTestFile(t, path), "x + x\n"; got != want {
		t.Fatalf("content changed after failed PATCH: %q", got)
	}
}

func TestPatchToolRequiresExpectedCountForReplaceAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "replace-all.txt")
	if err := os.WriteFile(path, []byte("x + x\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	patchTool := newTestPatchTool(t, dir)

	err := callPatch(patchTool, map[string]any{
		"path": "replace-all.txt",
		"edits": []map[string]any{
			{
				"old":         "x",
				"new":         "y",
				"replace_all": true,
			},
		},
	})
	requireToolErrorCode(t, err, tool.ErrorCodeInvalidInput)
	if got, want := readTestFile(t, path), "x + x\n"; got != want {
		t.Fatalf("content changed after failed PATCH: %q", got)
	}
}

func TestPatchToolRejectsOverlappingBatchEdits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "overlap.txt")
	if err := os.WriteFile(path, []byte("abcdef\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	patchTool := newTestPatchTool(t, dir)

	err := callPatch(patchTool, map[string]any{
		"path": "overlap.txt",
		"edits": []map[string]any{
			{"old": "abc", "new": "ABC"},
			{"old": "bcd", "new": "BCD"},
		},
	})
	requireToolErrorCode(t, err, tool.ErrorCodeInvalidInput)
	if got, want := readTestFile(t, path), "abcdef\n"; got != want {
		t.Fatalf("content changed after failed PATCH: %q", got)
	}
}

func TestPatchToolRejectsEmptyOld(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty-old.txt")
	if err := os.WriteFile(path, []byte("content\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	patchTool := newTestPatchTool(t, dir)

	err := callPatch(patchTool, map[string]any{
		"path": "empty-old.txt",
		"edits": []map[string]any{
			{"old": "", "new": "replacement"},
		},
	})
	requireToolErrorCode(t, err, tool.ErrorCodeInvalidInput)
}

func TestPatchToolRequiresEditsArray(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	patchTool := newTestPatchTool(t, dir)

	err := callPatch(patchTool, map[string]any{
		"path": "legacy.txt",
		"old":  "old",
		"new":  "new",
	})
	requireToolErrorCode(t, err, tool.ErrorCodeInvalidInput)
}

func newTestPatchTool(t *testing.T, dir string) *PatchTool {
	t.Helper()
	patchTool, err := NewPatch(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewPatch() error = %v", err)
	}
	return patchTool
}

func runPatch(t *testing.T, patchTool *PatchTool, args map[string]any) map[string]any {
	t.Helper()
	input, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	result, err := patchTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("PATCH error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSONValue(), &payload); err != nil {
		t.Fatalf("Unmarshal(result) error = %v", err)
	}
	return payload
}

func callPatch(patchTool *PatchTool, args map[string]any) error {
	input, err := json.Marshal(args)
	if err != nil {
		return err
	}
	_, err = patchTool.Call(context.Background(), tool.Call{Input: input})
	return err
}

func requireToolErrorCode(t *testing.T, err error, code tool.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("PATCH error = nil, want %s", code)
	}
	var toolErr *tool.ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("PATCH error = %T %v, want ToolError", err, err)
	}
	if toolErr.Code != code {
		t.Fatalf("PATCH error code = %s, want %s: %v", toolErr.Code, code, err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	return string(raw)
}
