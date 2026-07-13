//go:build windows

package file

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestWindowsSessionStoreRootLockBlocksAcrossProcesses(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=TestWindowsSessionStoreRootLockHelper$", "--", root)
	cmd.Env = append(os.Environ(), "CAELIS_SESSION_LOCK_HELPER=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe() error = %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read helper lock signal: %v", err)
	}
	if strings.TrimSpace(line) != "locked" {
		t.Fatalf("helper output = %q, want locked", line)
	}

	acquired := make(chan error, 1)
	go func() {
		file, err := lockSessionStoreRoot(context.Background(), root, storeRootLockExclusive)
		if err == nil {
			err = unlockSessionStoreRoot(file)
		}
		acquired <- err
	}()

	select {
	case err := <-acquired:
		t.Fatalf("exclusive lock acquired while helper still held it: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper Wait() error = %v", err)
	}
	cmd.Process = nil

	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("lock after helper exit error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for exclusive lock after helper exit")
	}
}

func TestWindowsSessionStoreRootLockHelper(t *testing.T) {
	if os.Getenv("CAELIS_SESSION_LOCK_HELPER") != "1" {
		return
	}
	root := os.Args[len(os.Args)-1]
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	file, err := lockSessionStoreRoot(context.Background(), root, storeRootLockExclusive)
	if err != nil {
		t.Fatalf("lockSessionStoreRoot() error = %v", err)
	}
	fmt.Println("locked")
	time.Sleep(500 * time.Millisecond)
	if err := unlockSessionStoreRoot(file); err != nil {
		t.Fatalf("unlockSessionStoreRoot() error = %v", err)
	}
}
