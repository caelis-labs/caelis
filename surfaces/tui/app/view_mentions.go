package tuiapp

func (m *Model) renderMentionListGeometry(geometry completionOverlayGeometry, candidates []CompletionCandidate) string {
	lines := make([]string, 0, geometry.candidateCount)
	for i := geometry.windowStart; i < geometry.windowEnd; i++ {
		display := completionCandidateDisplay(candidates[i])
		lines = append(lines, m.renderCompletionTextLine(display, "", i == geometry.selected))
	}
	return m.renderCompletionOverlay(geometry, lines)
}
