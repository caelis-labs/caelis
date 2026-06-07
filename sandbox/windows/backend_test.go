package windows

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/sandbox"
)

func TestBackendNameDescribeStatusAndFilesystem(t *testing.T) {
	b := New()
	if b.Name() != "windows" {
		t.Fatalf("Name() = %q, want windows", b.Name())
	}
	desc, err := b.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.Name != "windows" || desc.Platform != "windows" {
		t.Fatalf("descriptor = %#v, want windows backend", desc)
	}
	status, err := b.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Running != (runtime.GOOS == "windows") {
		t.Fatalf("status = %#v, want running only on windows", status)
	}
	fs, err := b.FileSystem(context.Background(), sandbox.Constraints{})
	if err != nil {
		t.Fatalf("FileSystem: %v", err)
	}
	if fs == nil {
		t.Fatal("FileSystem returned nil")
	}
}

func TestBackendRunUnsupportedOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unsupported path is for non-windows")
	}
	_, err := New().Run(context.Background(), sandbox.CommandRequest{Command: "echo no"})
	if err == nil || !strings.Contains(err.Error(), "only supported on windows") {
		t.Fatalf("Run error = %v, want windows unsupported", err)
	}
}
