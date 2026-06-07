package bwrap

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
	if b.Name() != "bwrap" {
		t.Fatalf("Name() = %q, want bwrap", b.Name())
	}
	desc, err := b.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.Name != "bwrap" || desc.Platform != "linux" {
		t.Fatalf("descriptor = %#v, want bwrap linux", desc)
	}
	status, err := b.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Running != (runtime.GOOS == "linux" && commandExists("bwrap") && commandExists("bash")) {
		t.Fatalf("status = %#v, want running only with bwrap+bash on linux", status)
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
	if runtime.GOOS != "linux" {
		t.Skip("bwrap execution is linux-only")
	}
	if !commandExists("bwrap") || !commandExists("bash") {
		t.Skip("bwrap or bash unavailable")
	}

	result, err := New().Run(context.Background(), sandbox.CommandRequest{
		Command: "printf bwrap-ok",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(string(result.Stdout)) != "bwrap-ok" || result.ExitCode != 0 {
		t.Fatalf("result = %#v, want bwrap-ok exit 0", result)
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

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
