package session

import "strings"

// StructuredDiffText summarizes affected files and includes the supplied patch
// verbatim. It never interprets patch text as complete file contents or renders
// arbitrary metadata as user-visible text.
func StructuredDiffText(changes *[]ProtocolDiffChange, patch *ProtocolDiffPatch) string {
	var parts []string
	if changes != nil {
		for _, change := range *changes {
			label := change.Operation + " " + change.Path
			if change.OldPath != nil {
				label += " (from " + *change.OldPath + ")"
			}
			parts = append(parts, label)
		}
	}
	if patch != nil && patch.Text != "" {
		parts = append(parts, patch.Text)
	}
	return strings.Join(parts, "\n")
}
