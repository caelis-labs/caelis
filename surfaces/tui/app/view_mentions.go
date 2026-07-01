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
			fmt.Sprintf("… and %d earlier", start),
		))
	}
	for i := start; i < end; i++ {
		display := completionCandidateDisplay(m.mentionCandidates[i])
		if m.mentionPrefix != "#" {
			display = m.mentionPrefix + display
		}
		detail := completionCandidateDetail(m.mentionCandidates[i])
		if m.mentionPrefix == "#" {
			detail = ""
		}
		lines = append(lines, m.renderCompletionTextLine(display, detail, i == m.mentionIndex))
	}
	if end < len(m.mentionCandidates) {
		lines = append(lines, m.theme.HelpHintTextStyle().Render(
			fmt.Sprintf("… and %d more", len(m.mentionCandidates)-end),
		))
	}
	title := "Agents"
	if m.mentionPrefix == "#" {
		title = "Files"
	}
	return m.renderCompletionOverlay(title, lines)
}
