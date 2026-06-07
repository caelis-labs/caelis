package landlock

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/sandbox"
)

func TestBackendNameDescribeStatusAndFilesystem(t *testing.T) {
	b := New()
	if b.Name() != "landlock" {
		t.Fatalf("Name() = %q, want landlock", b.Name())
	}
	desc, err := b.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.Name != "landlock" || desc.Platform != "linux" {
		t.Fatalf("descriptor = %#v, want landlock linux", desc)
	}
	status, err := b.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Running != (runtime.GOOS == "linux") {
		t.Fatalf("status = %#v, want running only on linux", status)
	}
	fs, err := b.FileSystem(context.Background(), sandbox.Constraints{})
	if err != nil {
		t.Fatalf("FileSystem: %v", err)
	}
	if fs == nil {
		t.Fatal("FileSystem returned nil")
	}
}

func TestBackendRunUnsupportedOffLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("unsupported path is for non-linux")
	}
	_, err := New().Run(context.Background(), sandbox.CommandRequest{Command: "printf no"})
	if err == nil || !strings.Contains(err.Error(), "only supported on linux") {
		t.Fatalf("Run error = %v, want linux unsupported", err)
	}
}
