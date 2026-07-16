package gatewayapp

import (
	"strings"
	"testing"
)

func TestReviewPromptScopesWorkspaceReview(t *testing.T) {
	prompt, offset := ReviewPrompt("  focus on auth  ")
	prefix := reviewWorkspaceScopePrompt + "\n\nAdditional review instructions:\n"
	if prompt != prefix+"focus on auth" {
		t.Fatalf("ReviewPrompt() prompt = %q, want fixed scope plus instructions", prompt)
	}
	if offset != len([]rune(prefix)) {
		t.Fatalf("ReviewPrompt() offset = %d, want %d", offset, len([]rune(prefix)))
	}
}

func TestFixedReviewerSystemPromptPreservesBasePrompt(t *testing.T) {
	prompt := fixedReviewerSystemPrompt("base system prompt")
	if !strings.HasPrefix(prompt, "base system prompt\n\n") || !strings.Contains(prompt, reviewerSceneInstructions) {
		t.Fatalf("fixedReviewerSystemPrompt() = %q", prompt)
	}
}
