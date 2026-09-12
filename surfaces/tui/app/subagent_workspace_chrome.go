package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func (m *Model) renderPaneTitle(view *subagentOutputView, width int) string {
	state := m.subagentOutputOverlay
	state.headerActions = nil
	if width <= 0 {
		return ""
	}
	closeText := truncateTailDisplay(" [x] ", width)
	closeWidth := displayColumns(closeText)
	right := m.paneHeaderStyle("close", m.theme.TextStyle()).Render(closeText)
	rightWidth := closeWidth
	layoutWidth := 0
	if width >= 12 && (m.paneLayoutAvailable("right") || m.paneLayoutAvailable("down")) {
		label := " " + paneLayoutSymbol(m.effectivePaneLayout()) + " "
		layoutWidth = displayColumns(label)
		right = m.paneHeaderStyle("layout", m.theme.HelpHintTextStyle()).Render(label) + " " + right
		rightWidth += layoutWidth + 1
	}
	leftWidth := maxInt(0, width-rightWidth-1)
	left := ""
	if leftWidth > 0 {
		style := m.theme.TitleStyle()
		if m.workspace.childFocused {
			style = style.Foreground(m.theme.CursorFg)
		}
		style = m.paneHeaderStyle("agents", style)
		handle, binding := subagentRosterMetadata(view)
		if handle == "" {
			handle = "Participant"
		}
		if leftWidth >= 3 {
			left = m.renderSubagentIdentity(subagentRosterRow{handle: handle, binding: binding}, leftWidth-2, style) + style.Bold(false).Render(" ≡")
		} else {
			left = style.Render("≡")
		}
		leftWidth = displayColumns(left)
		state.headerActions = append(state.headerActions, paneHeaderAction{0, leftWidth, "agents"})
	}
	if layoutWidth > 0 {
		state.headerActions = append(state.headerActions, paneHeaderAction{width - rightWidth, layoutWidth, "layout"})
	}
	state.headerActions = append(state.headerActions, paneHeaderAction{width - closeWidth, closeWidth, "close"})
	return left + strings.Repeat(" ", maxInt(0, width-displayColumns(left)-rightWidth)) + right
}

func (m *Model) paneHeaderStyle(action string, style lipgloss.Style) lipgloss.Style {
	state := m.subagentOutputOverlay
	if state.hoveredHeader == action || state.menu == action {
		return m.theme.SelectionStyle()
	}
	return style
}

func (m *Model) clearPaneChromeMouse() {
	if state := m.subagentOutputOverlay; state != nil {
		state.hoveredHeader, state.pressedItem = "", ""
		state.footerHideHovered = false
	}
}

func (m *Model) paneHeaderActionAt(mouse tea.Mouse) string {
	state := m.subagentOutputOverlay
	if mouse.Y != state.geometry.headerY {
		return ""
	}
	col := mouse.X - state.geometry.contentX
	for _, action := range state.headerActions {
		if col >= action.x && col < action.x+action.width {
			return action.value
		}
	}
	return ""
}

func (m *Model) paneFooterHideAt(mouse tea.Mouse) bool {
	state := m.subagentOutputOverlay
	col := mouse.X - state.geometry.contentX
	return mouse.Y == state.geometry.footerY && col >= state.footerHideX && col < state.footerHideX+state.footerHideWidth
}

// Track the chrome before routing the event, so leaving the child pane also
// clears its hover. Pointer motion never changes the focused composer.
func (m *Model) updatePaneChromeHover(msg tea.MouseMsg) {
	state := m.subagentOutputOverlay
	if state == nil {
		return
	}
	state.hoveredHeader = ""
	state.footerHideHovered = false
	if m.activePrompt != nil || m.subagentOverlay != nil || m.workspace.dragging || m.workspace.resizing || state.menu != "" || state.selecting || state.editorSelecting {
		return
	}
	if _, motion := msg.(tea.MouseMotionMsg); motion && msg.Mouse().Button == tea.MouseNone {
		state.hoveredHeader = m.paneHeaderActionAt(msg.Mouse())
		state.footerHideHovered = m.paneFooterHideAt(msg.Mouse())
	}
}

func (m *Model) renderPaneTooltip(base string) string {
	state := m.subagentOutputOverlay
	if state == nil || state.hoveredHeader == "" || state.menu != "" || m.activePrompt != nil || m.subagentOverlay != nil || m.workspace.dragging || m.workspace.resizing {
		return base
	}
	label := ""
	switch state.hoveredHeader {
	case "agents":
		label = m.paneKeyHint(m.keys.PaneAgents)
	case "layout":
		label = m.paneKeyHint(m.keys.PaneLayout) + " · " + string(m.effectivePaneLayout())
	case "close":
		label = m.paneKeyHint(m.keys.PaneToggle)
	}
	layout := m.subagentOutputLayout(state)
	if label == "" || layout.frameHeight < 3 {
		return base
	}
	width := minInt(layout.innerWidth, displayColumns(label)+2)
	left := state.geometry.contentX
	for _, action := range state.headerActions {
		if action.value == state.hoveredHeader {
			left += minInt(action.x, layout.innerWidth-width)
			break
		}
	}
	line := m.theme.TextStyle().Background(m.theme.Tokens().OverlayBg.GetBackground()).Render(padRightDisplay(truncateTailDisplay(" "+label, width), width))
	return tuikit.OverlayAt(base, line, m.width, m.height, left, state.geometry.headerY+1)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (m *Model) handlePaneChromeMouse(msg tea.MouseMsg) (bool, tea.Cmd) {
	state := m.subagentOutputOverlay
	mouse := msg.Mouse()
	g := state.geometry
	if state.editorSelecting {
		return m.handlePaneEditorMouse(msg)
	}
	if state.menu != "" {
		return m.handlePaneMenuMouse(msg)
	}
	if !state.selecting && (mouse.Y == g.footerY || state.pressedItem == "footer:hide") {
		switch msg.(type) {
		case tea.MouseClickMsg:
			state.pressedItem = ""
			if mouse.Button == tea.MouseLeft && m.paneFooterHideAt(mouse) {
				state.pressedItem = "footer:hide"
			}
			return true, nil
		case tea.MouseReleaseMsg:
			pressed := state.pressedItem
			state.pressedItem = ""
			if pressed == "footer:hide" && m.paneFooterHideAt(mouse) && (mouse.Button == tea.MouseLeft || mouse.Button == tea.MouseNone) {
				m.closeSubagentOutputOverlay()
			}
			return true, nil
		case tea.MouseMotionMsg:
			return true, nil
		}
	}
	if mouse.Y == g.headerY {
		action := m.paneHeaderActionAt(mouse)
		if _, press := msg.(tea.MouseClickMsg); press && mouse.Button == tea.MouseLeft {
			state.pressedItem = "header:" + action
			return true, nil
		}
		if _, release := msg.(tea.MouseReleaseMsg); release {
			pressed := state.pressedItem
			state.pressedItem = ""
			if pressed != "header:"+action {
				return true, nil
			}
			switch action {
			case "close":
				m.closeSubagentOutputOverlay()
			case "agents", "layout":
				m.openPaneMenu(action)
			}
			return true, nil
		}
	}
	return m.handlePaneEditorMouse(msg)
}
