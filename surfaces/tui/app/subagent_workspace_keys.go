package tuiapp

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/uipreferences"
)

// Workspace commands are available from either composer, below modal input
// owners. Tab and Shift+Tab retain their existing main-composer behavior.
func (m *Model) handlePaneWorkspaceKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	if state := m.subagentOutputOverlay; state != nil {
		state.hoveredHeader = ""
		state.footerHideHovered = false
	}
	if m.workspace.dragging {
		if msg.Key().Code == tea.KeyEscape {
			m.cancelPaneResize()
		}
		return true, nil
	}
	if m.workspace.resizing {
		return true, m.handlePaneResizeKey(msg)
	}
	state := m.subagentOutputOverlay
	if state != nil && state.menu != "" {
		return true, m.handlePaneMenuKey(msg)
	}
	switch {
	case key.Matches(msg, m.keys.PaneAgents):
		if !m.openSubagentWorkspace() {
			return false, nil
		}
		m.openPaneMenu("agents")
		return true, m.resumeRunningAnimationIfNeeded()
	case key.Matches(msg, m.keys.PaneLayout):
		if state == nil {
			return false, nil
		}
		m.openPaneMenu("layout")
		return true, nil
	case key.Matches(msg, m.keys.PaneToggle):
		if state != nil {
			m.closeSubagentOutputOverlay()
			return true, nil
		}
		if !m.openSubagentWorkspace() {
			return false, nil
		}
		return true, m.resumeRunningAnimationIfNeeded()
	case key.Matches(msg, m.keys.PaneFocus):
		if state == nil {
			if !m.openSubagentWorkspace() {
				return false, nil
			}
			return true, m.resumeRunningAnimationIfNeeded()
		}
		if m.workspaceLayout().split {
			m.workspace.childFocused = !m.workspace.childFocused
			m.clearInputOverlays()
		}
		return true, nil
	}
	return false, nil
}

func (m *Model) beginPaneResize() {
	if !m.workspaceLayout().split {
		return
	}
	m.workspace.resizeRatio = m.paneSplitRatio()
	m.workspace.resizing = true
	m.workspace.childFocused = true
	m.clearSubagentOutputSelection()
}

func (m *Model) cancelPaneResize() {
	m.workspace.resizing = false
	m.workspace.dragging = false
}

func (m *Model) handlePaneResizeKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.Key().Code {
	case tea.KeyEscape:
		m.cancelPaneResize()
	case tea.KeyEnter:
		m.workspace.resizing = false
		return m.applyPaneResize()
	case tea.KeyLeft, tea.KeyRight, tea.KeyUp, tea.KeyDown:
		layout := m.workspaceLayout()
		if !layout.split {
			m.cancelPaneResize()
			return nil
		}
		p := m.uiPreferences.value.WithDefaults()
		horizontal := p.SubagentLayout == uipreferences.Left || p.SubagentLayout == uipreferences.Right
		code := msg.Key().Code
		if horizontal != (code == tea.KeyLeft || code == tea.KeyRight) {
			return nil
		}
		delta := 5
		if code == tea.KeyLeft || code == tea.KeyUp {
			delta = -delta
		}
		if p.SubagentLayout == uipreferences.Left || p.SubagentLayout == uipreferences.Up {
			delta = -delta
		}
		axis, minimum := m.width-1, workspaceMinPaneWidth
		if !horizontal {
			axis, minimum = m.height-1, workspaceMinPaneHeight
		}
		lo := maxInt(uipreferences.MinRatio, (minimum*100+axis-1)/axis)
		hi := minInt(uipreferences.MaxRatio, (axis-minimum)*100/axis)
		m.workspace.resizeRatio = clampInt(m.workspace.resizeRatio+delta, lo, hi)
	}
	return nil
}

func (m *Model) paneSplitRatio() int {
	p := m.uiPreferences.value.WithDefaults()
	if p.SubagentLayout == uipreferences.Left || p.SubagentLayout == uipreferences.Right {
		return p.HorizontalRatio
	}
	return p.VerticalRatio
}

func (m *Model) paneResizePreferences() uipreferences.Preferences {
	p := m.uiPreferences.value.WithDefaults()
	if p.SubagentLayout == uipreferences.Left || p.SubagentLayout == uipreferences.Right {
		p.HorizontalRatio = m.workspace.resizeRatio
	} else {
		p.VerticalRatio = m.workspace.resizeRatio
	}
	return p
}

// Preview never mutates persisted preferences or transcript widths. Apply once
// when the gesture is complete, so long histories do not reflow per mouse cell.
func (m *Model) applyPaneResize() tea.Cmd {
	p := m.paneResizePreferences()
	if p == m.uiPreferences.value {
		return nil
	}
	update := uipreferences.Preferences{}
	if p.SubagentLayout == uipreferences.Left || p.SubagentLayout == uipreferences.Right {
		update.HorizontalRatio = p.HorizontalRatio
	} else {
		update.VerticalRatio = p.VerticalRatio
	}
	cmd := m.setUIPreferences(update)
	m.resizeWorkspace()
	return cmd
}
