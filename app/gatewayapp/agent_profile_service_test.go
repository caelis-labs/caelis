package gatewayapp

import (
	"testing"

	"github.com/caelis-labs/caelis/ports/agentprofile"
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

func TestReviewSubagentPromptForExternalACPScopesUserPrompt(t *testing.T) {
	prompt, offset := ReviewSubagentPromptForProfileTarget("  focus on auth  ", agentprofile.BindingTargetACP)
	prefix := "Review request:\nReview the current workspace changes, including staged, unstaged, and untracked files.\n\nUser review instructions:\n"
	if prompt != prefix+"focus on auth" {
		t.Fatalf("ReviewSubagentPromptForProfileTarget() prompt = %q, want external review scope plus instructions", prompt)
	}
	if offset != len([]rune(prefix)) {
		t.Fatalf("ReviewSubagentPromptForProfileTarget() offset = %d, want %d", offset, len([]rune(prefix)))
	}
}

func TestReviewSubagentPromptForExternalACPUsesBuiltInReviewRequestWhenEmpty(t *testing.T) {
	prompt, offset := ReviewSubagentPromptForProfileTarget("   ", agentprofile.BindingTargetACP)
	want := "Review request:\n" + reviewSubagentWorkspaceScopePrompt
	if prompt != want {
		t.Fatalf("ReviewSubagentPromptForProfileTarget() prompt = %q, want built-in review request", prompt)
	}
	if offset != len([]rune(prompt)) {
		t.Fatalf("ReviewSubagentPromptForProfileTarget() offset = %d, want prompt length %d", offset, len([]rune(prompt)))
	}
}
