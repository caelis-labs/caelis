package kernel

import "github.com/OnslaughtSnail/caelis/internal/displaypolicy"

// CleanSubagentFinalOutput normalizes markdown-heavy ACP final text into a
// dense terminal-panel summary. It intentionally stays small and lossy: the
// child transcript remains the source of rich formatting.
func CleanSubagentFinalOutput(text string) string {
	return displaypolicy.CleanSubagentFinalOutput(text)
}
