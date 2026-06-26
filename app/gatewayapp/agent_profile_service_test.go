package gatewayapp

import (
	"strings"
	"testing"
)

func TestReviewSubagentPromptScopesWorkspaceReview(t *testing.T) {
	prompt, offset := ReviewSubagentPrompt("  focus on auth  ")
	prefix := "Review the current workspace changes, including staged, unstaged, and untracked files.\n\nAdditional review instructions:\n"
	if prompt != prefix+"focus on auth" {
		t.Fatalf("ReviewSubagentPrompt() prompt = %q, want canonical workspace scope plus instructions", prompt)
	}
	if offset != len([]rune(prefix)) {
		t.Fatalf("ReviewSubagentPrompt() offset = %d, want %d", offset, len([]rune(prefix)))
	}
	if strings.Contains(prompt, "$review") {
		t.Fatalf("ReviewSubagentPrompt() prompt = %q, should not duplicate profile skill instructions", prompt)
	}
}

func TestReviewSubagentPromptWithoutInstructions(t *testing.T) {
	prompt, offset := ReviewSubagentPrompt("   ")
	if prompt != "Review the current workspace changes, including staged, unstaged, and untracked files." {
		t.Fatalf("ReviewSubagentPrompt() prompt = %q, want canonical workspace scope", prompt)
	}
	if offset != len([]rune(prompt)) {
		t.Fatalf("ReviewSubagentPrompt() offset = %d, want prompt length %d", offset, len([]rune(prompt)))
	}
}
