//go:build !windows

package gatewayapp

import (
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
	marker := filepath.Join(t.TempDir(), "child-pid")
	script := "#!/bin/sh\n" +
		"sleep 30 &\n" +
		"child=$!\n" +
		"printf '%s' \"$child\" > \"$CAELIS_TEST_NPM_CHILD_PID\"\n" +
		"wait\n"
	if err := os.WriteFile(filepath.Join(binDir, "npm"), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake npm: %v", err)
	}
	t.Setenv("CAELIS_TEST_NPM_CHILD_PID", marker)
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
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(marker)
		if err == nil {
			childPID, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
			if childPID > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID <= 0 {
		cancel()
		t.Fatal("fake npm did not publish its child pid")
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
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(childPID, 0); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("npm child process %d survived cancellation", childPID)
}
