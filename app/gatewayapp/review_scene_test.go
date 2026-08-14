package gatewayapp

import (
	"strings"
	"testing"
)

func TestReviewPromptScopesWorkspaceReviewWithoutRequiringASkill(t *testing.T) {
	prompt, offset := ReviewPrompt("  focus on auth  ")
	prefix := reviewerSceneInstructions + "\n\n" + reviewWorkspaceScopePrompt + "\n\nAdditional review instructions:\n"
	if prompt != prefix+"focus on auth" {
		t.Fatalf("ReviewPrompt() prompt = %q, want fixed scope plus instructions", prompt)
	}
	if offset != len([]rune(prefix)) {
		t.Fatalf("ReviewPrompt() offset = %d, want %d", offset, len([]rune(prefix)))
	}
	if !strings.Contains(prompt, "do not change code unless explicitly asked") {
		t.Fatalf("ReviewPrompt() lost fixed reviewer safety scene: %q", prompt)
	}
	if strings.Contains(strings.ToLower(prompt), "skill") {
		t.Fatalf("ReviewPrompt() still requires a Skill: %q", prompt)
	}
}
