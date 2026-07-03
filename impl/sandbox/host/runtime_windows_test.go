//go:build windows

package host

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/ports/sandbox"
)

func TestRuntimeRunWindowsDropsPowerShellWriteHostCLIXMLMirror(t *testing.T) {
	t.Parallel()

	rt, err := New(Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := rt.Run(context.Background(), sandbox.CommandRequest{Command: `Write-Host "Done"`})
	if err != nil {
		t.Fatalf("Run() error = %v; stdout=%q stderr=%q", err, result.Stdout, result.Stderr)
	}
	merged := result.Stdout + result.Stderr
	if strings.Count(merged, "Done") != 1 {
		t.Fatalf("merged output = %q, want exactly one Done", merged)
	}
	if strings.Contains(merged, "#< CLIXML") || strings.Contains(merged, "<Objs") {
		t.Fatalf("merged output = %q, want CLIXML stripped", merged)
	}
}

func TestRuntimeStartWindowsDecodesPowerShellErrorCLIXML(t *testing.T) {
	t.Parallel()

	rt, err := New(Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	session, err := rt.Start(context.Background(), sandbox.CommandRequest{Command: `Test-Path $null`})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	status, err := session.Wait(context.Background(), 2*time.Second)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if status.Running {
		t.Fatal("status.Running = true, want false")
	}
	result, _ := session.Result(context.Background())
	merged := result.Stdout + result.Stderr
	if !strings.Contains(merged, "Test-Path") {
		t.Fatalf("merged output = %q, want decoded PowerShell error text", merged)
	}
	if strings.Contains(merged, "#< CLIXML") || strings.Contains(merged, "<Objs") {
		t.Fatalf("merged output = %q, want CLIXML stripped", merged)
	}
}
