package tuiapp

import (
	"context"
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
	if m == nil || m.subagentOverlay == nil || m.subagentOverlay.pending || m.subagentOverlay.loading {
		return nil
	}
	text := strings.ReplaceAll(normalizeClipboardText(msg.String()), "\n", " ")
	m.searchSubagentRows(m.subagentOverlay.query + text)
	return nil
}

func (m *Model) handleSubagentOverlayMouse(msg tea.MouseMsg) (bool, tea.Cmd) {
	state := m.subagentOverlay
	if state == nil {
		return false, nil
	}
	if state.pending || state.loading {
		return true, nil
	}
	mouse := msg.Mouse()
	g := state.geometry
	if handled, close := state.closeButton.mouse(msg, g.closeX, g.closeY); handled {
		state.pressedKey = ""
		if close {
			m.subagentOverlay = nil
		}
		return true, nil
	}
	inside := mouse.X >= g.x && mouse.X < g.x+g.width && mouse.Y >= g.y && mouse.Y < g.y+g.height
	index := subagentRowAtY(g.classifierRows, mouse.Y)
	classifier := index >= 0
	if !classifier {
		index = subagentRowAtY(g.rows, mouse.Y)
	}
	delta := 0
	if !classifier && index >= 0 && index < len(g.efforts) {
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
		if inside {
			switch mouse.Button {
			case tea.MouseWheelUp:
				m.moveSubagentSelection(-1)
			case tea.MouseWheelDown:
				m.moveSubagentSelection(1)
			}
		}
	case tea.MouseClickMsg:
		state.pressedKey = ""
		if mouse.Button == tea.MouseLeft && inside && index >= 0 {
			state.focusClassifier(classifier)
			state.index = index
			state.notice = ""
			state.pressedKey, state.pressedClassifier, state.pressedDelta = m.currentSubagentRow().key, classifier, delta
		}
	case tea.MouseReleaseMsg:
		pressed := state.pressedKey
		state.pressedKey = ""
		row := m.currentSubagentRow()
		if inside && index >= 0 && index == state.index && pressed != "" && row.key == pressed &&
			classifier == state.pressedClassifier && delta == state.pressedDelta {
			if delta != 0 && len(row.efforts) > 1 {
				m.adjustSubagentEffort(delta)
			} else if !state.guardianPicker() {
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
	if state == nil || delta == 0 {
		return
	}
	next := state.index + delta
	if (next < 0 || next >= len(state.rows)) && state.guardianPicker() {
		// Enter the adjacent section on its current draft, without changing it.
		state.focusClassifier(!state.classifierFocused)
	} else if len(state.rows) > 0 {
		state.index = (next + len(state.rows)) % len(state.rows)
	}
	state.notice = ""
	state.pressedKey = ""
}

func (m *Model) adjustSubagentEffort(delta int) {
	state := m.subagentOverlay
	if state.page != subagentPageBinding || state.classifierFocused {
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
	if state.page == subagentPageMain {
		m.openSubagentBinding(row.handle)
		return nil
	}
	if !state.guardianPicker() {
		return m.applySubagentBinding(row.binding)
	}
	if len(state.other.rows) == 0 {
		return nil
	}
	var changes []agentbinding.Binding
	for _, draft := range []agentbinding.Binding{row.binding, state.other.rows[state.other.index].binding} {
		current := state.currentBinding(draft.Handle)
		current.Handle = draft.Handle
		if agentbinding.Normalize(draft) != agentbinding.Normalize(current) {
			changes = append(changes, draft)
		}
	}
	switch len(changes) {
	case 0:
		m.backSubagentOverlay()
	case 1:
		return m.applySubagentBinding(changes[0])
	case 2:
		service, ok := m.cfg.ControlService.(agentbinding.GuardianBindingService)
		if !ok {
			state.err = "Guardian configuration is unavailable."
			return nil
		}
		return m.runSubagentMutation(func(ctx context.Context, _ agentbinding.Service) (agentbinding.Status, error) {
			return service.BindGuardian(ctx, changes)
		})
	}
	return nil
}

func (m *Model) applySubagentBinding(binding agentbinding.Binding) tea.Cmd {
	return m.runSubagentMutation(func(ctx context.Context, service agentbinding.Service) (agentbinding.Status, error) {
		if binding.ProfileID == "" {
			return service.ResetAgentBinding(ctx, binding.Handle)
		}
		return service.BindAgentBinding(ctx, binding)
	})
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
