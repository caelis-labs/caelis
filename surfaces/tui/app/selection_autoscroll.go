package tuiapp

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

const (
	selectionAutoScrollInterval = 45 * time.Millisecond

	// The visible edge rows scroll gently; dragging beyond the viewport scrolls faster.
	selectionScrollSlow = 1
	selectionScrollFast = 2
)

type selectionAutoScrollState struct {
	active         bool
	tickScheduled  bool
	scheduledToken uint64
	nextToken      uint64
	mouse          tea.Mouse
}

func (m *Model) handleViewportSelectionWheel(mouse tea.Mouse) tea.Cmd {
	if m == nil || !m.selecting {
		return nil
	}
	m.selectionAutoScroll.mouse = mouse
	delta := 0
	step := maxInt(1, m.viewport.MouseWheelDelta)
	switch mouse.Button {
	case tea.MouseWheelUp:
		delta = -step
	case tea.MouseWheelDown:
		delta = step
	default:
		return nil
	}
	_, cmd := m.scrollViewportSelectionBy(delta, mouse)
	return cmd
}

func (m *Model) updateViewportSelectionAutoScroll(mouse tea.Mouse) tea.Cmd {
	if m == nil || !m.selecting {
		return nil
	}
	delta := m.selectionAutoScrollDelta(mouse)
	if delta == 0 {
		m.selectionAutoScroll.active = false
		return nil
	}
	m.selectionAutoScroll.active = true
	return m.ensureSelectionAutoScrollTick()
}

func (m *Model) ensureSelectionAutoScrollTick() tea.Cmd {
	if m == nil || !m.selectionAutoScroll.active || m.selectionAutoScroll.tickScheduled {
		return nil
	}
	m.selectionAutoScroll.nextToken++
	token := m.selectionAutoScroll.nextToken
	m.selectionAutoScroll.tickScheduled = true
	m.selectionAutoScroll.scheduledToken = token
	return selectionAutoScrollTickCmd(token)
}

func selectionAutoScrollTickCmd(token uint64) tea.Cmd {
	return tea.Tick(selectionAutoScrollInterval, func(at time.Time) tea.Msg {
		return frameTickMsg{at: at, kind: frameTickSelectionScroll, token: token}
	})
}

func (m *Model) advanceSelectionAutoScroll(token uint64) tea.Cmd {
	if m == nil {
		return nil
	}
	if token != 0 && token != m.selectionAutoScroll.scheduledToken {
		return nil
	}
	m.selectionAutoScroll.tickScheduled = false
	m.selectionAutoScroll.scheduledToken = 0
	if (!m.selecting && !m.inputSelecting) || !m.selectionAutoScroll.active {
		m.cancelSelectionAutoScroll()
		return nil
	}
	var (
		changed   bool
		scrollCmd tea.Cmd
	)
	if m.inputSelecting {
		changed, scrollCmd = m.scrollInputSelectionBy(m.inputSelectionAutoScrollDelta(m.selectionAutoScroll.mouse), m.selectionAutoScroll.mouse)
	} else {
		changed, scrollCmd = m.scrollViewportSelectionBy(m.selectionAutoScrollDelta(m.selectionAutoScroll.mouse), m.selectionAutoScroll.mouse)
	}
	if !changed {
		m.cancelSelectionAutoScroll()
		return nil
	}
	return tea.Batch(scrollCmd, m.ensureSelectionAutoScrollTick())
}

func (m *Model) selectionAutoScrollDelta(mouse tea.Mouse) int {
	if m == nil || m.viewport.Height() <= 0 {
		return 0
	}
	y := mouse.Y
	height := m.visibleViewportMouseHeight()
	if height <= 0 {
		return 0
	}
	switch {
	case y < 0:
		return -selectionScrollFast
	case y == 0:
		return -selectionScrollSlow
	case y >= height:
		return selectionScrollFast
	case y == height-1:
		return selectionScrollSlow
	default:
		return 0
	}
}

func (m *Model) updateInputSelectionAutoScroll(mouse tea.Mouse) tea.Cmd {
	if m == nil || !m.inputSelecting {
		return nil
	}
	delta := m.inputSelectionAutoScrollDelta(mouse)
	if delta == 0 {
		m.selectionAutoScroll.active = false
		return nil
	}
	m.selectionAutoScroll.active = true
	return m.ensureSelectionAutoScrollTick()
}

func (m *Model) inputSelectionAutoScrollDelta(mouse tea.Mouse) int {
	if m == nil {
		return 0
	}
	startY, height, ok := m.inputAreaBounds()
	if !ok || height <= 0 {
		return 0
	}
	y := m.screenYToFrameY(mouse.Y)
	switch {
	case y < startY:
		return -selectionScrollSlow
	case y >= startY+height:
		return selectionScrollSlow
	default:
		return 0
	}
}

func (m *Model) visibleViewportMouseHeight() int {
	if m == nil {
		return 0
	}
	height := m.viewport.Height()
	if height <= 0 {
		return 0
	}
	if trim := maxInt(0, m.frameTopTrim); trim > 0 {
		height -= minInt(trim, height)
	}
	if m.height > 0 {
		height = minInt(height, m.height)
	}
	return maxInt(0, height)
}

func (m *Model) cancelSelectionAutoScroll() {
	if m == nil {
		return
	}
	nextToken := m.selectionAutoScroll.nextToken
	m.selectionAutoScroll = selectionAutoScrollState{nextToken: nextToken}
}

func (m *Model) scrollViewportSelectionBy(delta int, mouse tea.Mouse) (bool, tea.Cmd) {
	if m == nil || delta == 0 {
		return false, nil
	}
	m.materializeViewportContentIfStale()
	next := m.viewport.YOffset() + delta
	maxOffset := m.viewportMaxOffset()
	if next < 0 {
		next = 0
	}
	if next > maxOffset {
		next = maxOffset
	}
	if next == m.viewport.YOffset() {
		return false, nil
	}
	m.viewport.SetYOffset(next)
	point, ok := m.mousePointToContentPoint(mouse.X, mouse.Y, true)
	if ok && m.selectionEnd != point {
		m.selectionEnd = point
	}
	m.setViewportFollowState(viewportSelecting)
	m.bumpViewportSelectionVersion()
	return true, m.touchViewportScrollbar()
}

func (m *Model) scrollInputSelectionBy(delta int, mouse tea.Mouse) (bool, tea.Cmd) {
	if m == nil || delta == 0 {
		return false, nil
	}
	snapshot := m.composeInputLayout()
	if snapshot.layout.totalRows <= maxInputBarRows {
		return false, nil
	}
	newOffset := clampComposerRowOffset(m.composerRowOffset+delta, snapshot.layout.totalRows, maxInputBarRows)
	if newOffset == m.composerRowOffset {
		return false, nil
	}
	m.composerRowOffset = newOffset
	refreshed := m.buildComposeInputLayout()
	point, ok := m.inputGlobalPointFromMouse(mouse, true)
	if !ok {
		return true, m.ensureSelectionAutoScrollTick()
	}
	if point == m.inputSelectionEnd {
		return true, m.ensureSelectionAutoScrollTick()
	}
	m.inputSelectionEnd = point
	m.moveTextareaCursorToIndex(refreshed.textareaIndexFromPoint(point))
	m.syncInputFromTextarea()
	return true, m.ensureSelectionAutoScrollTick()
}
