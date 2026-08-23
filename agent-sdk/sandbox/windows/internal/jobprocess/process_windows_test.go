//go:build windows

package jobprocess

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestStartPipesStdinStdoutAndStderr(t *testing.T) {
	powershell := powershellPath(t)
	process, err := Start(Config{
		Application: powershell,
		Args:        []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", `$v=[Console]::In.ReadToEnd(); Write-Output "out:$v"; [Console]::Error.WriteLine("err:$v")`},
		Env:         os.Environ(),
		Stdin:       true,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer process.ClosePipes()
	if _, err := io.WriteString(process.Input(), "hello"); err != nil {
		t.Fatalf("WriteString(stdin) error = %v", err)
	}
	_ = process.Input().Close()
	stdoutCh := make(chan []byte, 1)
	stderrCh := make(chan []byte, 1)
	go func() { data, _ := io.ReadAll(process.Stdout()); stdoutCh <- data }()
	go func() { data, _ := io.ReadAll(process.Stderr()); stderrCh <- data }()
	if _, err := process.WaitRoot(); err != nil {
		t.Fatalf("WaitRoot() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := process.DrainAndClose(ctx); err != nil {
		cancel()
		t.Fatalf("DrainAndClose() error = %v", err)
	}
	cancel()
	if got := string(<-stdoutCh); !strings.Contains(got, "out:hello") {
		t.Fatalf("stdout = %q", got)
	}
	if got := string(<-stderrCh); !strings.Contains(got, "err:hello") {
		t.Fatalf("stderr = %q", got)
	}
}

func TestStartContainsAndDrainsDescendantAtCreation(t *testing.T) {
	powershell := powershellPath(t)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	script := fmt.Sprintf(`$p=Start-Process powershell.exe -ArgumentList @('-NoLogo','-NoProfile','-NonInteractive','-Command','Start-Sleep -Seconds 30') -PassThru; Set-Content -LiteralPath '%s' -Value $p.Id`, pidFile)
	process, err := Start(Config{Application: powershell, Args: []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script}, Env: os.Environ()})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer process.ClosePipes()
	if _, err := process.WaitRoot(); err != nil {
		t.Fatalf("WaitRoot() error = %v", err)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("ReadFile(child PID) error = %v", err)
	}
	pid, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
	if err != nil {
		t.Fatalf("ParseUint(child PID) error = %v", err)
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatalf("OpenProcess(child) error = %v", err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := process.DrainAndClose(ctx); err != nil {
		cancel()
		t.Fatalf("DrainAndClose() error = %v", err)
	}
	cancel()
	if event, err := windows.WaitForSingleObject(handle, 0); err != nil || event != windows.WAIT_OBJECT_0 {
		t.Fatalf("child after DrainAndClose = %#x/%v, want exited", event, err)
	}
	if err := process.DrainAndClose(context.Background()); err != nil {
		t.Fatalf("DrainAndClose(second) error = %v, want cached success", err)
	}
}

func TestDrainAndCloseHonorsCanceledContext(t *testing.T) {
	process, err := Start(Config{Application: powershellPath(t), Args: []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "Start-Sleep -Seconds 30"}, Env: os.Environ()})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer process.ClosePipes()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = process.Terminate()
	_, _ = process.WaitRoot()
	drainErr := process.DrainAndClose(ctx)
	if drainErr != nil && !strings.Contains(drainErr.Error(), "context canceled") {
		t.Fatalf("DrainAndClose(canceled) error = %v", drainErr)
	}
	if drainErr != nil && !process.NeedsDrainRetry() {
		t.Fatal("NeedsDrainRetry() = false after failed drain, want true")
	}
	retryCtx, retryCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer retryCancel()
	if err := process.DrainAndClose(retryCtx); err != nil {
		t.Fatalf("DrainAndClose(retry) error = %v", err)
	}
	if process.NeedsDrainRetry() {
		t.Fatal("NeedsDrainRetry() = true after successful drain, want false")
	}
}

func TestStartWithoutStdinProvidesValidEOFHandle(t *testing.T) {
	process, err := Start(Config{
		Application: powershellPath(t),
		Args:        []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", `$v=[Console]::In.ReadToEnd(); Write-Output "eof:$v"`},
		Env:         os.Environ(),
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer process.ClosePipes()
	stdoutCh := make(chan []byte, 1)
	go func() { data, _ := io.ReadAll(process.Stdout()); stdoutCh <- data }()
	if _, err := process.WaitRoot(); err != nil {
		t.Fatalf("WaitRoot() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := process.DrainAndClose(ctx); err != nil {
		t.Fatalf("DrainAndClose() error = %v", err)
	}
	if got := string(<-stdoutCh); !strings.Contains(got, "eof:") {
		t.Fatalf("stdout = %q, want EOF output", got)
	}
}

func TestTerminateAndDrainAndCloseAreRaceSafe(t *testing.T) {
	process, err := Start(Config{Application: powershellPath(t), Args: []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "Start-Sleep -Seconds 30"}, Env: os.Environ()})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer process.ClosePipes()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = process.Terminate()
	}()
	go func() {
		defer wg.Done()
		_ = process.DrainAndClose(ctx)
	}()
	wg.Wait()
	_, _ = process.WaitRoot()
	if err := process.DrainAndClose(context.Background()); err != nil {
		t.Fatalf("DrainAndClose(cached) error = %v", err)
	}
}

func powershellPath(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skipf("powershell.exe unavailable: %v", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		t.Fatalf("filepath.Abs(powershell) error = %v", err)
	}
	return path
}
