//go:build !windows

package gatewayapp

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDefaultManagedACPInstallerCancellationKillsNPMProcessGroup(t *testing.T) {
	binDir := t.TempDir()
	readyFIFO := filepath.Join(t.TempDir(), "npm-ready")
	if err := syscall.Mkfifo(readyFIFO, 0o600); err != nil {
		t.Fatalf("create fake npm readiness pipe: %v", err)
	}
	readyReader, err := os.OpenFile(readyFIFO, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open fake npm readiness pipe: %v", err)
	}
	t.Cleanup(func() { _ = readyReader.Close() })
	ready := make(chan fakeNPMReady, 1)
	go readFakeNPMReady(readyReader, ready)

	script := "#!/bin/sh\n" +
		"sleep 30 &\n" +
		"child=$!\n" +
		"printf '%s\\n' \"$child\" > \"$CAELIS_TEST_NPM_READY_FIFO\"\n" +
		"wait\n"
	if err := os.WriteFile(filepath.Join(binDir, "npm"), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake npm: %v", err)
	}
	t.Setenv("CAELIS_TEST_NPM_READY_FIFO", readyFIFO)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	installRoot := t.TempDir()
	cacheRoot := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := defaultRunBuiltinACPAgentNPMInstall(ctx, builtinACPAgentNPMInstallRequest{
			Root: installRoot, CacheRoot: cacheRoot, AdapterID: "test", InstallSpec: "@example/acp@1.0.0",
		})
		done <- err
	}()

	var childPID int
	select {
	case result := <-ready:
		if result.err != nil || result.childPID <= 0 {
			cancel()
			waitForCanceledFakeNPM(t, done)
			t.Fatalf("fake npm readiness handshake failed: pid=%d err=%v", result.childPID, result.err)
		}
		childPID = result.childPID
	case err := <-done:
		cancel()
		t.Fatalf("installer exited before fake npm reported ready: %v", err)
	case <-time.After(10 * time.Second):
		cancel()
		waitForCanceledFakeNPM(t, done)
		t.Fatal("fake npm did not report ready within 10 seconds")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("default installer error = %v, want context canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("default installer did not return after cancellation")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(childPID, 0); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("npm child process %d survived cancellation", childPID)
}

type fakeNPMReady struct {
	childPID int
	err      error
}

func readFakeNPMReady(reader *os.File, ready chan<- fakeNPMReady) {
	scanner := bufio.NewScanner(reader)
	if !scanner.Scan() {
		err := scanner.Err()
		if err == nil {
			err = errors.New("readiness pipe closed before a child pid was written")
		}
		ready <- fakeNPMReady{err: err}
		return
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
	ready <- fakeNPMReady{childPID: childPID, err: err}
}

func waitForCanceledFakeNPM(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("default installer did not return after test cleanup cancellation")
	}
}
