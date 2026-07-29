package tuiapp

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// Terminal mouse protocols can emit several wheel messages for one light
// gesture. Limit same-direction selection changes while keeping sustained
// scrolling and immediate direction reversal responsive.
const completionWheelPulseInterval = 35 * time.Millisecond

type completionMouseTarget struct {
	kind  completionKind
	index int
	key   string
}

func (t completionMouseTarget) valid() bool {
	return t.kind != completionNone && t.index >= 0 && t.key != ""
}

type completionMouseState struct {
	press               completionMouseTarget
	pressActive         bool
	wheelKind           completionKind
	wheelDirection      int
	wheelLastAcceptedAt time.Time
}

func (m *Model) completionMouseContext() (completionSnapshot, completionOverlayGeometry, bool) {
	if m.activePrompt != nil || m.btwOverlay != nil || m.shouldRenderPalette() {
		return completionSnapshot{}, completionOverlayGeometry{}, false
	}
	return m.activeCompletionGeometry()
}

func (m *Model) completionMouseTargetAt(
	snapshot completionSnapshot,
	geometry completionOverlayGeometry,
	mouse tea.Mouse,
) (completionMouseTarget, bool) {
	index, ok := geometry.candidateIndexAt(mouse)
	if !ok {
		return completionMouseTarget{}, false
	}
	target := completionMouseTarget{
		kind:  snapshot.kind,
		index: index,
		key:   m.completionKeyAt(snapshot.kind, index),
	}
	return target, target.valid()
}

func (m *Model) cancelCompletionMousePress() {
	m.completionMouse.press = completionMouseTarget{}
	m.completionMouse.pressActive = false
}

func (m *Model) clearCompletionMouseState() {
	m.completionMouse = completionMouseState{}
}

func (m *Model) acceptCompletionWheelPulse(kind completionKind, direction int, at time.Time) bool {
	if kind == completionNone || direction == 0 {
		return false
	}
	state := &m.completionMouse
	if state.wheelKind == kind && state.wheelDirection == direction && !state.wheelLastAcceptedAt.IsZero() {
		elapsed := at.Sub(state.wheelLastAcceptedAt)
		if elapsed >= 0 && elapsed < completionWheelPulseInterval {
			return false
		}
	}
	state.wheelKind = kind
	state.wheelDirection = direction
	state.wheelLastAcceptedAt = at
	return true
}

func (m *Model) handleCompletionMouseMotion(mouse tea.Mouse) (bool, tea.Cmd) {
	snapshot, geometry, ok := m.completionMouseContext()
	if !ok {
		return m.completionMouse.pressActive, nil
	}
	if target, hit := m.completionMouseTargetAt(snapshot, geometry, mouse); hit {
		m.pinCompletionWindow(geometry)
		m.setCompletionIndex(snapshot.kind, target.index)
		return true, nil
	}
	if geometry.contains(mouse) || m.completionMouse.pressActive {
		return true, nil
	}
	return false, nil
}

func (m *Model) handleCompletionMousePress(mouse tea.Mouse) (bool, tea.Cmd) {
	snapshot, geometry, ok := m.completionMouseContext()
	if !ok || !geometry.contains(mouse) {
		m.cancelCompletionMousePress()
		return false, nil
	}
	m.clearSelection()
	m.clearInputSelection()
	m.clearFixedSelection()
	m.clearCompletionMouseState()
	if mouse.Button != tea.MouseLeft {
		return true, nil
	}
	if target, hit := m.completionMouseTargetAt(snapshot, geometry, mouse); hit {
		m.pinCompletionWindow(geometry)
		m.setCompletionIndex(snapshot.kind, target.index)
		m.completionMouse.press = target
		m.completionMouse.pressActive = true
	}
	return true, nil
}

func (m *Model) handleCompletionMouseRelease(mouse tea.Mouse) (bool, tea.Cmd) {
	if !m.completionMouse.pressActive {
		return false, nil
	}
	pressed := m.completionMouse.press
	m.cancelCompletionMousePress()
	if mouse.Button != tea.MouseLeft && mouse.Button != tea.MouseNone {
		return true, nil
	}
	snapshot, geometry, ok := m.completionMouseContext()
	if !ok {
		return true, nil
	}
	released, hit := m.completionMouseTargetAt(snapshot, geometry, mouse)
	if !hit || released.kind != pressed.kind || released.key != pressed.key {
		return true, nil
	}
	m.pinCompletionWindow(geometry)
	m.setCompletionIndex(snapshot.kind, released.index)
	return true, m.activateCompletion(snapshot.kind)
}

func (m *Model) handleCompletionMouseWheel(mouse tea.Mouse) (bool, tea.Cmd) {
	return m.handleCompletionMouseWheelAt(mouse, time.Now())
}

func (m *Model) handleCompletionMouseWheelAt(mouse tea.Mouse, at time.Time) (bool, tea.Cmd) {
	snapshot, geometry, ok := m.completionMouseContext()
	if !ok || !geometry.contains(mouse) {
		return false, nil
	}
	delta := 0
	switch mouse.Button {
	case tea.MouseWheelUp:
		delta = -1
	case tea.MouseWheelDown:
		delta = 1
	default:
		return true, nil
	}
	m.cancelCompletionMousePress()
	m.pinCompletionWindow(geometry)
	if !m.acceptCompletionWheelPulse(snapshot.kind, delta, at) {
		return true, nil
	}
	m.moveActiveCompletionSelection(delta, false)
	return true, nil
}
