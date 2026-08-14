package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestGoTestNonemptyRejectsEmptyAndUnmatchedSelectors(t *testing.T) {
	t.Parallel()

	for _, selector := range []string{"", "^TestDefinitelyDoesNotExist$"} {
		cmd := exec.Command("bash", "./go_test_nonempty.sh", "./markdown_links", selector, "contract-test")
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("selector %q unexpectedly passed: %s", selector, output)
		}
		if !strings.Contains(string(output), "go-test-nonempty") {
			t.Fatalf("selector %q output = %q, want explicit non-empty failure", selector, output)
		}
	}
}
