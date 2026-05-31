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
	var lifecycle SandboxLifecycleReport
	switch mode {
	case "":
	case "fix":
		repaired, err := s.services.Sandbox().Repair(ctx)
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		lifecycle = repaired.Lifecycle
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
	sandboxStatus.Lifecycle = lifecycle
	doctor := commandDoctorView(status, sandboxStatus, lifecycle)
	return appviewmodel.CommandExecutionView{
		Handled: true,
		Command: "doctor",
		Output:  formatCommandDoctor(doctor),
		Doctor:  &doctor,
	}, nil
}

func commandDoctorView(status appviewmodel.StatusView, sandboxStatus SandboxStatus, lifecycle SandboxLifecycleReport) appviewmodel.DoctorView {
	view := appviewmodel.DoctorView{Status: status}
	seenActions := map[string]bool{}
	addAction := func(action appviewmodel.DoctorAction) string {
		action.ID = strings.TrimSpace(action.ID)
		action.Command = strings.TrimSpace(action.Command)
		if action.ID == "" || action.Command == "" || seenActions[action.ID] {
			return action.ID
		}
		seenActions[action.ID] = true
		view.Actions = append(view.Actions, action)
		return action.ID
	}

	modelCheck := commandDoctorModelCheck(status.Model)
	switch {
	case status.Model.MissingAPIKey || !status.Model.Configured:
		actionID := addAction(appviewmodel.DoctorAction{
			ID:          "model.connect",
			Label:       "Connect model provider",
			Description: "Configure a provider, model, and credentials.",
			Kind:        "model",
			Command:     "/connect",
			Enabled:     true,
		})
		modelCheck.ActionIDs = appendDoctorActionID(modelCheck.ActionIDs, actionID)
	case status.Model.Current == nil:
		actionID := addAction(appviewmodel.DoctorAction{
			ID:          "model.select",
			Label:       "Select model",
			Description: "Choose one of the configured models.",
			Kind:        "model",
			Command:     "/model",
			Enabled:     true,
		})
		modelCheck.ActionIDs = appendDoctorActionID(modelCheck.ActionIDs, actionID)
	}
	view.Checks = append(view.Checks,
		modelCheck,
		commandDoctorStoreCheck(status.Runtime),
		commandDoctorSessionCheck(status),
	)

	sandboxCheck := commandDoctorSandboxCheck(status.Runtime, sandboxStatus)
	if commandDoctorSandboxNeedsRepair(sandboxStatus) {
		actionID := addAction(appviewmodel.DoctorAction{
			ID:          "sandbox.repair",
			Label:       "Repair sandbox",
			Description: "Run the configured sandbox repair flow.",
			Kind:        "sandbox",
			Command:     "/doctor fix",
			Enabled:     true,
		})
		sandboxCheck.ActionIDs = appendDoctorActionID(sandboxCheck.ActionIDs, actionID)
	}
	view.Checks = append(view.Checks, sandboxCheck)

	if resourceCheck, ok := commandDoctorResourceCheck(status.Resources); ok {
		view.Checks = append(view.Checks, resourceCheck)
	}
	if agentCheck, ok := commandDoctorAgentCheck(status.Agents); ok {
		view.Checks = append(view.Checks, agentCheck)
	}
	view.Lifecycle = commandDoctorLifecycleView(lifecycle)
	return view
}

func appendDoctorActionID(ids []string, id string) []string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ids
	}
	for _, existing := range ids {
		if existing == id {
			return ids
		}
	}
	return append(ids, id)
}

func commandDoctorModelCheck(modelStatus appviewmodel.ModelStatus) appviewmodel.DoctorCheck {
	switch {
	case modelStatus.MissingAPIKey:
		return appviewmodel.DoctorCheck{
			ID:       "model",
			Label:    "Model provider",
			Severity: "warning",
			Message:  "provider key missing - run /connect",
		}
	case modelStatus.Current != nil:
		name := firstNonEmpty(modelStatus.Current.Alias, modelStatus.Current.Model, modelStatus.Current.ID)
		detail := strings.Trim(strings.TrimSpace(modelStatus.Current.Provider)+"/"+strings.TrimSpace(modelStatus.Current.Model), "/")
		if detail != "" && !strings.EqualFold(name, detail) {
			name += " (" + detail + ")"
		}
		return appviewmodel.DoctorCheck{
			ID:       "model",
			Label:    "Model provider",
			Severity: "ok",
			Message:  "provider/model: " + name,
		}
	case modelStatus.Configured:
		return appviewmodel.DoctorCheck{
			ID:       "model",
			Label:    "Model provider",
			Severity: "warning",
			Message:  fmt.Sprintf("model: %d configured, none selected", modelStatus.Count),
		}
	default:
		return appviewmodel.DoctorCheck{
			ID:       "model",
			Label:    "Model provider",
			Severity: "warning",
			Message:  "model not configured - run /connect",
		}
	}
}

func commandDoctorStoreCheck(runtime appviewmodel.RuntimeStatus) appviewmodel.DoctorCheck {
	store := firstNonEmpty(strings.TrimSpace(runtime.StoreBackend), "store")
	if uri := strings.TrimSpace(runtime.StoreURI); uri != "" {
		store += " " + uri
	}
	return appviewmodel.DoctorCheck{
		ID:       "store",
		Label:    "Session store",
		Severity: "ok",
		Message:  "session store: " + store,
	}
}

func commandDoctorSessionCheck(status appviewmodel.StatusView) appviewmodel.DoctorCheck {
	sessionID := ""
	if status.Session != nil {
		sessionID = strings.TrimSpace(status.Session.Ref.SessionID)
	}
	if sessionID == "" {
		return appviewmodel.DoctorCheck{
			ID:       "session",
			Label:    "Session",
			Severity: "warning",
			Message:  "session unavailable",
		}
	}
	return appviewmodel.DoctorCheck{
		ID:       "session",
		Label:    "Session",
		Severity: "ok",
		Message:  "session: " + sessionID,
	}
}

func commandDoctorSandboxCheck(runtime appviewmodel.RuntimeStatus, status SandboxStatus) appviewmodel.DoctorCheck {
	backend := firstNonEmpty(status.ResolvedBackend, status.RequestedBackend, runtime.SandboxBackend)
	switch {
	case strings.TrimSpace(status.SetupError) != "":
		return appviewmodel.DoctorCheck{
			ID:       "sandbox",
			Label:    "Sandbox",
			Severity: "warning",
			Message:  "sandbox setup: " + strings.TrimSpace(status.SetupError),
		}
	case status.SetupRequired:
		return appviewmodel.DoctorCheck{
			ID:       "sandbox",
			Label:    "Sandbox",
			Severity: "warning",
			Message:  "sandbox setup required: " + firstNonEmpty(status.SetupMarkerReason, "setup required"),
		}
	case status.FallbackToHost:
		return appviewmodel.DoctorCheck{
			ID:       "sandbox",
			Label:    "Sandbox",
			Severity: "warning",
			Message:  "sandbox fallback: " + firstNonEmpty(status.FallbackReason, "using host execution"),
		}
	case strings.EqualFold(strings.TrimSpace(status.Route), "host"):
		return appviewmodel.DoctorCheck{
			ID:       "sandbox",
			Label:    "Sandbox",
			Severity: "warning",
			Message:  "sandbox: " + firstNonEmpty(backend, "host execution"),
		}
	case backend != "":
		return appviewmodel.DoctorCheck{
			ID:       "sandbox",
			Label:    "Sandbox",
			Severity: "ok",
			Message:  "sandbox: " + backend,
		}
	default:
		return appviewmodel.DoctorCheck{
			ID:       "sandbox",
			Label:    "Sandbox",
			Severity: "warning",
			Message:  "sandbox status unavailable",
		}
	}
}

func commandDoctorSandboxNeedsRepair(status SandboxStatus) bool {
	return strings.TrimSpace(status.SetupError) != "" ||
		status.SetupRequired ||
		status.FallbackToHost
}

func commandDoctorResourceCheck(status appviewmodel.ResourceStatus) (appviewmodel.DoctorCheck, bool) {
	if status.ErrorCount == 0 && status.WarningCount == 0 {
		return appviewmodel.DoctorCheck{}, false
	}
	severity := "warning"
	if status.ErrorCount > 0 {
		severity = "error"
	}
	return appviewmodel.DoctorCheck{
		ID:       "resources",
		Label:    "Resources",
		Severity: severity,
		Message:  fmt.Sprintf("resources: %d warnings, %d errors", status.WarningCount, status.ErrorCount),
		Detail:   commandDoctorResourceDetail(status.Diagnostics),
	}, true
}

func commandDoctorResourceDetail(diagnostics []appviewmodel.ResourceDiagnostic) string {
	var parts []string
	for _, diagnostic := range diagnostics {
		message := strings.TrimSpace(diagnostic.Message)
		if message == "" {
			message = strings.TrimSpace(diagnostic.Kind)
		}
		if message != "" {
			parts = append(parts, message)
		}
		if len(parts) == 3 {
			break
		}
	}
	return strings.Join(parts, "; ")
}

func commandDoctorAgentCheck(status appviewmodel.AgentStatus) (appviewmodel.DoctorCheck, bool) {
	if status.ExternalACPCount == 0 {
		return appviewmodel.DoctorCheck{}, false
	}
	return appviewmodel.DoctorCheck{
		ID:       "external_agents",
		Label:    "External ACP agents",
		Severity: "ok",
		Message:  fmt.Sprintf("external ACP agents: %d", status.ExternalACPCount),
	}, true
}

func commandDoctorLifecycleView(report SandboxLifecycleReport) *appviewmodel.DoctorLifecycleView {
	action := strings.TrimSpace(report.Action)
	if action == "" {
		return nil
	}
	return &appviewmodel.DoctorLifecycleView{
		Action:         action,
		Backend:        strings.TrimSpace(report.Backend),
		Supported:      report.Supported,
		Attempted:      report.Attempted,
		Noop:           report.Noop,
		FallbackAction: strings.TrimSpace(report.FallbackAction),
		Message:        strings.TrimSpace(report.Message),
		Error:          strings.TrimSpace(report.Error),
	}
}

func formatCommandDoctor(view appviewmodel.DoctorView) string {
	lines := []string{"doctor:"}
	for _, check := range view.Checks {
		if line := formatCommandDoctorCheck(check); line != "" {
			lines = append(lines, line)
		}
	}
	output := strings.Join(commandNonEmpty(lines), "\n")
	if view.Lifecycle == nil {
		return output
	}
	lifecycle := formatCommandDoctorLifecycle(*view.Lifecycle)
	if lifecycle == "" {
		return output
	}
	return lifecycle + "\n\n" + output
}

func formatCommandDoctorCheck(check appviewmodel.DoctorCheck) string {
	message := strings.TrimSpace(check.Message)
	if message == "" {
		message = firstNonEmpty(check.Label, check.ID)
	}
	if message == "" {
		return ""
	}
	return "  " + commandDoctorOutputSeverity(check.Severity) + " " + message
}

func commandDoctorOutputSeverity(severity string) string {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "ok", "success", "ready":
		return "ok"
	case "error", "failed":
		return "error"
	case "warning", "warn":
		return "warn"
	default:
		return "info"
	}
}

func formatCommandDoctorLifecycle(report appviewmodel.DoctorLifecycleView) string {
	action := strings.TrimSpace(report.Action)
	if action == "" {
		return ""
	}
	lines := []string{"sandbox " + action + " complete:"}
	lines = append(lines, "  message: "+firstNonEmpty(report.Message, "sandbox "+action+" complete"))
	if backend := strings.TrimSpace(report.Backend); backend != "" {
		lines = append(lines, "  backend: "+backend)
	}
	lines = append(lines, fmt.Sprintf("  supported: %t", report.Supported))
	lines = append(lines, fmt.Sprintf("  attempted: %t", report.Attempted))
	if report.Noop {
		lines = append(lines, "  noop: true")
	}
	if fallback := strings.TrimSpace(report.FallbackAction); fallback != "" {
		lines = append(lines, "  fallback_action: "+fallback)
	}
	if errText := strings.TrimSpace(report.Error); errText != "" {
		lines = append(lines, "  error: "+errText)
	}
	return strings.Join(lines, "\n")
}
