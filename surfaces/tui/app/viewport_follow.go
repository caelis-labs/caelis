package tuiapp

func (m *Model) isViewportFollowTail() bool {
	if m == nil {
		return true
	}
	return m.viewportFollowState == viewportFollowTail
}

func (m *Model) setViewportFollowState(state viewportFollowState) {
	if m == nil {
		return
	}
	switch state {
	case viewportFollowTail, viewportPinnedHistory, viewportSelecting:
	default:
		state = viewportFollowTail
	}
	m.viewportFollowState = state
	m.userScrolledUp = state != viewportFollowTail
}

func (m *Model) refreshViewportFollowStateFromOffset() {
	if m == nil {
		return
	}
	if m.viewport.AtBottom() {
		m.setViewportFollowState(viewportFollowTail)
		return
	}
	m.setViewportFollowState(viewportPinnedHistory)
}

func (m *Model) enterViewportSelecting() {
	m.setViewportFollowState(viewportSelecting)
}

func (m *Model) leaveViewportSelecting() {
	m.refreshViewportFollowStateFromOffset()
}
