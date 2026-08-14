package tuiapp

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

func formatUpdateHint(latestVersion string) string {
	latest := strings.TrimSpace(latestVersion)
	if latest == "" {
		return ""
	}
	return fmt.Sprintf("Update %s available. Press Ctrl+U to update.", latest)
}

func (m *Model) handleUpdateCheckResult(msg UpdateCheckResultMsg) (tea.Model, tea.Cmd) {
	if m == nil || m.updateOffered || m.turnRunning() || !msg.Eligible {
		return m, nil
	}
	text := formatUpdateHint(msg.LatestVersion)
	if text == "" {
		return m, nil
	}
	m.updateOffered = true
	cmd := m.showHint(text, hintOptions{
		priority: HintPriorityNormal,
	})
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
	if m.updateHintID != 0 {
		m.removeHintByID(m.updateHintID)
		m.updateHintID = 0
	}
}
