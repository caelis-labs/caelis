package tuiapp

import (
	"strings"

	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

func (m *Model) currentHomeView() appviewmodel.HomeView {
	if m != nil && m.cfg.RefreshHomeView != nil {
		home := m.cfg.RefreshHomeView()
		if home.AppName != "" || home.VersionLabel != "" || home.Workspace != "" || home.ModelAlias != "" {
			return normalizeHomeView(home)
		}
	}
	appName := "CAELIS"
	version := ""
	workspace := ""
	modelName := "not configured (/connect)"
	mode := ""
	if m != nil {
		appName = firstNonEmpty(strings.TrimSpace(m.cfg.AppName), appName)
		version = strings.TrimSpace(m.cfg.Version)
		workspace = strings.TrimSpace(m.cfg.Workspace)
		modelName = m.currentWelcomeModelName()
		mode = strings.TrimSpace(m.modeLabel())
	}
	return normalizeHomeView(appviewmodel.HomeView{
		AppName:        appName,
		Version:        version,
		VersionLabel:   fallbackHomeVersionLabel(version),
		Workspace:      workspace,
		WorkspaceLabel: firstNonEmpty(workspace, "."),
		ModelAlias:     modelName,
		Mode:           mode,
	})
}

func normalizeHomeView(home appviewmodel.HomeView) appviewmodel.HomeView {
	home.AppName = firstNonEmpty(strings.TrimSpace(home.AppName), "CAELIS")
	home.Version = strings.TrimSpace(home.Version)
	home.VersionLabel = firstNonEmpty(strings.TrimSpace(home.VersionLabel), fallbackHomeVersionLabel(home.Version))
	home.Workspace = firstNonEmpty(strings.TrimSpace(home.Workspace), strings.TrimSpace(home.WorkspaceLabel), ".")
	home.WorkspaceLabel = firstNonEmpty(strings.TrimSpace(home.WorkspaceLabel), home.Workspace)
	home.ModelAlias = firstNonEmpty(strings.TrimSpace(home.ModelAlias), "not configured (/connect)")
	home.Mode = strings.TrimSpace(home.Mode)
	return home
}

func fallbackHomeVersionLabel(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		version = "unknown"
	}
	if strings.HasPrefix(strings.ToLower(version), "v") {
		return version
	}
	return "v" + version
}

func cloneHomeView(home appviewmodel.HomeView) appviewmodel.HomeView {
	home.Diagnostics = append([]appviewmodel.HomeDiagnostic(nil), home.Diagnostics...)
	for i := range home.Diagnostics {
		home.Diagnostics[i].ActionIDs = append([]string(nil), home.Diagnostics[i].ActionIDs...)
		if home.Diagnostics[i].Meta != nil {
			meta := make(map[string]string, len(home.Diagnostics[i].Meta))
			for key, value := range home.Diagnostics[i].Meta {
				meta[key] = value
			}
			home.Diagnostics[i].Meta = meta
		}
	}
	home.Actions = append([]appviewmodel.HomeAction(nil), home.Actions...)
	home.Commands = append([]appviewmodel.CommandView(nil), home.Commands...)
	return normalizeHomeView(home)
}

func sameWelcomeHome(left appviewmodel.HomeView, right appviewmodel.HomeView) bool {
	left = normalizeHomeView(left)
	right = normalizeHomeView(right)
	if left.AppName != right.AppName ||
		left.VersionLabel != right.VersionLabel ||
		left.WorkspaceLabel != right.WorkspaceLabel ||
		left.ModelAlias != right.ModelAlias ||
		left.Mode != right.Mode ||
		len(left.Diagnostics) != len(right.Diagnostics) {
		return false
	}
	for i := range left.Diagnostics {
		if left.Diagnostics[i].Severity != right.Diagnostics[i].Severity ||
			left.Diagnostics[i].Kind != right.Diagnostics[i].Kind ||
			left.Diagnostics[i].Message != right.Diagnostics[i].Message {
			return false
		}
	}
	return true
}
