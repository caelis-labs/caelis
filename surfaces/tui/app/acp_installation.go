package tuiapp

import (
	"encoding/json"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/agents"
)

func confirmACPSetupStep(key, value string, _ *SlashArgCandidate, state map[string]string) {
	if key == "acp_launcher" {
		delete(state, "_acp_install")
	}
}

func confirmedACPInstallation(state map[string]string) *agents.RuntimeInstallation {
	var install agents.RuntimeInstallation
	if json.Unmarshal([]byte(state["_acp_install"]), &install) != nil {
		return nil
	}
	return &install
}

func (m *Model) initACPInstallationForm() {
	s := m.wizardOverlay
	directory := s.runtimeSetup.Directory
	if len(s.fields) > 0 && s.fields[0].key == "acp_install_dir" {
		directory = s.fields[0].value
	}
	s.fields = []wizardField{{key: "acp_install_dir", label: "Directory", value: directory}}
	s.field, s.cursor = 0, len([]rune(directory))
}

func (m *Model) submitACPInstallation() tea.Cmd {
	s := m.wizardOverlay
	if s.runtimeSetup == nil || s.runtimeSetup.ArchiveURL == "" {
		s.err = "Check the official download again."
		return nil
	}
	directory := strings.TrimSpace(s.fields[0].value)
	if directory == "" {
		s.err = "Enter an installation directory."
		return nil
	}
	m.rememberWizardStep()
	s.formAdvancing = true
	defer func() { s.formAdvancing = false }()
	install := agents.RuntimeInstallation{Directory: directory, ArchiveURL: s.runtimeSetup.ArchiveURL, SHA256: s.runtimeSetup.SHA256}
	encoded, _ := json.Marshal(install)
	m.wizard.state["_acp_install"] = string(encoded)
	m.wizard.state["acp_launcher"] = "installed"
	return m.advanceWizardStep("confirmed")
}

func (m *Model) isACPManualSetup() bool {
	return m.wizard != nil && m.wizardStepKey() == "acp_install" && m.wizard.state["acp_launcher"] == "manual"
}
