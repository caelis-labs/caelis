package tuiapp

import (
	"fmt"
)

// renderMentionList renders the @mention candidates as an overlay list.
func (m *Model) renderMentionList() string {
	if len(m.mentionCandidates) == 0 {
		return ""
	}
	maxItems := minInt(8, len(m.mentionCandidates))
	var lines []string
	for i := range maxItems {
		prefix := "  "
		display := completionCandidateDisplay(m.mentionCandidates[i])
		if m.mentionPrefix != "#" {
			display = m.mentionPrefix + display
		}
		detail := completionCandidateDetail(m.mentionCandidates[i])
		if m.mentionPrefix == "#" {
			detail = ""
		}
		if i == m.mentionIndex {
			prefix = m.theme.PromptStyle().Render("▸ ")
			line := prefix + m.theme.CommandActiveStyle().Render(display)
			if detail != "" {
				line += "  " + m.theme.HelpHintTextStyle().Render(detail)
			}
			lines = append(lines, line)
		} else {
			line := prefix + m.theme.HelpHintTextStyle().Render(display)
			if detail != "" {
				line += "  " + m.theme.HelpHintTextStyle().Render(detail)
			}
			lines = append(lines, line)
		}
	}
	if len(m.mentionCandidates) > maxItems {
		lines = append(lines, m.theme.HelpHintTextStyle().Render(
			fmt.Sprintf("  … and %d more", len(m.mentionCandidates)-maxItems),
		))
	}
	title := "Agents"
	if m.mentionPrefix == "#" {
		title = "Files"
	}
	return m.renderCompletionOverlay(title, lines)
}
