package main

import (
	"os/exec"
	"runtime"
	"testing"
)

func TestBuiltinDTWPackagePreparation(t *testing.T) {
	python := "python3"
	if runtime.GOOS == "windows" {
		python = "python"
	}
	command := exec.Command(python, "-m", "unittest", "test_prepare_builtin_dtw.py")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("pinned package verification: %v\n%s", err, output)
	}
}
