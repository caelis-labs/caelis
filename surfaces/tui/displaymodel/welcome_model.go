package displaymodel

import (
	"cmp"
	"os"
	"strings"
)

type WelcomeViewModel struct {
	VersionLabel string
	Workspace    string
	ModelAlias   string
}

func BuildWelcomeViewModel(version, workspace, modelName string) WelcomeViewModel {
	versionText := cmp.Or(strings.TrimSpace(version), "unknown")
	versionLabel := versionText
	if !strings.HasPrefix(strings.ToLower(versionText), "v") {
		versionLabel = "v" + versionText
	}

	workspace = cmp.Or(strings.TrimSpace(workspace), ".")
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		workspace = strings.Replace(workspace, home, "~", 1)
	}

	return WelcomeViewModel{
		VersionLabel: versionLabel,
		Workspace:    workspace,
		ModelAlias:   cmp.Or(strings.TrimSpace(modelName), "not configured (/connect)"),
	}
}
