package tuiapp

import (
	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

type subagentRosterFooterBounds struct {
	x     int
	y     int
	width int
}

func (m *Model) handleSubagentRosterFooterMouse(mouse tea.Mouse, phase mousePhase) (bool, tea.Cmd) {
	if m == nil || m.activePrompt != nil || m.subagentOverlay != nil {
		return false, nil
	}
	bounds, ok := m.subagentRosterFooterHitBounds()
	inside := ok && mouse.X >= bounds.x && mouse.X < bounds.x+bounds.width &&
		m.screenYToFrameY(mouse.Y) == bounds.y
	switch phase {
	case mousePhasePress:
		m.subagentRosterPressed = inside && mouse.Button == tea.MouseLeft
		if !m.subagentRosterPressed {
			return false, nil
		}
		m.clearSelection()
		m.clearInputSelection()
		m.clearFixedSelection()
		return true, nil
	case mousePhaseMotion:
		return m.subagentRosterPressed, nil
	case mousePhaseRelease:
		if !m.subagentRosterPressed {
			return false, nil
		}
		m.subagentRosterPressed = false
		if inside {

			if m.openSubagentWorkspace() {
				return true, tea.Batch(m.ensureSubagentDirectoryWatch(), m.resumeRunningAnimationIfNeeded())
			}
			return true, nil
		}
		return true, nil
	default:
		return false, nil
	}
}

func (m *Model) subagentRosterFooterHitBounds() (subagentRosterFooterBounds, bool) {
	if m == nil {
		return subagentRosterFooterBounds{}, false
	}
	width := m.fixedRowContentWidth()
	_, subagents, right := m.footerParts(width)
	if subagents == "" {
		return subagentRosterFooterBounds{}, false
	}
	rightGroupWidth := displayColumns(subagents)
	if right != "" {
		rightGroupWidth += 2 + displayColumns(right)
	}
	start := width - rightGroupWidth
	return subagentRosterFooterBounds{
		x:     m.mainColumnX() + tuikit.StatusInset + start,
		y:     m.fixedRowLayout().footerY,
		width: displayColumns(subagents),
	}, true
}
