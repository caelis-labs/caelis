package tuiapp

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/caelis-labs/caelis/control/uipreferences"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func (m *Model) ensureSubagentEditor(state *subagentOutputOverlayState) {
	if state.editorReady {
		return
	}
	state.editor = newPromptTextarea(m.theme)
	state.editor.CharLimit = 65536
	state.editor.Placeholder = "Message this agent…"
	state.editorReady = true
	state.historyIndex = -1
}
func (m *Model) paneEditorLayout(state *subagentOutputOverlayState, width int) composerInputLayout {
	m.ensureSubagentEditor(state)
	contentWidth := maxInt(1, width-2-2*m.composerChrome().horizontalInset())
	state.editor.SetWidth(contentWidth + 2)
	value, index := state.editor.Value(), promptTextareaCursorIndex(state.editor)
	display, cursor, mapping := composeInputDisplayWithMap(value, index, state.attachments)
	layout := layoutComposerDisplay(display, cursor, contentWidth)
	offset := clampComposerRowOffset(state.editorOffset, len(layout.rows), maxInputBarRows)
	if layout.cursorRow < offset {
		offset = layout.cursorRow
	}
	if layout.cursorRow >= offset+maxInputBarRows {
		offset = layout.cursorRow - maxInputBarRows + 1
	}
	state.editorOffset = offset
	end := minInt(len(layout.rows), offset+maxInputBarRows)
	return composerInputLayout{prompt: "> ", promptWidth: 2, continuation: "  ", value: value, cursorIndex: index, contentWidth: contentWidth, layout: layout, displayToValue: mapping, rowOffset: offset, rowEnd: end}
}
func (m *Model) renderPaneEditor(state *subagentOutputOverlayState, width int) string {
	layout := m.paneEditorLayout(state, width)
	theme := m.theme
	chrome := m.promptComposerChrome(m.workspace.childFocused)
	render := renderPromptEditor(layout, theme, state.editor, chrome, state.editor.Placeholder, "")
	// Placeholder is only a display hint for an empty draft.
	if layout.value != "" {
		render = renderPromptEditor(layout, theme, state.editor, chrome, "", "")
	}
	if state.editorSelecting {
		if start, end, ok := normalizedSelectionRange(state.editorSelectStart, state.editorSelectEnd, len(layout.layout.rows)); ok {
			styles := promptSelectionStyles(theme, chrome)
			render.styledLines = strings.Split(renderPromptAwareInputSelection(render.plainLines, start, end, layout.rowOffset, layout.promptWidth, layout.prompt, styles), "\n")
		}
	}
	state.editorCursor = render.cursor
	if state.editorCursor != nil {
		state.editorCursor.X += chrome.horizontalInset()
		state.editorCursor.Y += chrome.topRows()
	}
	return wrapPromptEditor(render.styledText(), chrome, width, lipgloss.NewStyle().Background(chrome.background))
}
func (m *Model) handlePaneEditorKey(msg tea.KeyMsg) tea.Cmd {
	state := m.subagentOutputOverlay
	if state == nil {
		return nil
	}
	k := msg.Key()
	if state.menu != "" {
		return m.handlePaneMenuKey(msg)
	}
	state.editorSelecting = false
	defer state.reconcileEditorAttachments()
	switch {
	case k.Code == tea.KeyUp && state.editor.Value() == "" && len(state.history) > 0:
		state.historyIndex = len(state.history) - 1
		state.editor.SetValue(state.history[state.historyIndex])
		state.attachments = cloneInputAttachments(state.historyAttachments[state.historyIndex])
		movePromptTextareaCursor(&state.editor, len([]rune(state.editor.Value())))
		return nil
	case k.Code == tea.KeyDown && state.historyIndex >= 0:
		state.editor.Reset()
		state.attachments = nil
		state.historyIndex = -1
		return nil
	case k.Code == tea.KeyEscape:
		m.clearSubagentOutputSelection()
		return nil
	case key.Matches(msg, m.keys.InsertNewline):
		state.editor.InsertString("\n")
		return nil
	case k.Code == tea.KeyEnter && k.Mod&tea.ModShift == 0 && k.Mod&tea.ModAlt == 0:
		return m.submitPanePrompt()
	case k.Code == tea.KeyEnd && state.editor.Value() == "":
		state.followTail = true
		return nil
	case k.Code == tea.KeyHome && state.editor.Value() == "":
		state.offset = 0
		state.followTail = false
		return nil
	case k.Code == tea.KeyPgUp:
		m.scrollSubagentOutputOverlay(-m.subagentOutputOverlayPageSize())
		return nil
	case k.Code == tea.KeyPgDown:
		m.scrollSubagentOutputOverlay(m.subagentOutputOverlayPageSize())
		return nil
	case key.Matches(msg, m.keys.ImagePaste), key.Matches(msg, m.keys.TextPaste):
		m.pastePaneClipboard(state, msg)
		return nil
	case msg.String() == "ctrl+c":
		state.inputStatus = m.paneKeyHint(m.keys.PaneToggle) + " · agent keeps running"
		return nil
	}
	m.ensureSubagentEditor(state)
	if k.Code == tea.KeyEnter {
		state.editor.InsertString("\n")
		return nil
	}
	state.historyIndex = -1
	var cmd tea.Cmd
	state.editor, cmd = state.editor.Update(msg)
	return cmd
}
func (m *Model) openPaneMenu(menu string) {
	state := m.subagentOutputOverlay
	if state == nil {
		return
	}
	m.workspace.childFocused = true
	state.menu, state.menuItems, state.menuIndex = menu, nil, 0
	value := state.callID
	if menu == "layout" {
		value = string(m.workspace.preferences.WithDefaults().SubagentLayout)
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
		items := []paneMenuItem{{label: "▣ Overlay", value: "overlay"}, {label: "← Split left", value: "left"}, {label: "→ Split right", value: "right"}, {label: "↑ Split up", value: "up"}, {label: "↓ Split down", value: "down"}}
		if m.workspaceLayout().split {
			items = append(items, paneMenuItem{label: "↔ Resize split", value: "resize"})
		}
		return items
	}
	var items []paneMenuItem
	for _, row := range m.subagentRosterRows() {
		mark := "✓"
		if row.running() {
			mark = "•"
		}
		if row.status == subagentOutputFailed {
			mark = "!"
		}
		items = append(items, paneMenuItem{label: mark + " " + row.handle, value: row.callID})
	}
	return items
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
	height := minInt(len(items), maxInt(1, layout.frameHeight-4))
	offset := clampInt(state.menuIndex-height+1, 0, maxInt(0, len(items)-height))
	width := minInt(maxInt(28, layout.innerWidth), 100)
	width = minInt(width, layout.innerWidth)
	if state.menu == "layout" {
		width = minInt(24, layout.innerWidth)
	}
	state.menuRect = paneRect{layout.startX + layout.contentInset, layout.startY + layout.borderInset + 1, width, height}
	if state.menu == "layout" {
		state.menuRect.x = layout.startX + layout.contentInset + layout.innerWidth - width
	}
	state.menuRows = items[offset : offset+height]
	roster := m.subagentRosterRows()
	var lines []string
	for i, item := range state.menuRows {
		if state.menu == "agents" {
			for _, row := range roster {
				if row.callID == item.value {
					lines = append(lines, tuikit.PaintLineBackground(m.renderSubagentRosterRow(row, offset+i == state.menuIndex, width, time.Now()), width, m.theme.Tokens().OverlayBg.GetBackground()))
					break
				}
			}
			continue
		}
		label := "  " + item.label
		style := m.theme.TextStyle().Background(m.theme.Tokens().OverlayBg.GetBackground())
		if offset+i == state.menuIndex {
			label = "› " + item.label
			style = m.theme.InputSelectionStyle()
		}
		lines = append(lines, style.Render(padRightDisplay(truncateTailDisplay(label, width), width)))
	}
	return strings.Join(lines, "\n")
}
func (m *Model) renderPaneTitle(view *subagentOutputView, width int) string {
	state := m.subagentOutputOverlay
	title := "Subagent"
	if view != nil {
		title = firstNonEmptyString(view.actor, view.taskHandle, "Subagent")
	}
	keys := m.workspace.childFocused && width >= 64
	agentKey, layoutKey := "", ""
	if keys {
		agentKey, layoutKey = "  "+m.keys.PaneAgents.Help().Key, "  "+m.keys.PaneLayout.Help().Key
	}
	right := m.paneLayoutLabel() + " ▾" + layoutKey + "  ×"
	if width < 42 {
		right = "▣ ▾  ×"
	}
	marker := "○ "
	if m.workspace.childFocused {
		marker = "▸ "
	}
	left := truncateTailDisplay(marker+title, maxInt(1, width-displayColumns(right)-displayColumns(agentKey)-4)) + " ▾"
	leftWidth := displayColumns(left) + displayColumns(agentKey)
	gap := maxInt(1, width-leftWidth-displayColumns(right))
	state.headerActions = []paneHeaderAction{{0, leftWidth, "agents"}, {leftWidth + gap, displayColumns(right) - 3, "layout"}, {width - 1, 1, "close"}}
	style := m.theme.TitleStyle()
	if m.workspace.childFocused {
		style = style.Foreground(m.theme.CursorFg)
	}
	return style.Render(left) + m.theme.HelpHintTextStyle().Render(agentKey) + strings.Repeat(" ", gap) + m.theme.HelpHintTextStyle().Render(right)
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
		items := m.paneMenuItems(state)
		if _, wheel := msg.(tea.MouseWheelMsg); wheel {
			if mouse.Button == tea.MouseWheelUp {
				state.menuIndex--
			} else {
				state.menuIndex++
			}
			state.menuIndex = clampInt(state.menuIndex, 0, maxInt(0, len(items)-1))
			return true, nil
		}
		if _, press := msg.(tea.MouseClickMsg); press {
			if !state.menuRect.contains(mouse.X, mouse.Y) {
				state.menu = ""
				return true, nil
			}
			row := mouse.Y - state.menuRect.y
			if row < len(state.menuRows) {
				selected := state.menuRows[row]
				for i, item := range items {
					if item == selected {
						state.menuIndex = i
						state.pressedItem = "menu:" + item.value
						break
					}
				}
			}
			return true, nil
		}
		if _, release := msg.(tea.MouseReleaseMsg); release {
			pressed := state.pressedItem
			state.pressedItem = ""
			if state.menuRect.contains(mouse.X, mouse.Y) {
				row := mouse.Y - state.menuRect.y
				if row < len(state.menuRows) && pressed == "menu:"+state.menuRows[row].value {
					return true, m.activatePaneMenu()
				}
			}
		}
		return true, nil
	}
	if mouse.Y == g.closeY {
		col := mouse.X - g.contentX
		action := ""
		for _, a := range state.headerActions {
			if col >= a.x && col < a.x+a.width {
				action = a.value
				break
			}
		}
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

// Keep the editor's cursor drawing separate from its input state. Bubble Tea
// has one physical cursor, owned by the focused pane in the current frame.
func (m *Model) paneCursor() *tea.Cursor {
	state := m.subagentOutputOverlay
	if state == nil || m.subagentOverlay != nil || !m.workspace.childFocused || state.menu != "" || m.workspace.resizing || m.workspace.dragging || state.editorCursor == nil {
		return nil
	}
	cursor := *state.editorCursor
	cursor.X += state.geometry.contentX
	cursor.Y += state.editorY
	if cursor.X < 0 || cursor.X >= m.width || cursor.Y < 0 || cursor.Y >= m.height {
		return nil
	}
	return &cursor
}
