package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

func (m *Model) canSendACPInstallPrompt() bool {
	if !m.isACPManualSetup() || m.wizardOverlay == nil || m.wizardOverlay.runtimeSetup == nil ||
		strings.TrimSpace(m.wizardOverlay.runtimeSetup.InstallPrompt) == "" {
		return false
	}
	// Wait for the Host's refreshed model selection; a startup alias alone
	// does not establish that the current Session has a configured model.
	model := strings.TrimSpace(m.statusView.Model)
	return model != "" && !strings.HasPrefix(strings.ToLower(model), "not configured") && !m.statusView.MissingAPIKey &&
		!m.turnRunning() && !m.wizardBusy() && m.wizardOverlay.err == "" && m.cfg.ExecuteLine != nil &&
		!m.sessionSwitchPending && !m.sessionObservationRecovering && !m.sessionHistoryFailed
}

func (m *Model) sendACPInstallPrompt() tea.Cmd {
	if !m.canSendACPInstallPrompt() {
		return nil
	}
	prompt := m.wizardOverlay.runtimeSetup.InstallPrompt
	m.clearWizard()
	// Preserve the draft and attachments; dispatch remains owned by normal
	// Session submission, with eligibility checked again on activation.
	_, cmd := m.submitLineWithDisplayAndAttachmentsOptions(prompt, prompt, nil, submitLineOptions{
		recordHistory: true, preserveComposer: true, forceIdle: true,
	})
	return cmd
}
