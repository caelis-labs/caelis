package tuiapp

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

func (m *Model) ensurePanelAnimationTick() tea.Cmd {
	if m == nil {
		return nil
	}
	if m.panelAnimationTickScheduled || !m.hasPendingPanelAnimations() {
		return nil
	}
	m.panelAnimationTickScheduled = true
	return frameTickCmd(frameTickPanelAnimation, m.streamTickInterval())
}

func (m *Model) hasPendingPanelAnimations() bool {
	if m == nil || m.doc == nil {
		return false
	}
	for _, block := range m.doc.Blocks() {
		switch panel := block.(type) {
		case *SubagentPanelBlock:
			if panel != nil && (!panel.CollapseAt.IsZero() || !panel.CollapseFrom.IsZero()) {
				return true
			}
		}
	}
	return false
}

func (m *Model) advancePanelAnimations(now time.Time) tea.Cmd {
	if m == nil {
		return nil
	}
	m.panelAnimationTickScheduled = false
	if m.doc == nil {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	changed := false
	for _, block := range m.doc.Blocks() {
		switch panel := block.(type) {
		case *SubagentPanelBlock:
			if !subagentHasInlineAnchor(m, panel) {
				continue
			}
			if advanceInlineCollapse(&panel.CollapseAt, &panel.CollapseFrom, &panel.CollapseFor, &panel.VisibleLines, &panel.Expanded, subagentOutputPreviewLines, now) {
				m.syncInlineSubagentAnchorState(panel)
				changed = true
			}
		}
	}
	if changed {
		m.syncViewportContent()
	}
	if !m.hasPendingPanelAnimations() {
		return nil
	}
	m.panelAnimationTickScheduled = true
	return frameTickCmd(frameTickPanelAnimation, m.streamTickInterval())
}

func scheduleInlineCollapse(collapseAt *time.Time, collapseFrom *time.Time, collapseFor *time.Duration, visibleLines *int, startedAt time.Time, defaultLines int, now time.Time) {
	if collapseAt == nil || collapseFrom == nil || collapseFor == nil || visibleLines == nil {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	if *collapseFor <= 0 {
		*collapseFor = inlinePanelCollapseDuration
	}
	readyAt := startedAt.Add(inlinePanelMinVisibleDuration)
	if readyAt.Before(now) {
		readyAt = now
	}
	*collapseAt = readyAt
	*collapseFrom = time.Time{}
	if defaultLines > 0 {
		*visibleLines = defaultLines
	}
}

func cancelInlineCollapse(collapseAt *time.Time, collapseFrom *time.Time, visibleLines *int) {
	if collapseAt != nil {
		*collapseAt = time.Time{}
	}
	if collapseFrom != nil {
		*collapseFrom = time.Time{}
	}
	if visibleLines != nil {
		*visibleLines = 0
	}
}

func advanceInlineCollapse(collapseAt *time.Time, collapseFrom *time.Time, collapseFor *time.Duration, visibleLines *int, expanded *bool, defaultLines int, now time.Time) bool {
	if collapseAt == nil || collapseFrom == nil || collapseFor == nil || visibleLines == nil || expanded == nil {
		return false
	}
	if collapseAt.IsZero() && collapseFrom.IsZero() {
		return false
	}
	if !*expanded {
		cancelInlineCollapse(collapseAt, collapseFrom, visibleLines)
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}
	if collapseFrom.IsZero() {
		if now.Before(*collapseAt) {
			return false
		}
		*collapseFrom = now
		if *collapseFor <= 0 {
			*collapseFor = inlinePanelCollapseDuration
		}
		if defaultLines > 0 {
			*visibleLines = defaultLines
		}
		return true
	}
	if *collapseFor <= 0 || !now.Before(collapseFrom.Add(*collapseFor)) {
		*expanded = false
		cancelInlineCollapse(collapseAt, collapseFrom, visibleLines)
		return true
	}
	elapsed := now.Sub(*collapseFrom)
	remaining := *collapseFor - elapsed
	next := int((int64(remaining)*int64(defaultLines) + int64(*collapseFor) - 1) / int64(*collapseFor))
	if next < 1 {
		next = 1
	}
	if *visibleLines == next {
		return false
	}
	*visibleLines = next
	return true
}
