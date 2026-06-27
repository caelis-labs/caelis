package filesystem

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func TestWriteToolCreatesWorkspaceParentDirectories(t *testing.T) {
	dir := t.TempDir()
	writeTool, err := NewWrite(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewWrite() error = %v", err)
	}

	runWrite(t, writeTool, map[string]any{
		"path":    "pkg/generated/calculator.py",
		"content": "print('ok')\n",
	})

	target := filepath.Join(dir, "pkg", "generated", "calculator.py")
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", target, err)
	}
	if string(content) != "print('ok')\n" {
		t.Fatalf("content = %q, want written file", string(content))
	}
}

func TestWriteToolReturnsRevisionUsableByPatch(t *testing.T) {
	dir := t.TempDir()
	writeTool, err := NewWrite(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewWrite() error = %v", err)
	}
	patchTool := newTestPatchTool(t, dir)

	payload := runWritePayload(t, writeTool, map[string]any{
		"path":    "notes.txt",
		"content": "hello\n",
	})
	revision, _ := payload["revision"].(string)
	if !strings.HasPrefix(revision, "sha256:") {
		t.Fatalf("WRITE revision = %q, want sha256 revision", revision)
	}

	runPatch(t, patchTool, map[string]any{
		"path":        "notes.txt",
		"if_revision": revision,
		"edits": []map[string]any{
			{"old": "hello", "new": "hi"},
		},
	})
	if got, want := readTestFile(t, filepath.Join(dir, "notes.txt")), "hi\n"; got != want {
		t.Fatalf("patched content = %q, want %q", got, want)
	}
}

func TestWriteToolDoesNotCreateParentsOutsideWorkspace(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "missing", "outside.txt")
	writeTool, err := NewWrite(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewWrite() error = %v", err)
	}

	err = callWrite(writeTool, map[string]any{
		"path":    outside,
		"content": "nope\n",
	})
	if err == nil {
		t.Fatal("WRITE error = nil, want missing outside parent to remain an error")
	}
	if _, statErr := os.Stat(filepath.Dir(outside)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("outside parent stat error = %v, want not exist", statErr)
	}
}

func TestWriteToolMissingTargetWithRevisionReportsCreationGuard(t *testing.T) {
	dir := t.TempDir()
	writeTool, err := NewWrite(fakeRuntime{defaultFS: hostFileSystem{cwd: dir}})
	if err != nil {
		t.Fatalf("NewWrite() error = %v", err)
	}

	err = callWrite(writeTool, map[string]any{
		"path":        "new.txt",
		"content":     "hello\n",
		"if_revision": "sha256:abcdef",
	})
	var toolErr *tool.ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("WRITE error = %T %v, want ToolError", err, err)
	}
	if toolErr.Code != tool.ErrorCodeNotFound {
		t.Fatalf("WRITE error code = %s, want %s: %v", toolErr.Code, tool.ErrorCodeNotFound, err)
	}
	text := err.Error()
	if !strings.Contains(text, "target does not exist") || !strings.Contains(text, "omit if_revision") {
		t.Fatalf("WRITE error = %q, want missing-target creation guard guidance", text)
	}
	if strings.Contains(text, "changed since it was read") || strings.Contains(text, filepath.Join(dir, "new.txt")) {
		t.Fatalf("WRITE error used stale-read wording or leaked path: %q", text)
	}
}

func runWrite(t *testing.T, writeTool *WriteTool, args map[string]any) {
	t.Helper()
	if err := callWrite(writeTool, args); err != nil {
		t.Fatalf("WRITE error = %v", err)
	}
}

func runWritePayload(t *testing.T, writeTool *WriteTool, args map[string]any) map[string]any {
	t.Helper()
	input, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	result, err := writeTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("WRITE error = %v", err)
	}
	return filesystemToolPayload(t, result)
}

func callWrite(writeTool *WriteTool, args map[string]any) error {
	input, err := json.Marshal(args)
	if err != nil {
		return err
	}
	_, err = writeTool.Call(context.Background(), tool.Call{Input: input})
	return err
}
