package host

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
)

func TestRuntimeRunExecutesCommandInConfiguredCWD(t *testing.T) {
	rt, err := New(context.Background(), sandbox.Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	command := "printf hello"
	if runtime.GOOS == "windows" {
		command = "echo hello"
	}
	result, err := rt.Run(context.Background(), sandbox.CommandRequest{Command: command})
	if err != nil {
		t.Fatal(err)
	}
	if result.Backend != sandbox.BackendHost || result.Route != sandbox.RouteHost {
		t.Fatalf("route/backend = %q/%q, want host/host", result.Route, result.Backend)
	}
	if got := strings.TrimSpace(result.Stdout); got != "hello" {
		t.Fatalf("stdout = %q, want hello", got)
	}
}

func TestRuntimeFileSystemResolvesRelativePaths(t *testing.T) {
	dir := t.TempDir()
	rt, err := New(context.Background(), sandbox.Config{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}

	if err := rt.FileSystem().WriteFile("note.txt", []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := rt.FileSystem().ReadFile("note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ok" {
		t.Fatalf("file content = %q, want ok", string(data))
	}
}
