package controlprompt

import "strings"

const reviewWorkspaceScopePrompt = "Review the current workspace changes (staged, unstaged, and untracked). Lead with concrete findings; consider correctness, regressions, maintainability, architecture, and test coverage."

const reviewerSceneInstructions = `You are Caelis' code reviewer. Stay scoped to the requested workspace change, prioritize high-confidence findings, and do not change code unless explicitly asked.`

// ReviewPrompt returns the model-visible /review prompt and the rune offset
// where user-provided instructions begin after the fixed workspace scope.
func ReviewPrompt(instructions string) (string, int) {
	base := reviewerSceneInstructions + "\n\n" + reviewWorkspaceScopePrompt
	instructions = strings.TrimSpace(instructions)
	if instructions == "" {
		return base, len([]rune(base))
	}
	prefix := base + "\n\nAdditional review instructions:\n"
	return prefix + instructions, len([]rune(prefix))
}
