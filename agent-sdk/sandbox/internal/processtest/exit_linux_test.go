package processtest

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestExitedFailsWhenProcDoesNotExposeLivePID(t *testing.T) {
	stopped, err := exitedWithStatReader(os.Getpid(), func(string) ([]byte, error) {
		return nil, os.ErrNotExist
	})
	if stopped || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("live PID without readable stat = %t, %v", stopped, err)
	}
}

func TestExitedDistinguishesLiveProcessAndUnreapedChild(t *testing.T) {
	if stopped, err := exited(os.Getpid()); err != nil || stopped {
		t.Fatalf("live test process = %t, %v", stopped, err)
	}
	child := exec.Command("/bin/sh", "-c", "exit 0")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Wait() })
	if err := WaitForExit(child.Process.Pid, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	// Deliberately withhold Wait: the PID still exists, but cannot execute.
	if err := child.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("expected unreaped child PID to exist: %v", err)
	}
	if stopped, err := exited(child.Process.Pid); err != nil || !stopped {
		t.Fatalf("unreaped child = %t, %v", stopped, err)
	}
}
