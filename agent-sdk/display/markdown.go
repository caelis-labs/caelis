package display

import "strings"

// CleanSubagentFinalOutput normalizes transport line endings without
// interpreting the Final Message as display markup. Final Messages are
// canonical user-visible content, so Markdown structure, blank lines, and
// repeated text must survive unchanged.
func CleanSubagentFinalOutput(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
}
