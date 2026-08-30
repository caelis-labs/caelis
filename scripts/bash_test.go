package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testBash(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		path, err := exec.LookPath("bash")
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Fatalf("resolve Git Bash: %v", err)
	}
	path := filepath.Clean(filepath.Join(strings.TrimSpace(string(execPath)), "..", "..", "..", "bin", "bash.exe"))
	if _, err := exec.LookPath(path); err != nil {
		t.Fatalf("resolve Git Bash %q: %v", path, err)
	}
	return path
}
