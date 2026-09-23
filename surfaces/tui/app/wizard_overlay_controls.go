package tuiapp

import (
	"strings"

	"charm.land/lipgloss/v2"
)

func (m *Model) wizardCloseEnabled() bool {
	s := m.wizardOverlay
	return !s.pending
}

func (m *Model) wizardActionEnabled() bool {
	s := m.wizardOverlay
	if m.isWizardAuthChoice() {
		return len(m.visiblePromptChoices()) > 0
	}
	return !m.wizardBusy() &&
		(s.blocked || s.err != "" || m.isACPManualSetup() || len(s.fields) > 0 || m.wizardRowCount() > 0)
}

func (m *Model) wizardButtonStyle(target string, enabled, focused bool) lipgloss.Style {
	if !enabled {
		return m.theme.HelpHintTextStyle()
	}
	s := m.wizardOverlay
	if s.hovered == target || s.pressed == target || focused || s.footerFocus == target {
		return m.theme.CommandActiveStyle().Padding(0)
	}
	return m.theme.CommandStyle().Padding(0).Bold(true)
}

type wizardFooterLayout struct {
	lines                                               []string
	backWidth, actionWidth, sendWidth, actionRightInset int
}

func (m *Model) renderWizardFooter(width int, hint string) wizardFooterLayout {
	s := m.wizardOverlay
	back := "[esc back]"
	if len(s.parents) == 0 || s.blocked {
		back = "[esc close]"
	}
	hint = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(hint, "esc back"), "esc close"))
	action := "[" + m.wizardAction() + " ↵]"
	if m.wizardBusy() {
		action = ""
	}
	if displayColumns(back)+displayColumns(action)+2 > width {
		back = "[esc]"
		action = truncateTailDisplay(action, max(0, width-displayColumns(back)-2))
	}
	var layout wizardFooterLayout
	send := ""
	if m.canSendACPInstallPrompt() {
		send = m.wizardButtonStyle("send", true, false).Render("[Send to agent]")
		layout.sendWidth = displayColumns(send)
		if displayColumns(back)+displayColumns(action)+displayColumns(send)+4 > width {
			layout.lines = append(layout.lines, strings.Repeat(" ", max(0, width-displayColumns(send)))+send)
			send = ""
		} else {
			layout.actionRightInset = displayColumns(send) + 2
		}
	}
	leftWidth := max(0, width-displayColumns(action)-layout.actionRightInset-2)
	left := m.wizardButtonStyle("back", m.wizardCloseEnabled(), false).Render(back)
	if hint != "" && displayColumns(back)+2+displayColumns(hint) <= leftWidth {
		left += "  " + m.theme.HelpHintTextStyle().Render(hint)
	}
	left = padRightDisplay(left, leftWidth)
	focused := len(s.fields) > 0 && s.field == len(s.fields)
	line := left + "  " + m.wizardButtonStyle("action", m.wizardActionEnabled(), focused).Render(action)
	if send != "" {
		line += "  " + send
	}
	layout.lines = append(layout.lines, line)
	if m.wizardCloseEnabled() {
		layout.backWidth = displayColumns(back)
	}
	if m.wizardActionEnabled() {
		layout.actionWidth = displayColumns(action)
	}
	return layout
}
