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

func runWrite(t *testing.T, writeTool *WriteTool, args map[string]any) {
	t.Helper()
	if err := callWrite(writeTool, args); err != nil {
		t.Fatalf("WRITE error = %v", err)
	}
}

func callWrite(writeTool *WriteTool, args map[string]any) error {
	input, err := json.Marshal(args)
	if err != nil {
		return err
	}
	_, err = writeTool.Call(context.Background(), tool.Call{Input: input})
	return err
}
