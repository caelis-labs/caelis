package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/caelis-labs/caelis/control/uipreferences"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func (m *Model) openPaneMenu(menu string) {
	state := m.subagentOutputOverlay
	if state == nil {
		return
	}
	if menu == "layout" && !m.paneLayoutAvailable(uipreferences.Right) && !m.paneLayoutAvailable(uipreferences.Down) {
		return
	}
	m.workspace.childFocused = true
	state.menu, state.menuItems, state.menuIndex = menu, nil, 0
	state.menuOffset = 0
	value := state.callID
	if menu == "layout" {
		value = string(m.effectivePaneLayout())
	}
	for i, item := range m.paneMenuItems(state) {
		if item.value == value {
			state.menuIndex = i
			break
		}
	}
}
func (m *Model) paneMenuItems(state *subagentOutputOverlayState) []paneMenuItem {
	if state.menuItems == nil {
		state.menuItems = m.buildPaneMenuItems(state)
	}
	return state.menuItems
}
func (m *Model) buildPaneMenuItems(state *subagentOutputOverlayState) []paneMenuItem {
	if state.menu == "layout" {
		var items []paneMenuItem
		for _, layout := range []uipreferences.Layout{uipreferences.Overlay, uipreferences.Left, uipreferences.Right, uipreferences.Up, uipreferences.Down} {
			if m.paneLayoutAvailable(layout) {
				name := string(layout)
				name = strings.ToUpper(name[:1]) + name[1:]
				items = append(items, paneMenuItem{label: paneLayoutSymbol(layout) + " " + name, value: string(layout)})
			}
		}
		if m.workspaceLayout().split {
			items = append(items, paneMenuItem{label: "↔ Resize split", value: "resize"})
		}
		return items
	}
	var items []paneMenuItem
	for _, row := range m.subagentRosterRows() {
		items = append(items, paneMenuItem{label: row.handle, binding: row.binding, value: row.callID})
	}
	return items
}

// Agent rows stay pinned while open. Only layout choices change on resize, and
// their keyboard selection follows the value rather than the old row number.
func (m *Model) refreshPaneLayoutMenu() {
	state := m.subagentOutputOverlay
	if state == nil || state.menu != "layout" {
		return
	}
	value := string(m.effectivePaneLayout())
	if state.menuIndex >= 0 && state.menuIndex < len(state.menuItems) {
		value = state.menuItems[state.menuIndex].value
	}
	state.menuItems = m.buildPaneMenuItems(state)
	state.menuRect, state.pressedItem = paneRect{}, ""
	state.menuIndex = 0
	for i, item := range state.menuItems {
		if item.value == value {
			state.menuIndex = i
			break
		}
	}
	if len(state.menuItems) <= 1 {
		state.menu = ""
	}
}
func (m *Model) activatePaneMenu() tea.Cmd {
	state := m.subagentOutputOverlay
	items := m.paneMenuItems(state)
	if state.menuIndex < 0 || state.menuIndex >= len(items) {
		return nil
	}
	item := items[state.menuIndex]
	menu := state.menu
	state.menu = ""
	if menu == "layout" {
		if item.value == "resize" {
			m.beginPaneResize()
			return nil
		}
		return m.setSubagentLayout(uipreferences.Layout(item.value))
	}
	m.openSubagentOutputOverlayView(item.value, m.subagentOutputViews[item.value])
	return m.resumeRunningAnimationIfNeeded()
}
func (m *Model) handlePaneMenuKey(msg tea.KeyMsg) tea.Cmd {
	state := m.subagentOutputOverlay
	items := m.paneMenuItems(state)
	switch msg.Key().Code {
	case tea.KeyEscape:
		state.menu = ""
	case tea.KeyDown, tea.KeyTab:
		if len(items) > 0 {
			delta := 1
			if msg.Key().Mod&tea.ModShift != 0 {
				delta = -1
			}
			state.menuIndex = (state.menuIndex + len(items) + delta) % len(items)
		}
	case tea.KeyUp:
		if len(items) > 0 {
			state.menuIndex = (state.menuIndex + len(items) - 1) % len(items)
		}
	case tea.KeyEnter:
		return m.activatePaneMenu()
	case tea.KeyHome:
		state.menuIndex = 0
	case tea.KeyEnd:
		state.menuIndex = maxInt(0, len(items)-1)
	}
	return nil
}
func (m *Model) renderPaneMenu() string {
	state := m.subagentOutputOverlay
	if state == nil || state.menu == "" {
		return ""
	}
	items := m.paneMenuItems(state)
	state.menuIndex = clampInt(state.menuIndex, 0, maxInt(0, len(items)-1))
	layout := m.subagentOutputLayout(state)
	startX, startY := layout.startX+layout.contentInset, layout.startY+layout.borderInset+1
	availableWidth := minInt(layout.innerWidth, maxInt(0, m.width-startX))
	availableHeight := minInt(maxInt(1, layout.frameHeight-4), maxInt(0, m.height-startY))
	if len(items) == 0 || availableWidth == 0 || availableHeight == 0 {
		state.menuRect = paneRect{}
		return ""
	}
	state.menuInset = 0
	if availableWidth >= 3 && availableHeight >= 3 {
		state.menuInset = 1
	}
	inset := state.menuInset
	height := minInt(len(items), maxInt(1, availableHeight-2*inset))
	offset := clampInt(state.menuOffset, 0, maxInt(0, len(items)-height))
	// Keep visible rows still while hovering; scroll only when keyboard or
	// wheel navigation moves the selection beyond the visible range.
	if state.menuIndex < offset {
		offset = state.menuIndex
	} else if state.menuIndex >= offset+height {
		offset = maxInt(0, state.menuIndex-height+1)
	}
	state.menuOffset = offset
	width := 1
	for _, item := range items {
		width = maxInt(width, displayColumns(item.label+subagentRosterBindingText(item.binding))+2)
	}
	width = minInt(width, availableWidth-2*inset)
	state.menuRect = paneRect{startX, startY, width + 2*inset, height + 2*inset}
	if state.menu == "layout" {
		state.menuRect.x = startX + availableWidth - state.menuRect.width
	}
	var lines []string
	for i, item := range items[offset : offset+height] {
		if state.menu == "agents" {
			row := subagentRosterRow{handle: item.label, binding: item.binding}
			lines = append(lines, m.renderSubagentRosterRow(row, offset+i == state.menuIndex, width))
			continue
		}
		label := " " + item.label
		style := m.theme.TextStyle().Background(m.theme.Tokens().OverlayBg.GetBackground())
		if offset+i == state.menuIndex {
			style = m.theme.SelectionStyle()
		}
		lines = append(lines, style.Render(padRightDisplay(truncateTailDisplay(label, width), width)))
	}
	frame := strings.Join(lines, "\n")
	tokens := m.theme.Tokens()
	if inset > 0 {
		frame = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).
			BorderForeground(tokens.OverlayBorder.GetForeground()).
			BorderBackground(tokens.OverlayBg.GetBackground()).Render(frame)
	}
	return tuikit.PaintLineBackground(frame, state.menuRect.width, tokens.OverlayBg.GetBackground())
}

func (state *subagentOutputOverlayState) paneMenuIndexAt(mouse tea.Mouse) int {
	content := state.menuRect
	content.x += state.menuInset
	content.y += state.menuInset
	content.width -= 2 * state.menuInset
	content.height -= 2 * state.menuInset
	if !content.contains(mouse.X, mouse.Y) {
		return -1
	}
	index := state.menuOffset + mouse.Y - content.y
	if index >= len(state.menuItems) {
		return -1
	}
	return index
}

func (m *Model) handlePaneMenuMouse(msg tea.MouseMsg) (bool, tea.Cmd) {
	state := m.subagentOutputOverlay
	mouse := msg.Mouse()
	index := state.paneMenuIndexAt(mouse)
	switch msg.(type) {
	case tea.MouseMotionMsg:
		if mouse.Button == tea.MouseNone && index >= 0 {
			state.menuIndex = index
		}
	case tea.MouseWheelMsg:
		state.pressedItem = ""
		switch mouse.Button {
		case tea.MouseWheelUp:
			state.menuIndex--
		case tea.MouseWheelDown:
			state.menuIndex++
		}
		state.menuIndex = clampInt(state.menuIndex, 0, maxInt(0, len(state.menuItems)-1))
	case tea.MouseClickMsg:
		state.pressedItem = ""
		if mouse.Button != tea.MouseLeft {
			return true, nil
		}
		if !state.menuRect.contains(mouse.X, mouse.Y) {
			state.menu = ""
		} else if index >= 0 {
			state.menuIndex = index
			state.pressedItem = "menu:" + state.menuItems[index].value
		}
	case tea.MouseReleaseMsg:
		pressed := state.pressedItem
		state.pressedItem = ""
		if (mouse.Button == tea.MouseLeft || mouse.Button == tea.MouseNone) && index >= 0 && pressed == "menu:"+state.menuItems[index].value {
			state.menuIndex = index
			return true, m.activatePaneMenu()
		}
	}
	return true, nil
}
