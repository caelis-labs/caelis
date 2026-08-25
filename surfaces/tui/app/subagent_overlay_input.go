package tuiapp

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

func (m *Model) handleSubagentOverlayKey(msg tea.KeyMsg) tea.Cmd {
	state := m.subagentOverlay
	if state == nil {
		return nil
	}
	if _, ok := msg.(tea.KeyReleaseMsg); ok {
		return nil
	}
	keyEvent := msg.Key()
	switch keyEvent.Code {
	case tea.KeyEscape:
		m.backSubagentOverlay()
		return nil
	case tea.KeyLeft:
		m.moveSubagentEffort(-1)
		return nil
	case tea.KeyRight:
		m.moveSubagentEffort(1)
		return nil
	case tea.KeyTab:
		m.moveSubagentSelection(1)
		return nil
	case tea.KeyUp:
		m.moveSubagentSelection(-1)
		return nil
	case tea.KeyDown:
		m.moveSubagentSelection(1)
		return nil
	case tea.KeyEnter:
		return m.activateSubagentRow(m.currentSubagentRow())
	case tea.KeyBackspace:
		if m.editingSubagentField() {
			m.backspaceSubagentField()
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
	switch strings.ToLower(text) {
	case "j":
		m.moveSubagentSelection(1)
	case "k":
		m.moveSubagentSelection(-1)
	case "h":
		m.moveSubagentEffort(-1)
	case "l":
		m.moveSubagentEffort(1)
	case "p":
		if state.page == subagentPageMain {
			m.openSubagentPage(subagentPageSets, 0)
		}
	case "n":
		if state.page == subagentPageMain {
			m.openNewSubagentRole()
		}
	case "s":
		if state.page == subagentPageMain || state.page == subagentPageSets {
			m.openSaveSubagentSet()
		}
	case "d":
		m.prepareSubagentDelete()
	}
	return nil
}

func (m *Model) handleSubagentOverlayPaste(msg tea.PasteMsg) tea.Cmd {
	if m == nil || m.subagentOverlay == nil || !m.editingSubagentField() {
		return nil
	}
	text := normalizeClipboardText(msg.String())
	text = strings.ReplaceAll(text, "\n", " ")
	m.appendSubagentField(text)
	return nil
}

func (m *Model) handleSubagentOverlayMouse(msg tea.MouseMsg) (bool, tea.Cmd) {
	state := m.subagentOverlay
	if state == nil {
		return false, nil
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
		if index := subagentRowAtY(geometry.rows, mouse.Y); index >= 0 {
			state.index = index
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
		} else {
			state.pressedKey = ""
		}
		return true, nil
	case tea.MouseReleaseMsg:
		pressed := state.pressedKey
		state.pressedKey = ""
		if pressed == "close" && mouse.Y == geometry.closeY && mouse.X >= geometry.closeX-1 && mouse.X <= geometry.closeX+1 {
			m.subagentOverlay = nil
			return true, nil
		}
		if index := subagentRowAtY(geometry.rows, mouse.Y); index >= 0 && index < len(state.rows) {
			state.index = index
			if state.rows[index].key == pressed {
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
		if rowY == y {
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
	state.pressedKey = ""
}

func (m *Model) moveSubagentEffort(delta int) {
	state := m.subagentOverlay
	if state == nil || state.page != subagentPageBinding || delta == 0 || len(state.rows) == 0 {
		return
	}
	row := m.currentSubagentRow()
	if len(row.efforts) < 2 || row.binding.ProfileID == "" {
		return
	}
	index := (row.effortIndex + delta + len(row.efforts)) % len(row.efforts)
	effort := row.efforts[index]
	profileID := modelprofile.NormalizeID(row.binding.ProfileID)
	if state.selectedEffortByProfile == nil {
		state.selectedEffortByProfile = make(map[string]string)
	}
	state.selectedEffortByProfile[profileID] = effort
	state.rows[state.index].effortIndex = index
	state.rows[state.index].binding.Effort = effort
	state.pressedKey = ""
}

func (m *Model) openSubagentPage(page subagentOverlayPage, index int) {
	if m.subagentOverlay == nil {
		return
	}
	m.subagentOverlay.page = page
	m.subagentOverlay.index = index
	m.subagentOverlay.err = ""
	m.subagentOverlay.pressedKey = ""
}

func (m *Model) backSubagentOverlay() {
	state := m.subagentOverlay
	if state == nil {
		return
	}
	switch state.page {
	case subagentPageMain:
		m.subagentOverlay = nil
	case subagentPageBinding:
		if state.creatingRole {
			state.creatingRole = false
			m.openSubagentPage(subagentPageNewRole, 2)
		} else {
			m.openSubagentPage(subagentPageMain, 0)
		}
	default:
		m.openSubagentPage(subagentPageMain, 0)
	}
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
		state.creatingRole = false
		state.selectedEffortByProfile = nil
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
		state.creatingRole = false
		m.openSubagentPage(subagentPageNewRole, 2)
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
		m.openSubagentPage(subagentPageConfirm, 0)
	case state.page == subagentPageSets && strings.HasPrefix(row.key, "set:"):
		state.confirmHandle = ""
		state.confirmSet = strings.TrimPrefix(row.key, "set:")
		state.confirmLabel = "binding set " + state.confirmSet
		m.openSubagentPage(subagentPageConfirm, 0)
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
}

func (s *subagentOverlayState) navigateAfterMutation(page subagentOverlayPage, index int) {
	if s == nil {
		return
	}
	s.afterMutation = &subagentOverlayNav{page: page, index: index}
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
