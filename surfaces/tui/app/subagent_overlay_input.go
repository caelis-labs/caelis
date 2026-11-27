package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/control/agentbinding"
)

func (m *Model) handleSubagentOverlayKey(msg tea.KeyMsg) tea.Cmd {
	state := m.subagentOverlay
	if state == nil || state.pending {
		return nil
	}
	if _, ok := msg.(tea.KeyReleaseMsg); ok {
		return nil
	}
	key := msg.Key()
	if state.reloadRequired {
		switch key.Code {
		case tea.KeyEnter:
			return m.openSubagentOverlay()
		case tea.KeyEscape:
			m.subagentOverlay = nil
		}
		return nil
	}
	if state.loading || (state.err != "" && len(state.status.Handles) == 0) {
		if key.Code == tea.KeyEscape {
			m.backSubagentOverlay()
		} else if !state.loading && key.Code == tea.KeyEnter {
			return m.openSubagentOverlay()
		}
		return nil
	}
	if key.Mod.Contains(tea.ModCtrl) {
		if key.Code == 'w' {
			m.searchSubagentRows("")
		}
		return nil
	}
	switch key.Code {
	case tea.KeyEscape:
		m.backSubagentOverlay()
	case tea.KeyLeft:
		m.adjustSubagentEffort(-1)
	case tea.KeyRight:
		m.adjustSubagentEffort(1)
	case tea.KeyTab:
		return nil
	case tea.KeyUp:
		m.moveSubagentSelection(-1)
	case tea.KeyDown:
		m.moveSubagentSelection(1)
	case tea.KeyEnter:
		return m.activateSubagentRow(m.currentSubagentRow())
	case tea.KeyBackspace:
		m.searchSubagentRows(trimLastRune(state.query))
	default:
		if key.Text != "" && !key.Mod.Contains(tea.ModAlt) {
			m.searchSubagentRows(state.query + key.Text)
		}
	}
	return nil
}

func (m *Model) handleSubagentOverlayPaste(msg tea.PasteMsg) tea.Cmd {
	if m == nil || m.subagentOverlay == nil || m.subagentOverlay.pending || m.subagentOverlay.loading || m.subagentOverlay.reloadRequired {
		return nil
	}
	text := strings.ReplaceAll(normalizeClipboardText(msg.String()), "\n", " ")
	m.searchSubagentRows(m.subagentOverlay.query + text)
	return nil
}

func (m *Model) handleSubagentOverlayMouse(msg tea.MouseMsg) (bool, tea.Cmd) {
	state := m.subagentOverlay
	if state == nil || m.activePrompt != nil {
		return false, nil
	}
	if state.pending || state.loading || state.reloadRequired {
		return true, nil
	}
	mouse := msg.Mouse()
	g := state.geometry
	inside := mouse.X >= g.x && mouse.X < g.x+g.width && mouse.Y >= g.y && mouse.Y < g.y+g.height
	index := subagentRowAtY(g.rows, mouse.Y)
	if index >= len(state.rows) {
		index = -1
	}
	delta := 0
	if state.page == subagentPageBinding && index >= 0 && index < len(g.efforts) {
		effort := g.efforts[index]
		if mouse.X >= effort.x && mouse.X < effort.x+effort.width {
			delta = -1
			if mouse.X >= effort.midpoint {
				delta = 1
			}
		}
	}
	switch msg.(type) {
	case tea.MouseWheelMsg:
		state.pressedKey = ""
		if inside {
			switch mouse.Button {
			case tea.MouseWheelUp:
				m.moveSubagentSelection(-1)
			case tea.MouseWheelDown:
				m.moveSubagentSelection(1)
			}
		}
	case tea.MouseMotionMsg:
		if inside && index >= 0 {
			if state.index != index {
				state.notice = ""
			}
			state.index = index
		}
	case tea.MouseClickMsg:
		state.pressedKey = ""
		if mouse.Button == tea.MouseLeft && inside && index >= 0 {
			state.index = index
			state.pressedKey, state.pressedDelta = state.rows[index].key, delta
		}
	case tea.MouseReleaseMsg:
		pressed := state.pressedKey
		state.pressedKey = ""
		if inside && index >= 0 && pressed != "" && state.rows[index].key == pressed && delta == state.pressedDelta {
			row := state.rows[index]
			state.index = index
			state.notice = ""
			if delta != 0 && len(row.efforts) > 1 {
				m.adjustSubagentEffort(delta)
			} else {
				return true, m.activateSubagentRow(row)
			}
		}
	}
	return true, nil
}

func subagentRowAtY(rows []int, y int) int {
	for index, rowY := range rows {
		if rowY >= 0 && rowY == y {
			return index
		}
	}
	return -1
}

func (m *Model) currentSubagentRow() subagentOverlayRow {
	state := m.subagentOverlay
	if state == nil || state.index < 0 || state.index >= len(state.rows) {
		return subagentOverlayRow{}
	}
	return state.rows[state.index]
}

func (m *Model) moveSubagentSelection(delta int) {
	state := m.subagentOverlay
	if state == nil || delta == 0 || len(state.rows) == 0 {
		return
	}
	state.index = (state.index + delta + len(state.rows)) % len(state.rows)
	state.notice = ""
	state.pressedKey = ""
}

func (m *Model) adjustSubagentEffort(delta int) {
	state := m.subagentOverlay
	if state.page != subagentPageBinding {
		return
	}
	row := m.currentSubagentRow()
	if len(row.efforts) < 2 {
		return
	}
	index := clampInt(row.effortIndex+delta, 0, len(row.efforts)-1)
	if index == row.effortIndex {
		return
	}
	row.effortIndex, row.binding.Effort = index, row.efforts[index]
	state.selectedEffortByProfile[row.binding.ProfileID] = row.binding.Effort
	state.rows[state.index] = row
	state.err = ""
}

func (m *Model) activateSubagentRow(row subagentOverlayRow) tea.Cmd {
	state := m.subagentOverlay
	if state == nil || state.pending || row.key == "" {
		return nil
	}
	switch state.page {
	case subagentPageMain:
		m.openSubagentBinding(row.handle)
		return nil
	case subagentPageBinding:
		if state.hasClassifiers {
			state.stagedBinding = row.binding
			state.bindingParent = subagentOverlayNav{
				index: state.index, key: row.key, query: state.query,
			}
			m.restoreSubagentPage(subagentPageClassifier, subagentOverlayNav{})
			m.selectCurrentSubagentBinding(agentbinding.HandleGuardianScreen)
			return nil
		}
		current := state.currentBinding(row.binding.Handle)
		current.Handle = row.binding.Handle
		if agentbinding.Normalize(row.binding) == agentbinding.Normalize(current) {
			m.backSubagentOverlay()
			return nil
		}
		return m.runSubagentMutation([]agentbinding.Binding{row.binding})
	case subagentPageClassifier:
		var changes []agentbinding.Binding
		for _, draft := range []agentbinding.Binding{state.stagedBinding, row.binding} {
			if draft.Handle == "" {
				continue
			}
			current := state.currentBinding(draft.Handle)
			current.Handle = draft.Handle
			if agentbinding.Normalize(draft) != agentbinding.Normalize(current) {
				changes = append(changes, draft)
			}
		}
		if len(changes) == 0 {
			m.returnToSubagentMain()
			return nil
		}
		return m.runSubagentMutation(changes)
	}
	return nil
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if limit > 0 && len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}

func trimLastRune(value string) string {
	runes := []rune(value)
	if len(runes) == 0 {
		return ""
	}
	return string(runes[:len(runes)-1])
}
