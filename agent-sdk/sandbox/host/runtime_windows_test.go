//go:build windows

package host

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
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
	status, err := session.Wait(context.Background(), 5*time.Second)
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

func TestHostSessionTerminatePublishesTerminalBeforeReturn(t *testing.T) {
	t.Parallel()

	rt, err := New(Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	session, err := rt.Start(context.Background(), sandbox.CommandRequest{
		Command: "Start-Sleep -Seconds 30",
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := session.Terminate(ctx); err != nil {
		t.Fatalf("Terminate() error = %v", err)
	}
	status, err := session.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Running {
		t.Fatalf("Status() after Terminate = %+v, want terminal publication", status)
	}
	if status.ExitCode != -1 {
		t.Fatalf("Status().ExitCode after Terminate = %d, want -1 cancellation sentinel", status.ExitCode)
	}
}

func TestHostSessionOutputCallbackCanTerminateWithoutDeadlock(t *testing.T) {
	t.Parallel()

	rt, err := New(Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	callbackStarted := make(chan struct{})
	callbackDone := make(chan error, 1)
	var callbackOnce sync.Once
	var session sandbox.Session
	session, err = rt.Start(context.Background(), sandbox.CommandRequest{
		Command: "Write-Output 'stop'; Start-Sleep -Seconds 30",
		OnOutput: func(chunk sandbox.OutputChunk) {
			if chunk.Stream != "stdout" || !strings.Contains(chunk.Text, "stop") {
				return
			}
			callbackOnce.Do(func() { close(callbackStarted) })
			<-ready
			callbackDone <- session.Terminate(context.Background())
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-callbackStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("output callback did not start")
	}
	close(ready)

	select {
	case err := <-callbackDone:
		if err != nil {
			t.Fatalf("Terminate() from output callback error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Terminate() from output callback deadlocked")
	}
	status, err := session.Wait(context.Background(), 2*time.Second)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if status.Running {
		t.Fatalf("Status() after callback termination = %+v, want terminal", status)
	}
}
