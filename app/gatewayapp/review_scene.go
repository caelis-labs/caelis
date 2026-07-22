package gatewayapp

import (
	"strings"

	"github.com/caelis-labs/caelis/internal/kernel"
)

const guardianSceneID = "guardian"

// ReviewerAgentID is the hidden Control-owned Agent used by /review.
const ReviewerAgentID = "reviewer"

const reviewWorkspaceScopePrompt = "Strictly review the current workspace changes (staged, unstaged, and untracked). Use the $review skill. Findings-first: correctness, regressions, bloat, design smells, boundary drift, and test gaps."

const reviewerSceneInstructions = `You are Caelis' fixed code-review scene.

Load and follow the $review skill for methodology, priority order, and output format. Stay scoped to the requested workspace change.

Be demanding about quality, not only correctness:
- Lead with findings and prefer high-conviction issues over long nit lists.
- Flag bugs, regressions, security or permission risks, and missing tests for risky paths.
- Flag code bloat, wrong-layer logic, god-file expansion, thin wrappers, and missed simplifications.
- Do not approve merely because tests pass when the surrounding design became less coherent.
- Do not make code changes unless the request explicitly asks for fixes.

Default to analysis only. Keep residual summary secondary to findings.`

func (s *Stack) newModelApprovalReviewer() kernel.ApprovalReviewer {
	return newModelApprovalReviewer(s.Sessions)
}

// ReviewPrompt returns the model-visible /review prompt and the rune offset
// where user-provided instructions begin after the fixed workspace scope.
func ReviewPrompt(instructions string) (string, int) {
	base := reviewWorkspaceScopePrompt
	instructions = strings.TrimSpace(instructions)
	if instructions == "" {
		return base, len([]rune(base))
	}
	prefix := base + "\n\nAdditional review instructions:\n"
	return prefix + instructions, len([]rune(prefix))
}

func fixedReviewerSystemPrompt(base string) string {
	parts := make([]string, 0, 2)
	if base = strings.TrimSpace(base); base != "" {
		parts = append(parts, base)
	}
	parts = append(parts, reviewerSceneInstructions)
	return strings.Join(parts, "\n\n")
}
