package services

import (
	"context"
	"os"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/session"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

type HomeRequest struct {
	SessionRef session.Ref `json:"session_ref,omitempty"`
	Version    string      `json:"version,omitempty"`
}

func (s ViewService) Home(ctx context.Context, req HomeRequest) (appviewmodel.HomeView, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	status, err := s.services.Status().View(ctx, StatusRequest{SessionRef: req.SessionRef})
	if err != nil {
		return appviewmodel.HomeView{}, err
	}
	sandboxStatus, err := s.services.Sandbox().Status(ctx)
	if err != nil {
		return appviewmodel.HomeView{}, err
	}
	doctor := commandDoctorView(status, sandboxStatus, SandboxLifecycleReport{})
	catalog, err := s.services.Commands().Available(ctx, CommandCatalogRequest{})
	if err != nil {
		return appviewmodel.HomeView{}, err
	}
	view := appviewmodel.HomeView{
		AppName:        firstNonEmpty(strings.TrimSpace(status.Runtime.AppName), "caelis"),
		Version:        strings.TrimSpace(req.Version),
		VersionLabel:   homeVersionLabel(req.Version),
		Workspace:      homeWorkspace(status),
		WorkspaceLabel: homeWorkspaceLabel(homeWorkspace(status)),
		ModelAlias:     homeModelAlias(status.Model),
		Mode:           firstNonEmpty(status.Mode.Current.Name, status.Mode.Current.ID),
		Status:         status,
		Diagnostics:    homeDiagnosticsFromDoctor(doctor),
		Actions:        homeActions(doctor),
		Commands:       append([]appviewmodel.CommandView(nil), catalog.Commands...),
	}
	return view, nil
}

func homeVersionLabel(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		version = "unknown"
	}
	if strings.HasPrefix(strings.ToLower(version), "v") {
		return version
	}
	return "v" + version
}

func homeWorkspace(status appviewmodel.StatusView) string {
	if status.Session != nil {
		if workspace := firstNonEmpty(status.Session.Workspace.CWD, status.Session.Workspace.Key); workspace != "" {
			return workspace
		}
	}
	return firstNonEmpty(status.Runtime.WorkspaceCWD, status.Runtime.WorkspaceKey, ".")
}

func homeWorkspaceLabel(workspace string) string {
	workspace = firstNonEmpty(strings.TrimSpace(workspace), ".")
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		home = strings.TrimRight(strings.TrimSpace(home), string(os.PathSeparator))
		switch {
		case workspace == home:
			return "~"
		case strings.HasPrefix(workspace, home+string(os.PathSeparator)):
			return "~" + strings.TrimPrefix(workspace, home)
		}
	}
	return workspace
}

func homeModelAlias(status appviewmodel.ModelStatus) string {
	if status.Current != nil {
		return firstNonEmpty(status.Current.Alias, status.Current.ID, status.Current.Model)
	}
	for _, choice := range status.Choices {
		if choice.Default {
			return firstNonEmpty(choice.Alias, choice.ID, choice.Model)
		}
	}
	if len(status.Choices) > 0 {
		choice := status.Choices[0]
		return firstNonEmpty(choice.Alias, choice.ID, choice.Model)
	}
	if status.Configured {
		return "model configured"
	}
	return "not configured (/connect)"
}

func homeDiagnosticsFromDoctor(doctor appviewmodel.DoctorView) []appviewmodel.HomeDiagnostic {
	out := make([]appviewmodel.HomeDiagnostic, 0, len(doctor.Checks))
	for _, check := range doctor.Checks {
		if isNormalHomeSessionDiagnostic(check) {
			continue
		}
		severity := strings.ToLower(strings.TrimSpace(check.Severity))
		switch severity {
		case "error", "failed", "warning", "warn":
		default:
			continue
		}
		out = append(out, appviewmodel.HomeDiagnostic{
			Severity:  commandDoctorOutputSeverity(severity),
			Source:    "doctor",
			Kind:      strings.TrimSpace(check.ID),
			Message:   strings.TrimSpace(firstNonEmpty(check.Message, check.Detail, check.Label)),
			ActionIDs: append([]string(nil), check.ActionIDs...),
		})
	}
	return out
}

func isNormalHomeSessionDiagnostic(check appviewmodel.DoctorCheck) bool {
	return strings.EqualFold(strings.TrimSpace(check.ID), "session") &&
		strings.EqualFold(strings.TrimSpace(check.Message), "session unavailable")
}

func homeActions(doctor appviewmodel.DoctorView) []appviewmodel.HomeAction {
	actions := []appviewmodel.HomeAction{
		{ID: "status.open", Label: "Status", Description: "Inspect runtime status.", Command: "/status", Enabled: true},
		{ID: "settings.open", Label: "Settings", Description: "Open shared settings.", Command: "/settings", Enabled: true},
		{ID: "doctor.open", Label: "Doctor", Description: "Run readiness diagnostics.", Command: "/doctor", Enabled: true},
	}
	seen := map[string]bool{}
	for _, action := range actions {
		seen[action.ID] = true
	}
	for _, action := range doctor.Actions {
		id := strings.TrimSpace(action.ID)
		command := strings.TrimSpace(action.Command)
		if id == "" || command == "" || seen[id] {
			continue
		}
		seen[id] = true
		actions = append(actions, appviewmodel.HomeAction{
			ID:          id,
			Label:       strings.TrimSpace(firstNonEmpty(action.Label, action.ID)),
			Description: strings.TrimSpace(action.Description),
			Command:     command,
			Enabled:     action.Enabled,
		})
	}
	return actions
}
