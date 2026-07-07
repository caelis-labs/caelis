package tuiapp

// renderMentionList renders the @mention candidates as an overlay list.
func (m *Model) renderMentionList() string {
	if len(m.mentionCandidates) == 0 {
		return ""
	}
	maxItems := minInt(completionOverlayVisibleItems, len(m.mentionCandidates))
	start, end := completionWindowRange(m.mentionIndex, len(m.mentionCandidates), maxItems)
	lines := make([]string, 0, end-start)
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
	title := "Agents"
	if m.mentionPrefix == "#" {
		title = "Files"
	}
	return m.renderCompletionOverlay(title, lines)
}
