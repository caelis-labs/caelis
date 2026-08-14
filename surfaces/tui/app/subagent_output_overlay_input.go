package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

func (m *Model) handleSubagentOutputOverlayKey(msg tea.KeyMsg) tea.Cmd {
	if m == nil || m.subagentOutputOverlay == nil {
		return nil
	}
	if _, ok := msg.(tea.KeyReleaseMsg); ok {
		return nil
	}
	keyEvent := msg.Key()
	switch keyEvent.Code {
	case tea.KeyEscape:
		m.closeSubagentOutputOverlay()
	case tea.KeyUp:
		m.scrollSubagentOutputOverlay(-1)
	case tea.KeyDown:
		m.scrollSubagentOutputOverlay(1)
	case tea.KeyPgUp:
		m.scrollSubagentOutputOverlay(-m.subagentOutputOverlayPageSize())
	case tea.KeyPgDown:
		m.scrollSubagentOutputOverlay(m.subagentOutputOverlayPageSize())
	case tea.KeyHome:
		m.subagentOutputOverlay.offset = 0
		m.subagentOutputOverlay.followTail = false
	case tea.KeyEnd:
		m.subagentOutputOverlay.followTail = true
	}
	return nil
}

func (m *Model) subagentOutputOverlayPageSize() int {
	if m == nil || m.subagentOutputOverlay == nil {
		return 1
	}
	return maxInt(1, len(m.subagentOutputOverlay.geometry.rowTokens)-1)
}

func (m *Model) scrollSubagentOutputOverlay(delta int) {
	if m == nil || m.subagentOutputOverlay == nil || delta == 0 {
		return
	}
	state := m.subagentOutputOverlay
	state.offset = maxInt(0, state.offset+delta)
	state.followTail = delta > 0 && state.offset >= m.subagentOutputOverlayMaxOffset()
}

func (m *Model) subagentOutputOverlayMaxOffset() int {
	if m == nil || m.subagentOutputOverlay == nil {
		return 0
	}
	state := m.subagentOutputOverlay
	height := maxInt(1, len(state.geometry.rowTokens))
	return maxInt(0, state.geometry.totalRows-height)
}

func (m *Model) handleSubagentOutputOverlayMouse(msg tea.MouseMsg) (bool, tea.Cmd) {
	if m == nil || m.subagentOutputOverlay == nil || m.activePrompt != nil {
		return false, nil
	}
	state := m.subagentOutputOverlay
	mouse := msg.Mouse()
	geometry := state.geometry
	inside := mouse.X >= geometry.x && mouse.X < geometry.x+geometry.width &&
		mouse.Y >= geometry.y && mouse.Y < geometry.y+geometry.height
	switch msg.(type) {
	case tea.MouseWheelMsg:
		if state.selecting {
			return true, m.handleSubagentOutputSelectionWheel(mouse)
		}
		if inside {
			switch mouse.Button {
			case tea.MouseWheelUp:
				m.scrollSubagentOutputOverlay(-1)
			case tea.MouseWheelDown:
				m.scrollSubagentOutputOverlay(1)
			}
		}
		return true, nil
	case tea.MouseClickMsg:
		if mouse.Button != tea.MouseLeft || !inside {
			state.pressedItem = ""
			m.clearSubagentOutputSelection()
			return true, nil
		}
		if subagentOutputCloseHit(geometry, mouse) {
			state.pressedItem = "close"
			m.clearSubagentOutputSelection()
			return true, nil
		}
		point, ok := m.subagentOutputPointFromMouse(mouse, false)
		if !ok {
			state.pressedItem = ""
			m.clearSubagentOutputSelection()
			return true, nil
		}
		index := point.line - state.offset
		state.pressedItem = subagentOutputRowItem(geometry, index)
		m.cancelSelectionAutoScroll()
		m.clearSelection()
		m.clearInputSelection()
		m.clearFixedSelection()
		state.selecting = true
		state.selectStart = point
		state.selectEnd = point
		state.followTail = false
		return true, nil
	case tea.MouseMotionMsg:
		if !state.selecting {
			return true, nil
		}
		m.selectionAutoScroll.mouse = mouse
		cmd := m.updateSubagentOutputSelectionAutoScroll(mouse)
		if point, ok := m.subagentOutputPointFromMouse(mouse, true); ok {
			state.selectEnd = point
		}
		return true, cmd
	case tea.MouseReleaseMsg:
		pressed := state.pressedItem
		state.pressedItem = ""
		if state.selecting {
			if mouse.Button != tea.MouseLeft && mouse.Button != tea.MouseNone {
				m.clearSubagentOutputSelection()
				return true, nil
			}
			m.cancelSelectionAutoScroll()
			if point, ok := m.subagentOutputPointFromMouse(mouse, true); ok {
				state.selectEnd = point
			}
			hadSelectionRange := state.selectStart != state.selectEnd
			text := m.subagentOutputSelectionText()
			m.clearSubagentOutputSelection()
			if hadSelectionRange {
				if text == "" {
					return true, nil
				}
				return true, m.copySelectionToClipboard(text)
			}
		}
		if pressed == "close" && subagentOutputCloseHit(geometry, mouse) {
			m.closeSubagentOutputOverlay()
			return true, nil
		}
		index := subagentOutputRowAtY(geometry, mouse.Y)
		if index < 0 || subagentOutputRowItem(geometry, index) != pressed {
			return true, nil
		}
		m.toggleSubagentOutputRow(geometry.rowTokens[index])
		return true, nil
	default:
		return true, nil
	}
}

func (m *Model) clearSubagentOutputSelection() {
	if m == nil || m.subagentOutputOverlay == nil {
		return
	}
	state := m.subagentOutputOverlay
	wasSelecting := state.selecting
	state.selecting = false
	state.selectStart = textSelectionPoint{line: -1, col: -1}
	state.selectEnd = textSelectionPoint{line: -1, col: -1}
	if wasSelecting {
		state.followTail = state.offset >= m.subagentOutputOverlayMaxOffset()
	}
	m.cancelSelectionAutoScroll()
}

func (m *Model) subagentOutputSelectionText() string {
	if m == nil || m.subagentOutputOverlay == nil {
		return ""
	}
	state := m.subagentOutputOverlay
	view := m.subagentOutputViews[state.callID]
	if view == nil || len(view.renderCache.rows) == 0 {
		return ""
	}
	rows := view.renderCache.rows
	start, finish, ok := normalizedSelectionRange(state.selectStart, state.selectEnd, len(rows))
	if !ok {
		return ""
	}
	plain := make([]string, len(rows))
	indents := make([]int, len(rows))
	for index := range rows {
		plain[index] = rows[index].Plain
		indents[index] = rows[index].selectionIndent
	}
	return selectionTextFromLinesWithIndents(plain, indents, start, finish)
}

func (m *Model) subagentOutputPointFromMouse(mouse tea.Mouse, clamp bool) (textSelectionPoint, bool) {
	if m == nil || m.subagentOutputOverlay == nil {
		return textSelectionPoint{}, false
	}
	state := m.subagentOutputOverlay
	geometry := state.geometry
	view := m.subagentOutputViews[state.callID]
	if view == nil || len(view.renderCache.rows) == 0 || geometry.contentWidth <= 0 {
		return textSelectionPoint{}, false
	}
	visibleRows := minInt(
		len(geometry.rowTokens),
		maxInt(0, minInt(geometry.totalRows, len(view.renderCache.rows))-state.offset),
	)
	if visibleRows <= 0 {
		return textSelectionPoint{}, false
	}

	row := mouse.Y - geometry.contentY
	col := mouse.X - geometry.contentX
	if !clamp {
		if row < 0 || row >= visibleRows || col < 0 || col >= geometry.contentWidth {
			return textSelectionPoint{}, false
		}
	} else {
		row = clampInt(row, 0, visibleRows-1)
		col = clampInt(col, 0, geometry.contentWidth)
	}
	line := state.offset + row
	if line < 0 || line >= len(view.renderCache.rows) {
		return textSelectionPoint{}, false
	}
	selectedRow := view.renderCache.rows[line]
	width := displayColumns(selectedRow.Plain)
	col = clampInt(col, 0, width)
	indent := clampInt(selectedRow.selectionIndent, 0, width)
	content := sliceByDisplayColumns(selectedRow.Plain, indent, width)
	contentCol := alignDisplayColumnToCharBoundary(content, maxInt(0, col-indent))
	col = indent + contentCol
	return textSelectionPoint{line: line, col: col}, true
}

func (m *Model) handleSubagentOutputSelectionWheel(mouse tea.Mouse) tea.Cmd {
	if m == nil || m.subagentOutputOverlay == nil || !m.subagentOutputOverlay.selecting {
		return nil
	}
	m.selectionAutoScroll.mouse = mouse
	delta := 0
	switch mouse.Button {
	case tea.MouseWheelUp:
		delta = -1
	case tea.MouseWheelDown:
		delta = 1
	default:
		return nil
	}
	_, cmd := m.scrollSubagentOutputSelectionBy(delta, mouse)
	return cmd
}

func (m *Model) updateSubagentOutputSelectionAutoScroll(mouse tea.Mouse) tea.Cmd {
	if m == nil || m.subagentOutputOverlay == nil || !m.subagentOutputOverlay.selecting {
		return nil
	}
	delta := m.subagentOutputSelectionAutoScrollDelta(mouse)
	if delta == 0 {
		m.selectionAutoScroll.active = false
		return nil
	}
	m.selectionAutoScroll.active = true
	return m.ensureSelectionAutoScrollTick()
}

func (m *Model) subagentOutputSelectionAutoScrollDelta(mouse tea.Mouse) int {
	if m == nil || m.subagentOutputOverlay == nil {
		return 0
	}
	geometry := m.subagentOutputOverlay.geometry
	height := minInt(
		len(geometry.rowTokens),
		maxInt(0, geometry.totalRows-m.subagentOutputOverlay.offset),
	)
	if height <= 0 {
		return 0
	}
	switch {
	case mouse.Y < geometry.contentY:
		return -selectionScrollFast
	case mouse.Y == geometry.contentY:
		return -selectionScrollSlow
	case mouse.Y >= geometry.contentY+height:
		return selectionScrollFast
	case mouse.Y == geometry.contentY+height-1:
		return selectionScrollSlow
	default:
		return 0
	}
}

func (m *Model) scrollSubagentOutputSelectionBy(delta int, mouse tea.Mouse) (bool, tea.Cmd) {
	if m == nil || m.subagentOutputOverlay == nil || !m.subagentOutputOverlay.selecting || delta == 0 {
		return false, nil
	}
	state := m.subagentOutputOverlay
	next := clampInt(state.offset+delta, 0, m.subagentOutputOverlayMaxOffset())
	if next == state.offset {
		return false, nil
	}
	state.offset = next
	state.followTail = false
	if point, ok := m.subagentOutputPointFromMouse(mouse, true); ok {
		state.selectEnd = point
	}
	return true, nil
}

func subagentOutputCloseHit(geometry subagentOutputOverlayGeometry, mouse tea.Mouse) bool {
	return mouse.Y == geometry.closeY &&
		mouse.X >= geometry.closeX-1 &&
		mouse.X <= geometry.closeX+1
}

func subagentOutputRowAtY(geometry subagentOutputOverlayGeometry, y int) int {
	index := y - geometry.contentY
	if index < 0 || index >= len(geometry.rowTokens) {
		return -1
	}
	return index
}

func subagentOutputRowItem(geometry subagentOutputOverlayGeometry, index int) string {
	if index < 0 || index >= len(geometry.rowTokens) {
		return ""
	}
	token := strings.TrimSpace(geometry.rowTokens[index])
	if token == "" {
		return ""
	}
	return token
}

func (m *Model) toggleSubagentOutputRow(token string) bool {
	if m == nil || m.subagentOutputOverlay == nil {
		return false
	}
	view := m.subagentOutputViews[m.subagentOutputOverlay.callID]
	if view == nil || view.document == nil {
		return false
	}
	token = strings.TrimSpace(token)
	for _, documentBlock := range view.document.Blocks() {
		block, ok := documentBlock.(*ParticipantTurnBlock)
		if !ok {
			continue
		}
		var changed bool
		switch {
		case strings.HasPrefix(token, "acp_tool_panel:"):
			callID := strings.TrimSpace(strings.TrimPrefix(token, "acp_tool_panel:"))
			changed = block.toggleToolPanelClick(callID)
		case strings.HasPrefix(token, "acp_reasoning:"):
			key := strings.TrimSpace(strings.TrimPrefix(token, "acp_reasoning:"))
			changed = block.toggleReasoningExpanded(key)
		case strings.HasPrefix(token, "acp_exploration_stage:"):
			key := strings.TrimSpace(strings.TrimPrefix(token, "acp_exploration_stage:"))
			changed = block.toggleExplorationExpanded(key)
		case strings.HasPrefix(token, "acp_exploration_stable:"):
			key := strings.TrimSpace(strings.TrimPrefix(token, "acp_exploration_stable:"))
			changed = block.toggleExplorationExpanded(key)
		default:
			return false
		}
		if changed {
			view.touch(true)
			return true
		}
	}
	return false
}
