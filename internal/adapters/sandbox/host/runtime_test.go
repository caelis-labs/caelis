package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	if snapshot.Metadata["sandbox_route"] != string(sandbox.RouteHost) ||
		snapshot.Metadata["sandbox_backend"] != string(sandbox.BackendHost) ||
		snapshot.Metadata["sandbox_permission"] != string(sandbox.PermissionFullAccess) ||
		snapshot.Metadata["sandbox_network"] != string(sandbox.NetworkInherit) {
		t.Fatalf("snapshot metadata = %#v, want host sandbox policy metadata", snapshot.Metadata)
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

func TestRuntimeReopensArchivedAsyncSessionFromStateDir(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	rt, err := New(context.Background(), sandbox.Config{CWD: dir, StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	command := "printf durable"
	if runtime.GOOS == "windows" {
		command = "echo durable"
	}
	session, err := rt.Start(context.Background(), sandbox.CommandRequest{Command: command})
	if err != nil {
		t.Fatal(err)
	}
	ref := session.Ref()
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := session.Wait(waitCtx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Stdout, "durable") {
		t.Fatalf("stdout = %q, want durable output", result.Stdout)
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := New(context.Background(), sandbox.Config{CWD: dir, StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	archived, err := reopened.Open(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := archived.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Running || snapshot.State != sandbox.SessionCompleted || snapshot.Ref.ID != ref.ID {
		t.Fatalf("archived snapshot = %#v, want completed session %q", snapshot, ref.ID)
	}
	if snapshot.OutputPreview == nil || !strings.Contains(snapshot.OutputPreview.Stdout, "durable") || snapshot.OutputPreview.Cursor.Stdout == 0 {
		t.Fatalf("archived snapshot preview = %#v, want durable stdout preview", snapshot.OutputPreview)
	}
	output, err := archived.Read(context.Background(), sandbox.OutputCursor{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.Stdout, "durable") || output.Cursor.Stdout == 0 {
		t.Fatalf("archived output = %#v, want durable stdout and cursor", output)
	}
	if err := archived.Write(context.Background(), []byte("input\n")); err == nil {
		t.Fatal("archived Write() error = nil, want read-only session")
	}
	listed, err := reopened.ListSessions(context.Background(), sandbox.SessionListQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !hasSessionSnapshot(listed, ref.ID) {
		t.Fatalf("archived session %q missing from list: %#v", ref.ID, listed)
	}
	listedSnapshot := findSessionSnapshot(listed, ref.ID)
	if listedSnapshot.OutputPreview == nil || !strings.Contains(listedSnapshot.OutputPreview.Stdout, "durable") {
		t.Fatalf("listed snapshot preview = %#v, want durable stdout preview", listedSnapshot.OutputPreview)
	}
}

func TestRuntimeRecoversLiveAsyncSessionFromStateDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("live process recovery test uses POSIX shell timing")
	}
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	rt, err := New(context.Background(), sandbox.Config{CWD: dir, StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	session, err := rt.Start(context.Background(), sandbox.CommandRequest{Command: "printf before; sleep 1; printf after"})
	if err != nil {
		t.Fatal(err)
	}
	ref := session.Ref()

	reopened, err := New(context.Background(), sandbox.Config{CWD: dir, StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	recovered, err := reopened.Open(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := recovered.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Running || snapshot.State != sandbox.SessionRunning || snapshot.SupportsInput {
		t.Fatalf("recovered snapshot = %#v, want running read-only recovered session", snapshot)
	}
	var recoveredOutput sandbox.OutputSnapshot
	for range 50 {
		recoveredOutput, err = recovered.Read(context.Background(), sandbox.OutputCursor{})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(recoveredOutput.Stdout, "before") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(recoveredOutput.Stdout, "before") {
		t.Fatalf("recovered output before wait = %#v, want durable prefix", recoveredOutput)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := recovered.Wait(waitCtx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Stdout, "beforeafter") {
		t.Fatalf("recovered result stdout = %q, want beforeafter", result.Stdout)
	}
	if result.Backend != sandbox.BackendHost || result.Route != sandbox.RouteHost {
		t.Fatalf("recovered route/backend = %q/%q, want host/host", result.Route, result.Backend)
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

func TestRuntimeFileSystemUsesConfiguredRootPolicy(t *testing.T) {
	root := t.TempDir()
	readRoot := filepath.Join(root, "read")
	writeRoot := filepath.Join(root, "write")
	for _, dir := range []string{readRoot, writeRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(readRoot, "note.txt"), []byte("readable"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt, err := New(context.Background(), sandbox.Config{
		CWD:           root,
		ReadableRoots: []string{readRoot},
		WritableRoots: []string{writeRoot},
	})
	if err != nil {
		t.Fatal(err)
	}
	fsys := rt.FileSystem()
	if _, err := fsys.ReadFile(filepath.Join(readRoot, "note.txt")); err != nil {
		t.Fatalf("ReadFile(read root) error = %v, want allowed", err)
	}
	if _, err := fsys.ReadFile(filepath.Join(string(filepath.Separator), "caelis-denied-read", "note.txt")); err == nil || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("ReadFile(blocked root) error = %v, want permission", err)
	}
	if err := fsys.WriteFile(filepath.Join(writeRoot, "note.txt"), []byte("writable"), 0o644); err != nil {
		t.Fatalf("WriteFile(write root) error = %v, want allowed", err)
	}
	if err := fsys.WriteFile(filepath.Join(string(filepath.Separator), "caelis-denied-write", "note.txt"), []byte("denied"), 0o644); err == nil || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("WriteFile(read root) error = %v, want permission", err)
	}
}

func hasSessionSnapshot(items []sandbox.SessionSnapshot, id string) bool {
	return findSessionSnapshot(items, id).Ref.ID != ""
}

func findSessionSnapshot(items []sandbox.SessionSnapshot, id string) sandbox.SessionSnapshot {
	for _, item := range items {
		if item.Ref.ID == id {
			return item
		}
	}
	return sandbox.SessionSnapshot{}
}
