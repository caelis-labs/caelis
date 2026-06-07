package seatbelt

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/sandbox"
)

func TestBackendNameDescribeStatusAndFilesystem(t *testing.T) {
	b := New()
	if b.Name() != "seatbelt" {
		t.Fatalf("Name() = %q, want seatbelt", b.Name())
	}
	desc, err := b.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.Name != "seatbelt" || desc.Platform != "darwin" {
		t.Fatalf("descriptor = %#v, want seatbelt darwin", desc)
	}
	status, err := b.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Running != (runtime.GOOS == "darwin") {
		t.Fatalf("status = %#v, want running only on darwin", status)
	}
	fs, err := b.FileSystem(context.Background(), sandbox.Constraints{})
	if err != nil {
		t.Fatalf("FileSystem: %v", err)
	}
	if fs == nil {
		t.Fatal("FileSystem returned nil")
	}
}

func TestBackendRun(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("seatbelt execution is darwin-only")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec unavailable")
	}

	result, err := New().Run(context.Background(), sandbox.CommandRequest{
		Command: "printf seatbelt-ok",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(string(result.Stdout)) != "seatbelt-ok" || result.ExitCode != 0 {
		t.Fatalf("result = %#v, want seatbelt-ok exit 0", result)
	}
}

func TestBackendRunUnsupportedOffDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("unsupported path is for non-darwin")
	}
	_, err := New().Run(context.Background(), sandbox.CommandRequest{Command: "printf no"})
	if err == nil || !strings.Contains(err.Error(), "only supported on darwin") {
		t.Fatalf("Run error = %v, want darwin unsupported", err)
	}
}
