package main

import (
	"os/exec"
	"testing"
)

func TestBuiltinDTWPackagePreparation(t *testing.T) {
	command := exec.Command("python3", "-m", "unittest", "test_prepare_builtin_dtw.py")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("pinned package verification: %v\n%s", err, output)
	}
}
