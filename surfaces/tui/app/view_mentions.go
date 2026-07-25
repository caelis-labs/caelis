package tuiapp

// renderMentionList renders @file candidates as an overlay list.
func (m *Model) renderMentionList() string {
	if len(m.mentionCandidates) == 0 {
		return ""
	}
	maxItems := minInt(completionOverlayVisibleItems, len(m.mentionCandidates))
	start, end := completionWindowRange(m.mentionIndex, len(m.mentionCandidates), maxItems)
	lines := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		display := completionCandidateDisplay(m.mentionCandidates[i])
		lines = append(lines, m.renderCompletionTextLine(display, "", i == m.mentionIndex))
	}
	return m.renderCompletionOverlay("Files", lines)
}
