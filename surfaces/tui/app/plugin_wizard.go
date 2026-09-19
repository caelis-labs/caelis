package tuiapp

import "strings"

func pluginWizard() WizardDef {
	return WizardDef{
		Command: "plugin", DisplayLine: "/plugin",
		Steps: []WizardStepDef{{Key: "action", RequireCandidate: true, CompletionCommand: func(map[string]string) string { return "plugin" }}},
		Branch: func(key, value string, _ *SlashArgCandidate, state map[string]string) *WizardDef {
			next := pluginWizardTarget(state)
			return &next
		},
	}
}

func pluginWizardTarget(state map[string]string) WizardDef {
	d := WizardDef{Command: "plugin", DisplayLine: "/plugin", BuildExecLine: func(s map[string]string) string {
		return strings.TrimSpace(joinNonEmpty([]string{"/plugin", s["action"], s["marketplace_action"], s["target"]}, " "))
	}}
	action := state["action"]
	if action == "manage" {
		return d
	}
	if action == "marketplace" && state["marketplace_action"] == "" {
		d.Steps = []WizardStepDef{{Key: "marketplace_action", RequireCandidate: true, CompletionCommand: func(map[string]string) string { return "plugin marketplace" }}}
		d.Branch = func(_ string, _ string, _ *SlashArgCandidate, s map[string]string) *WizardDef {
			next := pluginWizardTarget(s)
			return &next
		}
		return d
	}
	if state["marketplace_action"] == "list" {
		return d
	}
	freeform := action == "install" || state["marketplace_action"] == "add"
	d.Steps = []WizardStepDef{{Key: "target", NoCompletion: freeform, RequireCandidate: !freeform, CompletionCommand: func(s map[string]string) string {
		return joinNonEmpty([]string{"plugin", s["action"], s["marketplace_action"]}, " ")
	}}}
	return d
}

func pluginActionLabel(value, fallback string) string {
	switch value {
	case "install":
		return "Install plugin"
	case "marketplace":
		return "Marketplaces"
	case "manage":
		return "Enable / disable"
	case "rm":
		return "Remove"
	case "add":
		return "Add marketplace"
	case "list":
		return "List marketplaces"
	case "update":
		return "Refresh marketplace"
	}
	return fallback
}

func (m *Model) pluginWizardTitle() string {
	s := m.wizard.state
	if m.wizardStepKey() == "action" {
		return "Plugins"
	}
	if m.wizardStepKey() == "marketplace_action" {
		return "Marketplaces"
	}
	return pluginActionLabel(firstNonEmpty(s["marketplace_action"], s["action"]), "Plugins")
}

func (m *Model) pluginWizardAction() string {
	if m.wizardStepKey() != "target" {
		return "Continue"
	}
	s := m.wizard.state
	switch firstNonEmpty(s["marketplace_action"], s["action"]) {
	case "install":
		return "Install"
	case "rm":
		return "Remove"
	case "add":
		return "Add"
	case "update":
		return "Refresh"
	}
	return "Apply"
}
