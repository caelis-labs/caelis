//go:build unix

package filesystem

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestReadAndGrepRejectFIFOWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	fifoPath := filepath.Join(dir, "blocked.fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("Mkfifo() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nconst needle = true\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(main.go) error = %v", err)
	}
	rt, err := host.New(host.Config{CWD: dir})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	readTool, err := NewRead(DefaultReadConfig(), rt)
	if err != nil {
		t.Fatalf("NewRead() error = %v", err)
	}
	searchTool, err := NewSearch(rt)
	if err != nil {
		t.Fatalf("NewSearch() error = %v", err)
	}

	readInput, err := json.Marshal(map[string]any{"path": "blocked.fifo"})
	if err != nil {
		t.Fatalf("Marshal(read) error = %v", err)
	}
	err = mustCompleteSoon(t, func() error {
		_, callErr := readTool.Call(context.Background(), tool.Call{Input: readInput})
		return callErr
	})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("Read(fifo) error = %v, want regular-file error", err)
	}

	directInput, err := json.Marshal(map[string]any{"path": "blocked.fifo", "pattern": "needle"})
	if err != nil {
		t.Fatalf("Marshal(direct grep) error = %v", err)
	}
	err = mustCompleteSoon(t, func() error {
		_, callErr := searchTool.Call(context.Background(), tool.Call{Input: directInput})
		return callErr
	})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("Grep(fifo) error = %v, want regular-file error", err)
	}

	walkInput, err := json.Marshal(map[string]any{"pattern": "needle"})
	if err != nil {
		t.Fatalf("Marshal(walk grep) error = %v", err)
	}
	result, err := searchTool.Call(context.Background(), tool.Call{Input: walkInput})
	if err != nil {
		t.Fatalf("Grep(walk with fifo) error = %v", err)
	}
	hits, _ := filesystemToolPayload(t, result)["hits"].([]any)
	if len(hits) != 1 {
		t.Fatalf("hits = %#v, want the regular file match while skipping fifo", hits)
	}
}

func TestGitignoreFIFODoesNotBlockSearch(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, ".gitignore"), 0o600); err != nil {
		t.Fatalf("Mkfifo(.gitignore) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nconst needle = true\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(main.go) error = %v", err)
	}
	rt, err := host.New(host.Config{CWD: dir})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	searchTool, err := NewSearch(rt)
	if err != nil {
		t.Fatalf("NewSearch() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{"pattern": "needle"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	result, err := searchTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatalf("Grep(.gitignore fifo) error = %v", err)
	}
	hits, _ := filesystemToolPayload(t, result)["hits"].([]any)
	if len(hits) != 1 {
		t.Fatalf("hits = %#v, want match with fifo gitignore skipped", hits)
	}
}

func TestReadRejectsDeviceFile(t *testing.T) {
	rt, err := host.New(host.Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	readTool, err := NewRead(DefaultReadConfig(), rt)
	if err != nil {
		t.Fatalf("NewRead() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{"path": "/dev/null"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	err = mustCompleteSoon(t, func() error {
		_, callErr := readTool.Call(context.Background(), tool.Call{Input: input})
		return callErr
	})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("Read(/dev/null) error = %v, want regular-file error", err)
	}
}

func mustCompleteSoon(t *testing.T, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- fn()
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("operation blocked on a special file")
		return nil
	}
}
