package tuiapp

// Authentication retains its prompt response channel and keyboard handling.
// The connection wizard supplies the same visible controls as its other pages.
func (m *Model) isWizardAuthChoice() bool {
	p := m.activePrompt
	return m.wizardOverlay != nil && m.wizard != nil && m.wizard.def.Command == "connect" &&
		p != nil && p.response != nil && p.response == m.slashArgLoadAuthPrompt && len(p.choices) > 0
}

func (m *Model) wizardBusy() bool {
	return m.wizardOverlay.pending || !m.isWizardAuthChoice() && (m.slashArgLoadPending || m.slashArgRequestPending)
}
