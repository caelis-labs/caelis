package tuiapp

import (
	"fmt"
)

// renderMentionList renders the @mention candidates as an overlay list.
func (m *Model) renderMentionList() string {
	if len(m.mentionCandidates) == 0 {
		return ""
	}
	maxItems := minInt(completionOverlayVisibleItems, len(m.mentionCandidates))
	start, end := completionWindowRange(m.mentionIndex, len(m.mentionCandidates), maxItems)
	var lines []string
	if start > 0 {
		lines = append(lines, m.theme.HelpHintTextStyle().Render(
			fmt.Sprintf("  … and %d earlier", start),
		))
	}
	for i := start; i < end; i++ {
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
			line := m.renderCompletionSelectedText(display)
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
	if end < len(m.mentionCandidates) {
		lines = append(lines, m.theme.HelpHintTextStyle().Render(
			fmt.Sprintf("  … and %d more", len(m.mentionCandidates)-end),
		))
	}
	title := "Agents"
	if m.mentionPrefix == "#" {
		title = "Files"
	}
	return m.renderCompletionOverlay(title, lines)
}
