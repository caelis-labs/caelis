package tuiapp

import (
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
	if m.isWizardAuthChoice() {
		m.activePrompt.choiceIndex = wrapSelectionIndex(m.activePrompt.choiceIndex, len(m.visiblePromptChoices()), delta)
		return
	}
	if m.isACPManualSetup() {
		s.window = max(0, s.window+delta)
		return
	}
	if count := m.wizardRowCount(); count > 0 {
		if len(s.fields) > 0 {
			s.field = (s.field + delta + count) % count
			if s.field < len(s.fields) {
				s.cursor = len([]rune(s.fields[s.field].value))
			}
		} else {
			m.slashArgIndex = (m.slashArgIndex + delta + count) % count
		}
	}
	s.err, s.pressed = "", ""
}

func (m *Model) acceptWizardOverlay() tea.Cmd {
	s := m.wizardOverlay
	if m.isWizardAuthChoice() {
		return m.handlePromptKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	if s.pending || m.slashArgLoadPending || m.slashArgRequestPending {
		return nil
	}
	if s.blocked {
		m.clearWizard()
		return nil
	}
	if m.isACPManualSetup() {
		if s.footerFocus == "send" {
			return m.sendACPInstallPrompt()
		}
		if s.err != "" {
			return m.requestCurrentSlashArgCompletion()
		}
		return m.backWizardOverlay()
	}
	if m.wizardStepKey() == "acp_install" && len(s.fields) > 0 {
		return m.submitACPInstallation()
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
		return nil
	}
	if _, release := msg.(tea.KeyReleaseMsg); release {
		return nil
	}
	k := msg.Key()
	s.hovered, s.pressed = "", ""
	s.text.selecting = false
	m.cancelSelectionAutoScroll()
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
				if s.field < len(s.fields) && !s.fields[s.field].choice {
					m.setWizardFieldValue("")
				}
			} else {
				return m.wizardSearch("")
			}
		}
		return nil
	}
	switch k.Code {
	case tea.KeyUp:
		m.moveWizardSelection(-1)
	case tea.KeyDown:
		m.moveWizardSelection(1)
	case tea.KeyTab:
		if m.canSendACPInstallPrompt() {
			if s.footerFocus == "send" {
				s.footerFocus = "action"
			} else {
				s.footerFocus = "send"
			}
			return nil
		}
		delta := 1
		if k.Mod.Contains(tea.ModShift) {
			delta = -1
		}
		m.moveWizardSelection(delta)
	case tea.KeyEnter:
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
	cursor := clampInt(s.cursor, 0, len(value))
	switch k.Code {
	case tea.KeyLeft:
		s.cursor = max(0, cursor-1)
		return
	case tea.KeyRight:
		s.cursor = min(len(value), cursor+1)
		return
	case tea.KeyHome:
		s.cursor = 0
		return
	case tea.KeyEnd:
		s.cursor = len(value)
		return
	case tea.KeyBackspace:
		if cursor == 0 {
			return
		}
		value = append(value[:cursor-1], value[cursor:]...)
		cursor--
	case tea.KeyDelete:
		if cursor == len(value) {
			return
		}
		value = append(value[:cursor], value[cursor+1:]...)
	default:
		if k.Text == "" || k.Mod.Contains(tea.ModAlt) {
			return
		}
		insert := []rune(k.Text)
		value = append(append(append([]rune(nil), value[:cursor]...), insert...), value[cursor:]...)
		cursor += len(insert)
	}
	if m.setWizardFieldValue(string(value)) {
		s.cursor = min(cursor, len([]rune(f.value)))
	}
}

func (m *Model) setWizardFieldValue(value string) bool {
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
	return true
}

func (m *Model) handleWizardOverlayPaste(msg tea.PasteMsg) tea.Cmd {
	s := m.wizardOverlay
	if s.pending || s.blocked || m.slashArgLoadPending {
		return nil
	}
	text := normalizeClipboardText(msg.String())
	text = strings.ReplaceAll(text, "\n", " ")
	if len(s.fields) > 0 {
		m.editWizardField(tea.Key{Text: text})
		return nil
	}
	return m.wizardSearch(m.slashArgQuery + text)
}
