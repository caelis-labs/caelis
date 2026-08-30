package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

func formatUpdateNotice(latestVersion string) string {
	latest := strings.TrimSpace(latestVersion)
	if latest == "" {
		return ""
	}
	return latest + " · Ctrl+U"
}

func formatUpdateHint(latestVersion string) string {
	latest := strings.TrimSpace(latestVersion)
	if latest == "" {
		return ""
	}
	return "Update " + latest + " available. Press Ctrl+U to update."
}

func (m *Model) handleUpdateCheckResult(msg UpdateCheckResultMsg) (tea.Model, tea.Cmd) {
	if m == nil || m.updateOffered || m.turnRunning() || !msg.Eligible {
		return m, nil
	}
	text := formatUpdateNotice(msg.LatestVersion)
	if text == "" {
		return m, nil
	}
	m.updateOffered = true
	if welcomePanelSupportsNotice(m.blockRenderContext(maxInt(1, m.viewport.Width()))) && m.setWelcomeNotice(text) {
		return m, nil
	}
	cmd := m.showHint(formatUpdateHint(msg.LatestVersion), hintOptions{priority: HintPriorityNormal})
	m.updateHintID = m.nextHintID
	return m, cmd
}

func (m *Model) handleUpdateKey() (tea.Model, tea.Cmd) {
	if m == nil || !m.updateOffered || m.cfg.OnUpdateRequested == nil {
		return m, nil
	}
	m.revokeUpdateOffer()
	m.cfg.OnUpdateRequested()
	m.quit = true
	return m, tea.Quit
}

func (m *Model) revokeUpdateOffer() {
	if m == nil {
		return
	}
	m.updateOffered = false
	m.setWelcomeNotice(m.cfg.WelcomeNotice)
	m.removeHintByID(m.updateHintID)
	m.updateHintID = 0
}

func (m *Model) setWelcomeNotice(notice string) bool {
	if m == nil || m.doc == nil {
		return false
	}
	notice = normalizeWelcomeNotice(notice)
	found := false
	changed := false
	for _, block := range m.doc.FindByKind(BlockWelcome) {
		welcome, ok := block.(*WelcomeBlock)
		if !ok {
			continue
		}
		found = true
		if welcome.Notice == notice {
			continue
		}
		welcome.Notice = notice
		m.markViewportBlockDirty(welcome.BlockID())
		changed = true
	}
	if changed {
		m.syncViewportContent()
	}
	return found
}
