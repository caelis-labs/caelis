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
			return true, nil
		}
		if subagentOutputCloseHit(geometry, mouse) {
			state.pressedItem = "close"
			return true, nil
		}
		if index := subagentOutputRowAtY(geometry, mouse.Y); index >= 0 {
			state.pressedItem = subagentOutputRowItem(geometry, index)
		} else {
			state.pressedItem = ""
		}
		return true, nil
	case tea.MouseReleaseMsg:
		pressed := state.pressedItem
		state.pressedItem = ""
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
	if view == nil || view.block == nil {
		return false
	}
	token = strings.TrimSpace(token)
	switch {
	case strings.HasPrefix(token, "acp_tool_panel:"):
		callID := strings.TrimSpace(strings.TrimPrefix(token, "acp_tool_panel:"))
		if !view.block.toggleToolPanelClick(callID) {
			return false
		}
	case strings.HasPrefix(token, "acp_reasoning:"):
		key := strings.TrimSpace(strings.TrimPrefix(token, "acp_reasoning:"))
		if !view.block.toggleReasoningExpanded(key) {
			return false
		}
	case strings.HasPrefix(token, "acp_exploration_stage:"):
		key := strings.TrimSpace(strings.TrimPrefix(token, "acp_exploration_stage:"))
		if !view.block.toggleExplorationExpanded(key) {
			return false
		}
	case strings.HasPrefix(token, "acp_exploration_stable:"):
		key := strings.TrimSpace(strings.TrimPrefix(token, "acp_exploration_stable:"))
		if !view.block.toggleExplorationExpanded(key) {
			return false
		}
	default:
		return false
	}
	view.touch(true)
	return true
}
