package host

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/sandbox"
)

func TestBackendName(t *testing.T) {
	b := New()
	if b.Name() != "host" {
		t.Errorf("got %q, want %q", b.Name(), "host")
	}
}

func TestBackendDescribe(t *testing.T) {
	b := New()
	desc, err := b.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.Name != "host" {
		t.Errorf("got %q, want %q", desc.Name, "host")
	}
}

func TestBackendRun(t *testing.T) {
	b := New()
	cmd := "echo"
	if runtime.GOOS == "windows" {
		cmd = "cmd"
	}
	req := sandbox.CommandRequest{Command: cmd, Args: []string{"hello"}}
	if runtime.GOOS == "windows" {
		req = sandbox.CommandRequest{Command: "cmd", Args: []string{"/C", "echo", "hello"}}
	}
	result, err := b.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code: got %d, want 0", result.ExitCode)
	}
}

func TestBackendRunInvalid(t *testing.T) {
	b := New()
	_, err := b.Run(context.Background(), sandbox.CommandRequest{})
	if err == nil {
		t.Error("expected error for empty command")
	}
}

func TestBackendStatus(t *testing.T) {
	b := New()
	status, err := b.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.Running {
		t.Error("expected running")
	}
}

func TestBackendClose(t *testing.T) {
	b := New()
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestBackendFileSystem(t *testing.T) {
	b := New()
	fs, err := b.FileSystem(context.Background(), sandbox.Constraints{})
	if err != nil {
		t.Fatalf("FileSystem: %v", err)
	}
	if fs == nil {
		t.Fatal("expected non-nil filesystem")
	}
}

func TestBackendCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cancellation test not reliable on windows")
	}

	b := New()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := b.Run(ctx, sandbox.CommandRequest{
			Command: "sleep 60",
			Timeout: 60,
		})
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Command returned after cancellation.
	case <-time.After(5 * time.Second):
		t.Fatal("command did not respond to cancellation within 5s")
	}
}

// ─── Command constraints tests ───────────────────────────────────────

func TestBackendRun_ConstraintsAllowedWorkdir(t *testing.T) {
	dir := t.TempDir()
	b := New()

	result, err := b.Run(context.Background(), sandbox.CommandRequest{
		Command: "echo ok",
		Dir:     dir,
		Constraints: sandbox.Constraints{
			Paths: []sandbox.PathRule{{Path: dir, Access: sandbox.PathAccessWrite}},
		},
	})
	if err != nil {
		t.Fatalf("Run allowed: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code: %d", result.ExitCode)
	}
}

func TestBackendRun_ConstraintsDeniedWorkdir(t *testing.T) {
	dir := t.TempDir()
	allowed := filepath.Join(dir, "workspace")
	denied := filepath.Join(dir, "elsewhere")
	os.MkdirAll(allowed, 0o755)
	os.MkdirAll(denied, 0o755)

	b := New()
	_, err := b.Run(context.Background(), sandbox.CommandRequest{
		Command: "echo nope",
		Dir:     denied,
		Constraints: sandbox.Constraints{
			Paths: []sandbox.PathRule{{Path: allowed, Access: sandbox.PathAccessWrite}},
		},
	})
	if err == nil {
		t.Error("expected error for denied workdir")
	}
}

func TestBackendRun_ConstraintsEmptyWorkdir(t *testing.T) {
	// Empty workdir uses process cwd. If cwd is outside allowed paths,
	// the command should be denied.
	dir := t.TempDir()
	b := New()

	_, err := b.Run(context.Background(), sandbox.CommandRequest{
		Command: "echo ok",
		Constraints: sandbox.Constraints{
			Paths: []sandbox.PathRule{{Path: dir, Access: sandbox.PathAccessWrite}},
		},
	})
	// cwd is sandbox/host which is outside dir → must deny.
	if err == nil {
		t.Error("expected error when cwd is outside allowed paths")
	}
}

func TestBackendRun_ConstraintsEmptyWorkdirAllowed(t *testing.T) {
	// When cwd IS inside the allowed root, empty workdir should succeed.
	cwd, err := os.Getwd()
	if err != nil {
		t.Skip("cannot get cwd")
	}
	b := New()

	result, err := b.Run(context.Background(), sandbox.CommandRequest{
		Command: "echo ok",
		Constraints: sandbox.Constraints{
			Paths: []sandbox.PathRule{{Path: cwd, Access: sandbox.PathAccessWrite}},
		},
	})
	if err != nil {
		t.Fatalf("expected allowed when cwd is in root: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code: %d", result.ExitCode)
	}
}

func TestBackendRun_NoConstraints(t *testing.T) {
	b := New()
	result, err := b.Run(context.Background(), sandbox.CommandRequest{
		Command: "echo free",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code: %d", result.ExitCode)
	}
}
