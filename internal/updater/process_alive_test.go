package updater

import (
	"os"
	"os/exec"
	"testing"
)

func TestProcessExistsReportsCurrentAndExitedPIDs(t *testing.T) {
	if !processExists(os.Getpid()) {
		t.Fatal("processExists(self) = false")
	}
	if processExists(0) || processExists(-1) {
		t.Fatal("processExists(invalid) = true")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		t.Fatalf("probe process: %v", err)
	}
	if cmd.Process == nil {
		t.Fatal("probe process pid is missing")
	}
	if processExists(cmd.Process.Pid) {
		t.Fatalf("processExists(%d) = true after exit", cmd.Process.Pid)
	}
}
