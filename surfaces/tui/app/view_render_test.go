package tuiapp

import (
	"strings"
	"testing"
)

func TestFitHeaderRowPartsPreservesWorkspaceGitBranch(t *testing.T) {
	t.Parallel()

	workspace := `D:\xue\code\storage [⎇ feature/windows-paths*]`
	model := "xiaomi/mimo-v2.5-pro [high]"
	left, right := fitHeaderRowParts(58, workspace, model)

	if !strings.Contains(left, "[⎇ ") || !strings.Contains(left, "*]") {
		t.Fatalf("left header = %q, want visible git branch", left)
	}
	if right == "" {
		t.Fatalf("right header is empty, want model text preserved")
	}
}
