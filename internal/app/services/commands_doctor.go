package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/session"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

func (s CommandService) executeDoctor(ctx context.Context, ref session.Ref, args string) (appviewmodel.CommandExecutionView, error) {
	mode := strings.ToLower(strings.TrimSpace(args))
	repaired := false
	switch mode {
	case "":
	case "fix":
		if _, err := s.services.Sandbox().Repair(ctx); err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		repaired = true
	default:
		return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /doctor [fix]")
	}
	status, err := s.services.Status().View(ctx, StatusRequest{SessionRef: ref})
	if err != nil {
		return appviewmodel.CommandExecutionView{}, err
	}
	sandboxStatus, err := s.services.Sandbox().Status(ctx)
	if err != nil {
		return appviewmodel.CommandExecutionView{}, err
	}
	output := formatCommandDoctor(status, sandboxStatus)
	if repaired {
		output = "sandbox repair complete\n\n" + output
	}
	return appviewmodel.CommandExecutionView{
		Handled: true,
		Command: "doctor",
		Output:  output,
	}, nil
}

func formatCommandDoctor(status appviewmodel.StatusView, sandboxStatus SandboxStatus) string {
	lines := []string{"doctor:"}
	lines = append(lines, formatCommandDoctorModel(status.Model))
	lines = append(lines, formatCommandDoctorStore(status.Runtime))
	if status.Session != nil {
		if sessionID := strings.TrimSpace(status.Session.Ref.SessionID); sessionID != "" {
			lines = append(lines, "  ok session: "+sessionID)
		}
	}
	lines = append(lines, formatCommandDoctorSandbox(status.Runtime, sandboxStatus))
	if status.Resources.ErrorCount > 0 || status.Resources.WarningCount > 0 {
		lines = append(lines, fmt.Sprintf("  warn resources: %d warnings, %d errors", status.Resources.WarningCount, status.Resources.ErrorCount))
	}
	if status.Agents.ExternalACPCount > 0 {
		lines = append(lines, fmt.Sprintf("  ok external ACP agents: %d", status.Agents.ExternalACPCount))
	}
	return strings.Join(commandNonEmpty(lines), "\n")
}

func formatCommandDoctorModel(modelStatus appviewmodel.ModelStatus) string {
	if modelStatus.MissingAPIKey {
		return "  warn provider key missing - run /connect"
	}
	if modelStatus.Current != nil {
		name := firstNonEmpty(modelStatus.Current.Alias, modelStatus.Current.Model, modelStatus.Current.ID)
		detail := strings.Trim(strings.TrimSpace(modelStatus.Current.Provider)+"/"+strings.TrimSpace(modelStatus.Current.Model), "/")
		if detail != "" && !strings.EqualFold(name, detail) {
			name += " (" + detail + ")"
		}
		return "  ok provider/model: " + name
	}
	if modelStatus.Configured {
		return fmt.Sprintf("  warn model: %d configured, none selected", modelStatus.Count)
	}
	return "  warn model not configured - run /connect"
}

func formatCommandDoctorStore(runtime appviewmodel.RuntimeStatus) string {
	store := firstNonEmpty(strings.TrimSpace(runtime.StoreBackend), "store")
	if uri := strings.TrimSpace(runtime.StoreURI); uri != "" {
		store += " " + uri
	}
	return "  ok session store: " + store
}

func formatCommandDoctorSandbox(runtime appviewmodel.RuntimeStatus, status SandboxStatus) string {
	backend := firstNonEmpty(status.ResolvedBackend, status.RequestedBackend, runtime.SandboxBackend)
	switch {
	case status.SetupError != "":
		return "  warn sandbox setup: " + status.SetupError
	case status.SetupRequired:
		return "  warn sandbox setup required: " + firstNonEmpty(status.SetupMarkerReason, "setup required")
	case status.FallbackToHost:
		return "  warn sandbox fallback: " + firstNonEmpty(status.FallbackReason, "using host execution")
	case strings.EqualFold(strings.TrimSpace(status.Route), "host"):
		return "  warn sandbox: " + firstNonEmpty(backend, "host execution")
	case backend != "":
		return "  ok sandbox: " + backend
	default:
		return "  warn sandbox status unavailable"
	}
}
