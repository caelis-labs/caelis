package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/session"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

func (s CommandService) executeController(ctx context.Context, ref session.Ref, args string) (appviewmodel.CommandExecutionView, error) {
	args = strings.TrimSpace(args)
	if args == "" {
		panel, err := s.services.Controllers().Panel(ctx, ControllerPanelRequest{SessionRef: ref})
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		return appviewmodel.CommandExecutionView{
			Handled:         true,
			Command:         "controller",
			Output:          formatCommandControllerPanel(panel),
			ControllerPanel: &panel,
		}, nil
	}
	sub, rest, _ := strings.Cut(args, " ")
	switch strings.ToLower(strings.TrimSpace(sub)) {
	case "set":
		optionID, value, ok := strings.Cut(strings.TrimSpace(rest), " ")
		if !ok || strings.TrimSpace(optionID) == "" || strings.TrimSpace(value) == "" {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /controller set <option-id> <value>")
		}
		if _, err := s.services.Controllers().SetConfigOption(ctx, ref, optionID, value); err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		panel, err := s.services.Controllers().Panel(ctx, ControllerPanelRequest{SessionRef: ref})
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		return appviewmodel.CommandExecutionView{
			Handled:         true,
			Command:         "controller",
			Output:          "controller option updated: " + normalizeControllerConfigOptionID(optionID) + "\n\n" + formatCommandControllerPanel(panel),
			ControllerPanel: &panel,
		}, nil
	default:
		return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /controller [set <option-id> <value>]")
	}
}

func formatCommandControllerPanel(panel appviewmodel.ControllerPanelView) string {
	lines := []string{"controller:"}
	if !panel.Active {
		lines = append(lines, "  inactive")
		return strings.Join(lines, "\n")
	}
	if summary := formatCommandControllerSummary(panel.Summary); summary != "" {
		lines = append(lines, "  summary: "+summary)
	}
	for _, section := range panel.Sections {
		if len(section.Fields) == 0 {
			continue
		}
		title := strings.ToLower(strings.TrimSpace(section.Title))
		if title == "" {
			title = strings.TrimSpace(section.ID)
		}
		if title != "" {
			lines = append(lines, "  "+title+":")
		}
		for _, field := range section.Fields {
			if line := formatCommandControllerField(field); line != "" {
				lines = append(lines, "    "+line)
			}
		}
	}
	for _, diagnostic := range panel.Diagnostics {
		if strings.EqualFold(strings.TrimSpace(diagnostic.Severity), "info") {
			continue
		}
		if line := formatCommandControllerDiagnostic(diagnostic); line != "" {
			lines = append(lines, "  "+line)
		}
	}
	return strings.Join(lines, "\n")
}

func formatCommandControllerSummary(summary appviewmodel.ControllerPanelSummary) string {
	var parts []string
	for _, item := range []struct {
		label string
		value string
	}{
		{label: "agent", value: summary.Agent},
		{label: "remote", value: summary.RemoteSessionID},
		{label: "model", value: summary.Model},
		{label: "reasoning", value: summary.ReasoningEffort},
		{label: "mode", value: summary.Mode},
		{label: "phase", value: summary.Phase},
	} {
		if value := strings.TrimSpace(item.value); value != "" {
			parts = append(parts, item.label+"="+value)
		}
	}
	if summary.Running {
		parts = append(parts, "running")
	}
	if summary.Recovering {
		parts = append(parts, "recovering")
	}
	return strings.Join(parts, "  ")
}

func formatCommandControllerField(field appviewmodel.ControllerPanelField) string {
	name := firstNonEmpty(strings.TrimSpace(field.Label), strings.TrimSpace(field.ID))
	if name == "" {
		return ""
	}
	value := strings.TrimSpace(field.Value)
	if value == "" {
		value = "-"
	}
	if field.Editable && len(field.Options) > 0 {
		value += " (" + fmt.Sprintf("%d options", len(field.Options)) + ")"
	}
	return name + ": " + value
}

func formatCommandControllerDiagnostic(diagnostic appviewmodel.ControllerPanelDiagnostic) string {
	parts := []string{strings.TrimSpace(diagnostic.Severity), strings.TrimSpace(diagnostic.Kind)}
	if message := strings.TrimSpace(diagnostic.Message); message != "" {
		parts = append(parts, message)
	}
	return strings.Join(commandNonEmpty(parts), "  ")
}
