package tuiapp

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
)

func (m *Model) wizardRowCount() int {
	if len(m.wizardOverlay.fields) > 0 {
		return len(m.wizardOverlay.fields) + 1 // final action
	}
	n := len(m.slashArgCandidates)
	if (m.wizardStepKey() == "model" || m.wizardStepKey() == "endpoint") && !m.slashArgLoadPending && !m.slashArgRequestPending {
		n++ // custom model or endpoint
	}
	return n
}

func (m *Model) moveWizardSelection(delta int) {
	s := m.wizardOverlay
	if count := m.wizardRowCount(); count > 0 {
		if len(s.fields) > 0 {
			s.field = (s.field + delta + count) % count
			if s.field < len(s.fields) {
				s.cursor = len([]rune(s.fields[s.field].value))
			}
		} else {
			m.slashArgIndex = (m.slashArgIndex + delta + count) % count
			if m.isBotSettingsModel() && m.slashArgIndex < len(m.slashArgCandidates) {
				m.modelPicker.selected = m.slashArgCandidates[m.slashArgIndex].Value
			}
		}
	}
	s.err, s.pressed = "", ""
}

func (m *Model) acceptWizardOverlay() tea.Cmd {
	s := m.wizardOverlay
	if s.pending || m.slashArgLoadPending || m.slashArgRequestPending {
		return nil
	}
	if s.blocked {
		m.clearWizard()
		return nil
	}
	if s.bot != nil {
		if s.err != "" && s.bot.choosingModel {
			s.err = ""
			return m.requestCurrentSlashArgCompletion()
		}
		return m.acceptBotSettings()
	}
	if len(s.fields) > 0 {
		if m.wizard.def.Command == "plugin" {
			value := strings.TrimSpace(s.fields[0].value)
			if value == "" {
				s.err = "Enter a source"
				return nil
			}
			return m.advanceWizardStep(value)
		}
		return m.submitConnectForm()
	}
	if s.err != "" && s.submitID == 0 {
		s.err = ""
		return m.requestCurrentSlashArgCompletion()
	}
	if m.wizardMultiSelectStep() && len(m.wizard.multiSelections[m.wizardStepKey()]) > 0 {
		_, cmd := m.handleWizardEnter()
		return cmd
	}
	if m.wizardStepKey() == "model" && m.slashArgIndex == len(m.slashArgCandidates) {
		m.openConnectCustomModel()
		return nil
	}
	if m.wizardStepKey() == "endpoint" && m.slashArgIndex == len(m.slashArgCandidates) {
		m.openConnectCustomEndpoint()
		return nil
	}
	if len(m.slashArgCandidates) == 0 {
		return nil
	}
	_, cmd := m.handleWizardEnter()
	return cmd
}

func (m *Model) handleWizardOverlayKey(msg tea.KeyMsg) tea.Cmd {
	s := m.wizardOverlay
	if s == nil {
		return nil
	}
	if s.pending {
		if s.bot != nil && s.bot.loading && msg.Key().Code == tea.KeyEscape {
			m.clearWizard()
		}
		return nil
	}
	if _, release := msg.(tea.KeyReleaseMsg); release {
		return nil
	}
	k := msg.Key()
	if k.Code == tea.KeyEscape {
		return m.backWizardOverlay()
	}
	if s.blocked {
		if k.Code == tea.KeyEnter {
			m.clearWizard()
		}
		return nil
	}
	if m.slashArgLoadPending || m.slashArgRequestPending && len(s.fields) > 0 {
		return nil
	}
	if k.Mod.Contains(tea.ModCtrl) {
		switch k.Code {
		case 'n':
			if len(s.fields) == 0 {
				m.openConnectCustomModel()
			}
		case 'u', 'w':
			if len(s.fields) > 0 {
				if s.field < len(s.fields) && !s.fields[s.field].choice && s.fields[s.field].key != "bot_model" {
					m.setWizardFieldValue("")
				}
			} else {
				return m.wizardSearch("")
			}
		}
		return nil
	}
	if m.isBotSettingsModel() && (k.Code == tea.KeyLeft || k.Code == tea.KeyRight || k.Code == tea.KeyTab) {
		_, cmd := m.handleModelPickerKey(msg)
		return cmd
	}
	switch k.Code {
	case tea.KeyUp:
		m.moveWizardSelection(-1)
	case tea.KeyDown:
		m.moveWizardSelection(1)
	case tea.KeyTab:
		delta := 1
		if k.Mod.Contains(tea.ModShift) {
			delta = -1
		}
		m.moveWizardSelection(delta)
	case tea.KeyEnter:
		if s.bot != nil && len(s.fields) > 0 && s.field < len(s.fields) {
			if s.fields[s.field].key == "bot_model" {
				return m.openBotSettingsModels()
			}
			m.moveWizardSelection(1)
			return nil
		}
		return m.acceptWizardOverlay()
	default:
		if len(s.fields) > 0 {
			m.editWizardField(k)
		} else if k.Code == tea.KeyBackspace {
			return m.wizardSearch(trimLastRune(m.slashArgQuery))
		} else if k.Code == tea.KeySpace && m.wizardMultiSelectStep() {
			if m.slashArgIndex < len(m.slashArgCandidates) {
				_, cmd := m.toggleWizardMultiSelectCandidate(m.slashArgCandidates[m.slashArgIndex])
				return cmd
			}
		} else if k.Text != "" && !k.Mod.Contains(tea.ModAlt) {
			return m.wizardSearch(m.slashArgQuery + k.Text)
		}
	}
	return nil
}

func (m *Model) editWizardField(k tea.Key) {
	s := m.wizardOverlay
	if s.field >= len(s.fields) {
		return
	}
	f := &s.fields[s.field]
	if f.key == "bot_model" {
		return
	}
	if f.choice {
		if k.Code == tea.KeyLeft || k.Code == tea.KeyRight || k.Code == tea.KeySpace {
			if f.value == "true" {
				f.value = "false"
			} else {
				f.value = "true"
			}
		}
		return
	}
	value := []rune(f.value)
	s.cursor = clampInt(s.cursor, 0, len(value))
	switch k.Code {
	case tea.KeyLeft:
		s.cursor = max(0, s.cursor-1)
	case tea.KeyRight:
		s.cursor = min(len(value), s.cursor+1)
	case tea.KeyHome:
		s.cursor = 0
	case tea.KeyEnd:
		s.cursor = len(value)
	case tea.KeyBackspace:
		if s.cursor > 0 {
			value = append(value[:s.cursor-1], value[s.cursor:]...)
			s.cursor--
		}
	case tea.KeyDelete:
		if s.cursor < len(value) {
			value = append(value[:s.cursor], value[s.cursor+1:]...)
		}
	default:
		if k.Text != "" && !k.Mod.Contains(tea.ModAlt) {
			insert := []rune(k.Text)
			value = append(append(append([]rune(nil), value[:s.cursor]...), insert...), value[s.cursor:]...)
			s.cursor += len(insert)
		}
	}
	m.setWizardFieldValue(string(value))
}

func (m *Model) setWizardFieldValue(value string) {
	s := m.wizardOverlay
	f := &s.fields[s.field]
	updated := truncateRunes(value, 8192)
	if (f.key == "baseurl" || f.key == "endpoint") && updated != f.value {
		for i := range s.fields {
			if s.fields[i].secret {
				s.fields[i].value = ""
			}
		}
	}
	f.value = updated
	s.cursor = min(s.cursor, len([]rune(f.value)))
	s.err = ""
}

func (m *Model) handleWizardOverlayPaste(msg tea.PasteMsg) tea.Cmd {
	s := m.wizardOverlay
	if s.pending || s.blocked || m.slashArgLoadPending {
		return nil
	}
	text := normalizeClipboardText(msg.String())
	if s.bot == nil || s.field >= len(s.fields) || s.fields[s.field].key != "description" {
		text = strings.ReplaceAll(text, "\n", " ")
	}
	if len(s.fields) > 0 {
		m.editWizardField(tea.Key{Text: text})
		return nil
	}
	return m.wizardSearch(m.slashArgQuery + text)
}

func (m *Model) handleWizardOverlayMouse(msg tea.MouseMsg) tea.Cmd {
	s := m.wizardOverlay
	if s.pending && (s.bot == nil || !s.bot.loading) {
		return nil
	}
	mouse, g := msg.Mouse(), s.geometry
	inside := mouse.X >= g.x && mouse.X < g.x+g.width && mouse.Y >= g.y && mouse.Y < g.y+g.height
	target := ""
	if inside {
		switch {
		case mouse.Y == g.closeY && mouse.X >= g.closeX-1:
			target = "close"
		case mouse.Y == g.actionY && mouse.X >= g.actionX:
			target = "action"
		default:
			if index := subagentRowAtY(g.rows, mouse.Y); index >= 0 {
				target = "row:" + strconv.Itoa(index)
			}
		}
	}
	switch msg.(type) {
	case tea.MouseWheelMsg:
		if inside && !m.slashArgLoadPending && !s.blocked {
			if mouse.Button == tea.MouseWheelUp {
				m.moveWizardSelection(-1)
			}
			if mouse.Button == tea.MouseWheelDown {
				m.moveWizardSelection(1)
			}
		}
	case tea.MouseClickMsg:
		if mouse.Button == tea.MouseLeft {
			s.pressed = target
		}
	case tea.MouseReleaseMsg:
		pressed := s.pressed
		s.pressed = ""
		if target == "" || target != pressed {
			return nil
		}
		if target == "close" {
			m.clearWizard()
			return nil
		}
		if target == "action" {
			return m.acceptWizardOverlay()
		}
		if index := subagentRowAtY(g.rows, mouse.Y); index >= 0 && !m.slashArgLoadPending && !s.blocked {
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
