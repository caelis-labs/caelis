package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

type subagentRosterFooterBounds struct {
	x     int
	y     int
	width int
}

func (m *Model) handleSubagentRosterOverlayKey(msg tea.KeyMsg) tea.Cmd {
	state := m.subagentRosterOverlay
	if state == nil {
		return nil
	}
	if _, ok := msg.(tea.KeyReleaseMsg); ok {
		return nil
	}
	keyEvent := msg.Key()
	switch keyEvent.Code {
	case tea.KeyEscape:
		m.closeSubagentRosterOverlay()
	case tea.KeyTab, tea.KeyDown:
		m.moveSubagentRosterSelection(1)
	case tea.KeyUp:
		m.moveSubagentRosterSelection(-1)
	case tea.KeyEnter:
		m.activateSubagentRosterSelection()
	default:
		switch strings.ToLower(keyEvent.Text) {
		case "j":
			m.moveSubagentRosterSelection(1)
		case "k":
			m.moveSubagentRosterSelection(-1)
		}
	}
	return nil
}

func (m *Model) moveSubagentRosterSelection(delta int) {
	state := m.subagentRosterOverlay
	if state == nil || len(state.rows) == 0 || delta == 0 {
		return
	}
	state.index = (state.index + delta + len(state.rows)) % len(state.rows)
	state.selectedCallID = state.rows[state.index].callID
	state.pressedCallID = ""
}

func (m *Model) activateSubagentRosterSelection() bool {
	state := m.subagentRosterOverlay
	if state == nil || state.index < 0 || state.index >= len(state.rows) {
		return false
	}
	row := state.rows[state.index]
	return m.openSubagentOutputOverlayView(row.callID, m.subagentOutputViews[row.callID])
}

func (m *Model) handleSubagentRosterOverlayMouse(msg tea.MouseMsg) (bool, tea.Cmd) {
	state := m.subagentRosterOverlay
	if state == nil {
		return false, nil
	}
	mouse := msg.Mouse()
	switch msg.(type) {
	case tea.MouseClickMsg:
		if handled, cmd := m.handleSubagentRosterFooterMouse(mouse, mousePhasePress); handled {
			return true, cmd
		}
	case tea.MouseMotionMsg:
		if handled, cmd := m.handleSubagentRosterFooterMouse(mouse, mousePhaseMotion); handled {
			return true, cmd
		}
	case tea.MouseReleaseMsg:
		if handled, cmd := m.handleSubagentRosterFooterMouse(mouse, mousePhaseRelease); handled {
			return true, cmd
		}
	}
	geometry := state.geometry
	inside := mouse.X >= geometry.x && mouse.X < geometry.x+geometry.width &&
		mouse.Y >= geometry.y && mouse.Y < geometry.y+geometry.height
	switch msg.(type) {
	case tea.MouseWheelMsg:
		if inside {
			switch mouse.Button {
			case tea.MouseWheelUp:
				m.moveSubagentRosterSelection(-1)
			case tea.MouseWheelDown:
				m.moveSubagentRosterSelection(1)
			}
		}
		return true, nil
	case tea.MouseMotionMsg:
		if index := subagentRosterRowAtY(geometry.rows, mouse.Y); index >= 0 && index < len(state.rows) {
			state.index = index
			state.selectedCallID = state.rows[index].callID
		}
		return true, nil
	case tea.MouseClickMsg:
		if mouse.Button != tea.MouseLeft || !inside {
			state.pressedCallID = ""
			return true, nil
		}
		if mouse.Y == geometry.closeY && mouse.X >= geometry.closeX-1 && mouse.X <= geometry.closeX+1 {
			state.pressedCallID = "close"
			return true, nil
		}
		if index := subagentRosterRowAtY(geometry.rows, mouse.Y); index >= 0 && index < len(state.rows) {
			state.index = index
			state.selectedCallID = state.rows[index].callID
			state.pressedCallID = state.rows[index].callID
		} else {
			state.pressedCallID = ""
		}
		return true, nil
	case tea.MouseReleaseMsg:
		pressed := state.pressedCallID
		state.pressedCallID = ""
		if pressed == "close" && mouse.Y == geometry.closeY && mouse.X >= geometry.closeX-1 && mouse.X <= geometry.closeX+1 {
			m.closeSubagentRosterOverlay()
			return true, nil
		}
		if index := subagentRosterRowAtY(geometry.rows, mouse.Y); index >= 0 && index < len(state.rows) {
			state.index = index
			state.selectedCallID = state.rows[index].callID
			if pressed == state.rows[index].callID {
				m.activateSubagentRosterSelection()
			}
		}
		return true, nil
	default:
		return true, nil
	}
}

func subagentRosterRowAtY(rows []subagentRosterRowGeometry, y int) int {
	for index, row := range rows {
		if row.top >= 0 && y >= row.top && y <= row.bottom {
			return index
		}
	}
	return -1
}

func (m *Model) handleSubagentRosterFooterMouse(mouse tea.Mouse, phase mousePhase) (bool, tea.Cmd) {
	if m == nil || m.activePrompt != nil || m.subagentOutputOverlay != nil || m.subagentOverlay != nil {
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
			if m.subagentRosterOverlay != nil {
				m.closeSubagentRosterOverlay()
				return true, nil
			}
			if m.openSubagentRosterOverlay() {
				return true, tea.Batch(m.requestSubagentRosterRefresh(), m.resumeRunningAnimationIfNeeded())
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
