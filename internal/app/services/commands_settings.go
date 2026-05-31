package services

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/session"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

func (s CommandService) executeSettings(ctx context.Context, ref session.Ref, args string) (appviewmodel.CommandExecutionView, error) {
	sub, rest, hasSub := splitCommandArg(args)
	switch strings.ToLower(strings.TrimSpace(sub)) {
	case "":
		if hasSub {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /settings [set <field-id> <value>|run <action-id> [confirm]]")
		}
		panel, err := s.services.Settings().Panel(ctx, SettingsPanelRequest{SessionRef: ref})
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		return appviewmodel.CommandExecutionView{
			Handled:       true,
			Command:       "settings",
			Output:        formatCommandSettingsPanel(panel),
			SettingsPanel: &panel,
		}, nil
	case "run", "action":
		actionID, actionArgs, ok := splitCommandArg(rest)
		if !ok || strings.TrimSpace(actionID) == "" {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /settings run <action-id> [confirm]")
		}
		before, err := s.services.Settings().Panel(ctx, SettingsPanelRequest{SessionRef: ref})
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		action, ok := findSettingsRunAction(before.Actions, actionID)
		if !ok {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: settings action %q is unavailable", strings.TrimSpace(actionID))
		}
		if !action.Enabled {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: settings action %q is disabled", action.ID)
		}
		if (action.Destructive || action.RequiresConfirmation) && !settingsActionConfirmed(actionArgs) {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: settings action %q requires confirmation", action.ID)
		}
		panel, err := s.services.Settings().RunPanelAction(ctx, SettingsPanelActionRequest{SessionRef: ref, ActionID: action.ID})
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		return appviewmodel.CommandExecutionView{
			Handled:       true,
			Command:       "settings",
			Output:        "settings action completed: " + action.ID + "\n\n" + formatCommandSettingsPanel(panel),
			SettingsPanel: &panel,
		}, nil
	case "set", "field":
		fieldID, value, ok := splitCommandArg(rest)
		if !ok || strings.TrimSpace(fieldID) == "" {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /settings set <field-id> <value>")
		}
		panel, err := s.services.Settings().SetPanelField(ctx, SettingsPanelFieldUpdateRequest{
			SessionRef: ref,
			FieldID:    fieldID,
			Value:      value,
		})
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		return appviewmodel.CommandExecutionView{
			Handled:       true,
			Command:       "settings",
			Output:        "settings field updated: " + strings.ToLower(strings.TrimSpace(fieldID)) + "\n\n" + formatCommandSettingsPanel(panel),
			SettingsPanel: &panel,
		}, nil
	default:
		return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /settings [set <field-id> <value>|run <action-id> [confirm]]")
	}
}

func formatCommandSettingsPanel(panel appviewmodel.SettingsPanelView) string {
	lines := []string{"settings:"}
	lines = append(lines, "  configured: "+formatCommandBool(panel.Configured))
	if panel.Runtime.AppName != "" || panel.Runtime.UserID != "" || panel.Runtime.WorkspaceCWD != "" {
		lines = append(lines, "  runtime:")
		if panel.Runtime.AppName != "" {
			lines = append(lines, "    app: "+panel.Runtime.AppName)
		}
		if panel.Runtime.UserID != "" {
			lines = append(lines, "    user: "+panel.Runtime.UserID)
		}
		if panel.Runtime.WorkspaceCWD != "" {
			lines = append(lines, "    workspace: "+panel.Runtime.WorkspaceCWD)
		}
		if panel.Runtime.StoreBackend != "" || panel.Runtime.StoreURI != "" {
			store := firstNonEmpty(panel.Runtime.StoreBackend, "store")
			if panel.Runtime.StoreURI != "" {
				store += " " + panel.Runtime.StoreURI
			}
			lines = append(lines, "    store: "+store)
		}
	}
	lines = append(lines, "  model: "+settingsPanelModelSummary(panel.Model))
	if panel.Agents.Count > 0 || panel.Agents.ExternalACPCount > 0 {
		lines = append(lines, fmt.Sprintf("  agents: %d registered, %d external ACP", panel.Agents.Count, panel.Agents.ExternalACPCount))
	}
	lines = append(lines, "  sandbox: "+settingsPanelSandboxSummary(panel.Sandbox.Status))
	if len(panel.Diagnostics) > 0 {
		lines = append(lines, "  diagnostics:")
		for _, diagnostic := range panel.Diagnostics {
			lines = append(lines, "    "+settingsPanelDiagnosticLine(diagnostic))
		}
	}
	if len(panel.Actions) > 0 {
		lines = append(lines, "  actions:")
		for _, action := range panel.Actions {
			lines = append(lines, "    "+settingsPanelActionLine(action))
		}
	}
	if len(panel.Sections) > 0 {
		lines = append(lines, "  sections:")
		for _, section := range panel.Sections {
			lines = append(lines, settingsPanelSectionLines(section)...)
		}
	}
	return strings.Join(commandNonEmpty(lines), "\n")
}

func settingsPanelModelSummary(status appviewmodel.ModelStatus) string {
	if status.Current != nil {
		return firstNonEmpty(status.Current.Alias, status.Current.Model, status.Current.ID)
	}
	if status.Configured {
		return fmt.Sprintf("%d configured", status.Count)
	}
	return "not configured"
}

func settingsPanelSandboxSummary(status appviewmodel.SandboxPanelStatus) string {
	backend := firstNonEmpty(status.ResolvedBackend, status.RequestedBackend, "unknown")
	parts := []string{backend}
	if route := strings.TrimSpace(status.Route); route != "" && !strings.EqualFold(route, backend) {
		parts = append(parts, "route="+route)
	}
	if status.FallbackToHost {
		parts = append(parts, "fallback=host")
	}
	if status.SetupRequired {
		parts = append(parts, "setup=required")
	}
	if status.SetupError != "" {
		parts = append(parts, "error="+status.SetupError)
	}
	return strings.Join(parts, " ")
}

func settingsPanelDiagnosticLine(diagnostic appviewmodel.SettingsPanelDiagnostic) string {
	label := strings.Trim(strings.TrimSpace(diagnostic.Source)+"/"+strings.TrimSpace(diagnostic.Kind), "/")
	if label == "" {
		label = firstNonEmpty(diagnostic.ID, "diagnostic")
	}
	severity := firstNonEmpty(diagnostic.Severity, "info")
	line := "[" + severity + "] " + label
	if message := strings.TrimSpace(diagnostic.Message); message != "" {
		line += ": " + message
	}
	if len(diagnostic.ActionIDs) > 0 {
		line += " (actions: " + strings.Join(diagnostic.ActionIDs, ", ") + ")"
	}
	return line
}

func settingsPanelActionLine(action appviewmodel.SettingsPanelAction) string {
	line := action.ID
	if strings.TrimSpace(action.Label) != "" && !strings.EqualFold(action.Label, action.ID) {
		line += " - " + strings.TrimSpace(action.Label)
	}
	state := "disabled"
	if action.Enabled {
		state = "enabled"
	}
	traits := []string{state}
	if action.Destructive {
		traits = append(traits, "destructive")
	}
	if action.RequiresConfirmation {
		traits = append(traits, "confirm")
	}
	if command := strings.TrimSpace(action.Command); command != "" {
		traits = append(traits, "command="+command)
	}
	return line + " (" + strings.Join(traits, ", ") + ")"
}

func settingsPanelSectionLines(section appviewmodel.SettingsPanelSection) []string {
	title := firstNonEmpty(section.Title, section.ID, "section")
	lines := []string{"    " + title}
	for _, field := range section.Fields {
		lines = append(lines, "      "+settingsPanelFieldLine(field))
	}
	if len(section.Actions) > 0 {
		actionIDs := make([]string, 0, len(section.Actions))
		for _, action := range section.Actions {
			if strings.TrimSpace(action.ID) != "" {
				actionIDs = append(actionIDs, action.ID)
			}
		}
		if len(actionIDs) > 0 {
			lines = append(lines, "      actions: "+strings.Join(actionIDs, ", "))
		}
	}
	return lines
}

func settingsPanelFieldLine(field appviewmodel.SettingsPanelField) string {
	id := firstNonEmpty(field.ID, field.Label, "field")
	value := settingsPanelFieldValue(field)
	traits := []string{firstNonEmpty(field.Kind, "text")}
	if field.Editable {
		traits = append(traits, "editable")
	} else {
		traits = append(traits, "readonly")
	}
	if field.ConfigID != "" {
		traits = append(traits, "config="+field.ConfigID)
	}
	if len(field.Options) > 0 {
		traits = append(traits, "options="+settingsPanelFieldOptions(field.Options))
	}
	if detail := strings.TrimSpace(field.Detail); detail != "" {
		traits = append(traits, detail)
	}
	return id + ": " + value + " (" + strings.Join(traits, ", ") + ")"
}

func settingsPanelFieldValue(field appviewmodel.SettingsPanelField) string {
	if field.Sensitive {
		return "[redacted]"
	}
	if value := strings.TrimSpace(field.Value); value != "" {
		return value
	}
	return "-"
}

func settingsPanelFieldOptions(options []appviewmodel.SettingsPanelFieldOption) string {
	values := make([]string, 0, len(options))
	for _, option := range options {
		value := strings.TrimSpace(option.Value)
		if value == "" {
			value = "default"
		}
		values = append(values, value)
	}
	return strings.Join(values, "|")
}

func findSettingsAction(actions []appviewmodel.SettingsPanelAction, id string) (appviewmodel.SettingsPanelAction, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return appviewmodel.SettingsPanelAction{}, false
	}
	for _, action := range actions {
		if strings.EqualFold(strings.TrimSpace(action.ID), id) {
			return action, true
		}
	}
	return appviewmodel.SettingsPanelAction{}, false
}

func findSettingsRunAction(actions []appviewmodel.SettingsPanelAction, id string) (appviewmodel.SettingsPanelAction, bool) {
	action, ok := findSettingsAction(actions, id)
	if !ok {
		return appviewmodel.SettingsPanelAction{}, false
	}
	command := strings.TrimSpace(action.Command)
	if command == "" {
		command = "/settings run " + strings.TrimSpace(action.ID)
	}
	if !strings.HasPrefix(strings.ToLower(command), "/settings run ") {
		return appviewmodel.SettingsPanelAction{}, false
	}
	return action, true
}

func settingsActionConfirmed(args string) bool {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(args)))
	return slices.Contains(fields, "confirm") || slices.Contains(fields, "--confirm")
}

func formatCommandBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
