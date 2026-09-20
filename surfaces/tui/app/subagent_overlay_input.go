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
	keyEvent := msg.Key()
	if state.loading {
		if keyEvent.Code == tea.KeyEscape {
			m.backSubagentOverlay()
		}
		return nil
	}
	if state.err != "" && len(state.status.Handles) == 0 {
		switch keyEvent.Code {
		case tea.KeyEnter:
			return m.openSubagentOverlay()
		case tea.KeyEscape:
			m.backSubagentOverlay()
		}
		return nil
	}
	if keyEvent.Mod.Contains(tea.ModCtrl) {
		switch keyEvent.Code {
		case 'p':
			if state.page == subagentPageMain {
				m.openSubagentPage(subagentPageSets, 0)
			}
		case 'n':
			if state.page == subagentPageMain {
				m.openNewSubagentRole()
			}
		case 's':
			if state.page == subagentPageMain || state.page == subagentPageSets {
				m.openSaveSubagentSet()
			}
		case 'w':
			if state.searchable() {
				m.searchSubagentRows("")
			}
		}
		return nil
	}
	switch keyEvent.Code {
	case tea.KeyEscape:
		m.backSubagentOverlay()
		return nil
	case tea.KeyLeft:
		return m.adjustSubagentControl(-1, false)
	case tea.KeyRight:
		return m.adjustSubagentControl(1, false)
	case tea.KeyTab:
		delta := 1
		if keyEvent.Mod.Contains(tea.ModShift) {
			delta = -1
		}
		if state.page == subagentPageMain || state.page == subagentPageBinding {
			m.moveSubagentField(delta)
		} else {
			m.moveSubagentSelection(delta)
		}
		return nil
	case tea.KeyUp:
		m.moveSubagentSelection(-1)
		return nil
	case tea.KeyDown:
		m.moveSubagentSelection(1)
		return nil
	case tea.KeyEnter:
		if m.editingSubagentField() {
			m.moveSubagentSelection(1)
			return nil
		}
		return m.activateSubagentRow(m.currentSubagentRow())
	case tea.KeyDelete:
		m.prepareSubagentDelete()
		return nil
	case tea.KeyBackspace:
		if m.editingSubagentField() {
			m.backspaceSubagentField()
		} else if state.searchable() {
			m.searchSubagentRows(trimLastRune(state.query))
		}
		return nil
	}
	text := keyEvent.Text
	if text == "" || keyEvent.Mod.Contains(tea.ModCtrl) || keyEvent.Mod.Contains(tea.ModAlt) {
		return nil
	}
	if m.editingSubagentField() {
		m.appendSubagentField(text)
		return nil
	}
	if strings.EqualFold(text, "f") && state.query == "" && m.currentSubagentRow().fastSupported &&
		(state.page == subagentPageBinding || state.page == subagentPageMain && state.field == subagentFieldModel) {
		return m.adjustSubagentControl(0, true)
	}
	if state.searchable() {
		m.searchSubagentRows(state.query + text)
	}
	return nil
}

func (m *Model) handleSubagentOverlayPaste(msg tea.PasteMsg) tea.Cmd {
	if m == nil || m.subagentOverlay == nil || m.subagentOverlay.pending || m.subagentOverlay.loading {
		return nil
	}
	text := normalizeClipboardText(msg.String())
	text = strings.ReplaceAll(text, "\n", " ")
	if m.editingSubagentField() {
		m.appendSubagentField(text)
	} else if m.subagentOverlay.searchable() {
		m.searchSubagentRows(m.subagentOverlay.query + text)
	}
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
	// The modal captures every mouse event while open so clicks cannot leak to
	// the underlying transcript or prompt.
	mouse := msg.Mouse()
	geometry := state.geometry
	inside := mouse.X >= geometry.x && mouse.X < geometry.x+geometry.width &&
		mouse.Y >= geometry.y && mouse.Y < geometry.y+geometry.height
	switch msg.(type) {
	case tea.MouseWheelMsg:
		if !inside {
			return true, nil
		}
		switch mouse.Button {
		case tea.MouseWheelUp:
			m.moveSubagentSelection(-1)
		case tea.MouseWheelDown:
			m.moveSubagentSelection(1)
		}
		return true, nil
	case tea.MouseMotionMsg:
		if !inside {
			return true, nil
		}
		if index := subagentRowAtY(geometry.rows, mouse.Y); index >= 0 {
			if state.index != index {
				state.notice = ""
			}
			state.index = index
			m.focusSubagentCell(index, mouse.X)
		}
		return true, nil
	case tea.MouseClickMsg:
		if mouse.Button != tea.MouseLeft || !inside {
			state.pressedKey = ""
			return true, nil
		}
		if mouse.Y == geometry.closeY && mouse.X >= geometry.closeX-1 && mouse.X <= geometry.closeX+1 {
			state.pressedKey = "close"
			return true, nil
		}
		if index := subagentRowAtY(geometry.rows, mouse.Y); index >= 0 && index < len(state.rows) {
			state.index = index
			state.pressedKey = state.rows[index].key
			state.pressedField, state.pressedDelta = m.focusSubagentCell(index, mouse.X)
		} else {
			state.pressedKey = ""
		}
		return true, nil
	case tea.MouseReleaseMsg:
		pressed := state.pressedKey
		state.pressedKey = ""
		if !inside {
			return true, nil
		}
		if pressed == "close" && mouse.Y == geometry.closeY && mouse.X >= geometry.closeX-1 && mouse.X <= geometry.closeX+1 {
			m.subagentOverlay = nil
			return true, nil
		}
		if index := subagentRowAtY(geometry.rows, mouse.Y); index >= 0 && index < len(state.rows) {
			state.index = index
			field, delta := m.focusSubagentCell(index, mouse.X)
			if state.rows[index].key == pressed && field == state.pressedField && delta == state.pressedDelta {
				if field == subagentFieldEffort {
					return true, m.adjustSubagentControl(delta, false)
				}
				if field == subagentFieldFast {
					return true, m.adjustSubagentControl(0, true)
				}
				return true, m.activateSubagentRow(state.rows[index])
			}
		}
		return true, nil
	default:
		return true, nil
	}
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
	if state == nil || len(state.rows) == 0 || delta == 0 {
		return
	}
	state.index = (state.index + delta + len(state.rows)) % len(state.rows)
	state.notice = ""
	state.pressedKey = ""
	m.normalizeSubagentField()
}

func (m *Model) activateSubagentRow(row subagentOverlayRow) tea.Cmd {
	state := m.subagentOverlay
	if state == nil || state.pending || !row.enabled {
		return nil
	}
	switch row.action {
	case subagentActionOpenSets:
		m.openSubagentPage(subagentPageSets, 0)
	case subagentActionOpenBinding:
		if state.page == subagentPageBinding {
			return m.chooseSubagentBinding(row)
		}
		state.bindingHandle = row.handle
		if row.companion != nil && state.field == subagentFieldAuxiliary {
			state.bindingHandle = row.companion.handle
		}
		state.creatingRole = false
		state.selectedEffortByProfile = nil
		state.selectedSpeedByProfile = nil
		m.openSubagentPage(subagentPageBinding, 0)
	case subagentActionNewRole:
		m.openNewSubagentRole()
	case subagentActionSaveSet:
		m.openSaveSubagentSet()
	case subagentActionApplySet:
		state.navigateAfterMutation(subagentPageMain, 0)
		return m.runSubagentMutation(func(ctx context.Context, service agentbinding.ConfigurationService) (agentbinding.Status, error) {
			return service.ApplyAgentBindingSet(ctx, strings.TrimPrefix(row.key, "set:"))
		})
	case subagentActionFieldBind:
		handle := agentbinding.NormalizeHandle(agentbinding.Handle(state.roleHandle))
		state.bindingHandle = handle
		state.creatingRole = true
		state.selectedEffortByProfile = nil
		state.selectedSpeedByProfile = nil
		m.openSubagentPage(subagentPageBinding, 0)
	case subagentActionCreateRole:
		return m.createSubagentRole()
	case subagentActionCommitSet:
		return m.saveSubagentSet()
	case subagentActionCancel:
		m.backSubagentOverlay()
	case subagentActionConfirm:
		return m.confirmSubagentDelete()
	}
	return nil
}

func (m *Model) chooseSubagentBinding(row subagentOverlayRow) tea.Cmd {
	state := m.subagentOverlay
	if state == nil {
		return nil
	}
	if state.creatingRole {
		if row.reset {
			return nil
		}
		state.roleBinding = row.binding
		m.backSubagentOverlay()
		return nil
	}
	state.navigateAfterMutation(subagentPageMain, 0)
	if row.reset {
		handle := state.bindingHandle
		return m.runSubagentMutation(func(ctx context.Context, service agentbinding.ConfigurationService) (agentbinding.Status, error) {
			return service.ResetAgentBinding(ctx, handle)
		})
	}
	binding := row.binding
	return m.runSubagentMutation(func(ctx context.Context, service agentbinding.ConfigurationService) (agentbinding.Status, error) {
		return service.BindAgentBinding(ctx, binding)
	})
}

func (m *Model) openNewSubagentRole() {
	state := m.subagentOverlay
	if state == nil {
		return
	}
	state.roleHandle = ""
	state.roleDescription = ""
	state.roleBinding = agentbinding.Binding{}
	state.creatingRole = false
	m.openSubagentPage(subagentPageNewRole, 0)
}

func (m *Model) createSubagentRole() tea.Cmd {
	state := m.subagentOverlay
	if state == nil {
		return nil
	}
	handle := agentbinding.NormalizeHandle(agentbinding.Handle(state.roleHandle))
	if err := agentbinding.ValidateCustomHandle(handle); err != nil {
		state.err = err.Error()
		return nil
	}
	description := strings.TrimSpace(state.roleDescription)
	if description == "" {
		state.err = "description is required"
		return nil
	}
	if state.roleBinding.ProfileID == "" || state.roleBinding.Effort == "" {
		state.err = "choose an initial binding"
		return nil
	}
	role := agentbinding.Role{Handle: handle, Description: description}
	binding := state.roleBinding
	binding.Handle = handle
	state.navigateAfterMutation(subagentPageMain, 0)
	state.afterMutation.key = "handle:" + string(handle)
	state.afterMutation.query = ""
	return m.runSubagentMutation(func(ctx context.Context, service agentbinding.ConfigurationService) (agentbinding.Status, error) {
		return service.CreateAgentRole(ctx, role, binding)
	})
}

func (m *Model) openSaveSubagentSet() {
	if m.subagentOverlay == nil {
		return
	}
	m.subagentOverlay.setName = ""
	m.openSubagentPage(subagentPageSaveSet, 0)
}

func (m *Model) saveSubagentSet() tea.Cmd {
	state := m.subagentOverlay
	if state == nil {
		return nil
	}
	name := agentbinding.NormalizeSetName(state.setName)
	if err := agentbinding.ValidateSetName(name); err != nil {
		state.err = err.Error()
		return nil
	}
	state.navigateAfterMutation(subagentPageSets, 0)
	state.afterMutation.key = "set:" + name
	state.afterMutation.query = ""
	return m.runSubagentMutation(func(ctx context.Context, service agentbinding.ConfigurationService) (agentbinding.Status, error) {
		return service.SaveAgentBindingSet(ctx, name)
	})
}

func (m *Model) prepareSubagentDelete() {
	state := m.subagentOverlay
	row := m.currentSubagentRow()
	if state == nil {
		return
	}
	switch {
	case state.page == subagentPageMain && row.custom:
		state.confirmHandle = row.handle
		state.confirmSet = ""
		state.confirmLabel = "custom role " + string(row.handle)
		m.openSubagentPage(subagentPageConfirm, 1)
	case state.page == subagentPageSets && strings.HasPrefix(row.key, "set:"):
		state.confirmHandle = ""
		state.confirmSet = strings.TrimPrefix(row.key, "set:")
		state.confirmLabel = "binding set " + state.confirmSet
		m.openSubagentPage(subagentPageConfirm, 1)
	}
}

func (m *Model) confirmSubagentDelete() tea.Cmd {
	state := m.subagentOverlay
	if state == nil {
		return nil
	}
	if state.confirmHandle != "" {
		handle := state.confirmHandle
		state.navigateAfterMutation(subagentPageMain, 0)
		return m.runSubagentMutation(func(ctx context.Context, service agentbinding.ConfigurationService) (agentbinding.Status, error) {
			return service.DeleteAgentRole(ctx, handle)
		})
	}
	name := state.confirmSet
	state.navigateAfterMutation(subagentPageSets, 0)
	return m.runSubagentMutation(func(ctx context.Context, service agentbinding.ConfigurationService) (agentbinding.Status, error) {
		return service.DeleteAgentBindingSet(ctx, name)
	})
}

func (m *Model) editingSubagentField() bool {
	row := m.currentSubagentRow()
	state := m.subagentOverlay
	if state == nil {
		return false
	}
	switch row.action {
	case subagentActionFieldHandle, subagentActionFieldDesc, subagentActionFieldSetName:
		return true
	default:
		return false
	}
}

func (m *Model) appendSubagentField(text string) {
	state := m.subagentOverlay
	row := m.currentSubagentRow()
	if state == nil || text == "" {
		return
	}
	switch row.action {
	case subagentActionFieldHandle:
		state.roleHandle = truncateRunes(state.roleHandle+text, 32)
	case subagentActionFieldDesc:
		state.roleDescription = truncateRunes(state.roleDescription+text, 160)
	case subagentActionFieldSetName:
		state.setName = truncateRunes(state.setName+text, 32)
	}
	state.err = ""
	m.refreshSubagentRows(row.key)
}

func (m *Model) backspaceSubagentField() {
	state := m.subagentOverlay
	row := m.currentSubagentRow()
	if state == nil {
		return
	}
	switch row.action {
	case subagentActionFieldHandle:
		state.roleHandle = trimLastRune(state.roleHandle)
	case subagentActionFieldDesc:
		state.roleDescription = trimLastRune(state.roleDescription)
	case subagentActionFieldSetName:
		state.setName = trimLastRune(state.setName)
	}
	state.err = ""
	m.refreshSubagentRows(row.key)
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
