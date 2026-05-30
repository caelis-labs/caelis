package host

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

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

func TestRuntimeStartSupportsAsyncOutputInputAndCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("async stdin test uses POSIX cat")
	}
	rt, err := New(context.Background(), sandbox.Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	session, err := rt.Start(context.Background(), sandbox.CommandRequest{Command: "cat"})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Write(context.Background(), []byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	var output sandbox.OutputSnapshot
	for range 50 {
		output, err = session.Read(context.Background(), sandbox.OutputCursor{})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.Stdout, "hello") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(output.Stdout, "hello") {
		t.Fatalf("stdout = %q, want echoed input", output.Stdout)
	}
	snapshot, err := session.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Running || !snapshot.SupportsInput || snapshot.State != sandbox.SessionRunning {
		t.Fatalf("snapshot = %#v, want running input-capable session", snapshot)
	}
	if err := session.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := session.Wait(waitCtx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Backend != sandbox.BackendHost || result.Route != sandbox.RouteHost {
		t.Fatalf("result route/backend = %q/%q, want host/host", result.Route, result.Backend)
	}
	snapshot, err = session.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Running || snapshot.State != sandbox.SessionCancelled {
		t.Fatalf("snapshot after cancel = %#v, want cancelled", snapshot)
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
