package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

func formatUpdateAnnouncement(latestVersion string) welcomeAnnouncement {
	latest := strings.TrimSpace(latestVersion)
	if latest == "" {
		return welcomeAnnouncement{}
	}
	emphasis := welcomeVersionLabel(latest) + " available"
	return newWelcomeAnnouncementWithEmphasis(
		emphasis+", press ctrl+u to update",
		emphasis,
	)
}

func (m *Model) handleUpdateCheckResult(msg UpdateCheckResultMsg) (tea.Model, tea.Cmd) {
	if m == nil || m.updateOffered || m.turnRunning() || !msg.Eligible {
		return m, nil
	}
	announcement := formatUpdateAnnouncement(msg.LatestVersion)
	if announcement.plainText() == "" {
		return m, nil
	}
	m.updateOffered = true
	if welcomePanelSupportsAnnouncement(m.blockRenderContext(maxInt(1, m.viewport.Width())), announcement) && m.setWelcomeAnnouncement(announcement) {
		return m, nil
	}
	cmd := m.showHint(announcement.plainText(), hintOptions{priority: HintPriorityNormal})
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
	return m.setWelcomeAnnouncement(newWelcomeAnnouncement(notice))
}

func (m *Model) setWelcomeAnnouncement(announcement welcomeAnnouncement) bool {
	if m == nil || m.doc == nil {
		return false
	}
	if strings.TrimSpace(announcement.plainText()) == "" {
		announcement = newWelcomeAnnouncement("")
	}
	found := false
	changed := false
	for _, block := range m.doc.FindByKind(BlockWelcome) {
		welcome, ok := block.(*WelcomeBlock)
		if !ok {
			continue
		}
		found = true
		if welcome.announcement == announcement {
			continue
		}
		welcome.announcement = announcement
		m.markViewportBlockDirty(welcome.BlockID())
		changed = true
	}
	if changed {
		m.syncViewportContent()
	}
	return found
}
