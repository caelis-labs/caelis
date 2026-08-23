//go:build windows

package conpty

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestStartPlacesTTYDescendantInKillOnCloseJobAtCreation(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	script := fmt.Sprintf(`$p=Start-Process powershell.exe -ArgumentList @('-NoLogo','-NoProfile','-NonInteractive','-Command','Start-Sleep -Seconds 30') -PassThru; Set-Content -LiteralPath '%s' -Value $p.Id`, pidFile)
	process, err := Start(Config{
		Application: "powershell.exe",
		Args:        []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script},
		Env:         os.Environ(),
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer process.CloseAfterExit()
	defer process.Output().Close()
	if _, err := process.Wait(); err != nil {
		t.Fatalf("Wait(root) error = %v", err)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("ReadFile(child PID) error = %v", err)
	}
	pid64, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
	if err != nil {
		t.Fatalf("ParseUint(child PID) error = %v", err)
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid64))
	if err != nil {
		t.Fatalf("OpenProcess(child before Job close) error = %v", err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	if event, err := windows.WaitForSingleObject(handle, 0); err != nil || event != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("child before Job close wait = %#x/%v, want running", event, err)
	}

	drainCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := process.DrainJob(drainCtx); err != nil {
		cancel()
		t.Fatalf("DrainJob() error = %v", err)
	}
	cancel()
	process.CloseAfterExit()
	deadline := time.Now().Add(5 * time.Second)
	for {
		event, waitErr := windows.WaitForSingleObject(handle, 0)
		if waitErr == nil && event == windows.WAIT_OBJECT_0 {
			return
		}
		if waitErr != nil {
			t.Fatalf("WaitForSingleObject(child after Job close) error = %v", waitErr)
		}
		if time.Now().After(deadline) {
			t.Fatal("TTY descendant survived explicit Job-tree drain")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
