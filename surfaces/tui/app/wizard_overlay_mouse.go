package tuiapp

import (
	"strconv"

	tea "charm.land/bubbletea/v2"
)

func wizardRowTarget(index int) string { return "row:" + strconv.Itoa(index) }

func (m *Model) wizardMouseTarget(mouse tea.Mouse) string {
	s, g := m.wizardOverlay, m.wizardOverlay.geometry
	if mouse.X < g.contentX || mouse.X >= g.contentX+g.contentWidth {
		return ""
	}
	switch {
	case m.wizardCloseEnabled() && mouse.Y == g.closeY && mouse.X >= g.closeX && mouse.X < g.closeX+3:
		return "close"
	case g.backWidth > 0 && mouse.Y == g.backY && mouse.X >= g.backX && mouse.X < g.backX+g.backWidth:
		return "back"
	case g.actionWidth > 0 && m.wizardActionEnabled() && mouse.Y == g.actionY && mouse.X >= g.actionX && mouse.X < g.actionX+g.actionWidth:
		return "action"
	case g.sendWidth > 0 && m.canSendACPInstallPrompt() && mouse.Y == g.sendY && mouse.X >= g.sendX && mouse.X < g.sendX+g.sendWidth:
		return "send"
	case !m.wizardBusy() && !s.blocked:
		if index := subagentRowAtY(g.rows, mouse.Y); index >= 0 {
			return wizardRowTarget(index)
		}
	}
	return ""
}

func (m *Model) handleWizardOverlayMouse(msg tea.MouseMsg) tea.Cmd {
	s := m.wizardOverlay
	if !m.wizardCloseEnabled() {
		s.hovered, s.pressed = "", ""
		return nil
	}
	mouse, g := msg.Mouse(), s.geometry
	target := m.wizardMouseTarget(mouse)
	inside := mouse.X >= g.contentX && mouse.X < g.contentX+g.contentWidth && mouse.Y >= g.y && mouse.Y < g.y+g.height
	switch msg.(type) {
	case tea.MouseMotionMsg:
		if s.text.selecting {
			s.hovered, s.pressed = "", ""
			if point, ok := m.wizardTextPoint(mouse, true); ok {
				s.text.end = point
			}
			m.selectionAutoScroll.mouse = mouse
			m.selectionAutoScroll.active = m.wizardSelectionScrollDelta(mouse) != 0
			return m.ensureSelectionAutoScrollTick()
		}
		s.hovered = target
		if s.pressed != target {
			s.pressed = ""
		}
		if (len(s.fields) == 0 || m.isWizardAuthChoice()) && target != "" && target == wizardRowTarget(subagentRowAtY(g.rows, mouse.Y)) {
			if m.isWizardAuthChoice() {
				m.activePrompt.choiceIndex = subagentRowAtY(g.rows, mouse.Y)
				return nil
			}
			m.slashArgIndex = subagentRowAtY(g.rows, mouse.Y)
			if m.isBotSettingsModel() && m.slashArgIndex < len(m.slashArgCandidates) {
				m.modelPicker.selected = m.slashArgCandidates[m.slashArgIndex].Value
			}
		}
	case tea.MouseWheelMsg:
		s.pressed, s.hovered = "", ""
		if !inside || m.wizardBusy() || s.blocked {
			return nil
		}
		delta := 0
		switch mouse.Button {
		case tea.MouseWheelUp:
			delta = -1
		case tea.MouseWheelDown:
			delta = 1
		}
		if delta == 0 {
			return nil
		}
		if s.text.selecting && m.isACPManualSetup() {
			_, cmd := m.scrollWizardSelectionBy(delta, mouse)
			return cmd
		}
		m.moveWizardSelection(delta)
	case tea.MouseClickMsg:
		s.pressed = ""
		s.text.selecting = false
		m.cancelSelectionAutoScroll()
		if mouse.Button != tea.MouseLeft {
			return nil
		}
		s.hovered, s.pressed = target, target
		if target == "" {
			if point, ok := m.wizardTextPoint(mouse, false); ok {
				m.clearSelection()
				m.clearInputSelection()
				m.clearFixedSelection()
				s.text.selecting, s.text.start, s.text.end = true, point, point
			}
		}
	case tea.MouseReleaseMsg:
		pressed := s.pressed
		s.pressed = ""
		if s.text.selecting {
			return m.finishWizardTextSelection(mouse)
		}
		if target == "" || target != pressed || (mouse.Button != tea.MouseLeft && mouse.Button != tea.MouseNone) {
			return nil
		}
		switch target {
		case "close":
			if m.isWizardAuthChoice() {
				return m.backWizardOverlay()
			}
			m.clearWizard()
			return nil
		case "back":
			if s.pending {
				m.clearWizard()
				return nil
			}
			return m.backWizardOverlay()
		case "action":
			s.footerFocus = "action"
			return m.acceptWizardOverlay()
		case "send":
			return m.sendACPInstallPrompt()
		}
		if index := subagentRowAtY(g.rows, mouse.Y); index >= 0 {
			if m.isWizardAuthChoice() {
				m.activePrompt.choiceIndex = index
				return m.acceptWizardOverlay()
			}
			if len(s.fields) > 0 {
				s.field, s.cursor = index, len([]rune(s.fields[index].value))
				if s.fields[index].key == "bot_model" {
					return m.openBotSettingsModels()
				}
				if s.fields[index].choice {
					m.editWizardField(tea.Key{Code: tea.KeySpace})
				}
			} else {
				m.slashArgIndex = index
				if m.wizardMultiSelectStep() && index < len(m.slashArgCandidates) {
					if handled, cmd := m.toggleWizardMultiSelectCandidate(m.slashArgCandidates[index]); handled {
						return cmd
					}
				}
				return m.acceptWizardOverlay()
			}
		}
	}
	return nil
}
