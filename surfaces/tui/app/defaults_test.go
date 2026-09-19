package tuiapp

import (
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func TestDefaultCommandsExposePlatformCoreCommands(t *testing.T) {
	got := controlprompt.DefaultNamesForPlatform("linux")
	want := []string{
		"help",
		"review",
		"breeze",
		"orbit",
		"zenith",
		"connect",
		"disconnect",
		"team",
		"plugin",
		"model",
		"status",
		"new",
		"resume",
		"compact",
		"quit",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultNamesForPlatform(linux) = %#v, want %#v", got, want)
	}

	windows := controlprompt.DefaultNamesForPlatform("windows")
	if !sliceContainsString(windows, "doctor") {
		t.Fatalf("DefaultNamesForPlatform(windows) = %#v, want doctor", windows)
	}
	if sliceContainsString(got, "doctor") {
		t.Fatalf("DefaultNamesForPlatform(linux) = %#v, should hide doctor", got)
	}
}

func TestDefaultWizardsExposeConfigurationFlows(t *testing.T) {
	wizards := DefaultWizards()
	if len(wizards) != 3 {
		t.Fatalf("DefaultWizards() count = %d, want connect, disconnect and plugin", len(wizards))
	}
	if wizards[0].Command != "connect" || wizards[1].Command != "disconnect" || wizards[2].Command != "plugin" {
		t.Fatalf("DefaultWizards() = %#v, want connect, disconnect then plugin", wizards)
	}
}

func TestDefaultConnectWizardSeparatesModelAndACPConnectionSteps(t *testing.T) {
	wizards := DefaultWizards()
	var connect *WizardDef
	for i := range wizards {
		if wizards[i].Command == "connect" {
			connect = &wizards[i]
			break
		}
	}
	if connect == nil {
		t.Fatalf("connect wizard not found")
		return
	}
	if connect.DisplayLine != "/connect" {
		t.Fatalf("DisplayLine = %q, want /connect", connect.DisplayLine)
	}
	if got := len(connect.Steps); got != 1 || connect.Steps[0].Key != "source" || connect.Branch == nil {
		t.Fatalf("connect root wizard = %#v, want one explicit branching step", connect)
	}
	for _, source := range []string{"account", "api-key", "acp"} {
		branch := connect.Branch("source", source, nil, map[string]string{})
		if branch == nil || len(branch.Steps) == 0 {
			t.Fatalf("/connect %s branch = %#v", source, branch)
		}
		if source != "acp" {
			want := "connect-provider-" + source
			if got := branch.Steps[0].CompletionCommand(nil); got != want {
				t.Fatalf("/connect %s provider command = %q, want %q", source, got, want)
			}
		}
	}
	tests := []struct {
		name string
		def  WizardDef
		want []string
	}{
		{name: "account", def: connectModelWizard("account"), want: []string{"provider", "endpoint", "baseurl", "apikey", "model", "image_input", "context_window_tokens", "max_output_tokens", "reasoning_levels"}},
		{name: "api-key", def: connectModelWizard("api-key"), want: []string{"provider", "endpoint", "baseurl", "apikey", "model", "image_input", "context_window_tokens", "max_output_tokens", "reasoning_levels"}},
		{name: "acp", def: connectACPWizard(), want: []string{"acp_agent", "acp_launcher", "acp_command", "acp_model"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys := make([]string, 0, len(tt.def.Steps))
			for _, step := range tt.def.Steps {
				keys = append(keys, step.Key)
			}
			if !reflect.DeepEqual(keys, tt.want) {
				t.Fatalf("steps = %#v, want %#v", keys, tt.want)
			}
		})
	}
}

func TestDefaultDisconnectWizardSeparatesProviderAndACP(t *testing.T) {
	disconnect := disconnectWizard()
	if len(disconnect.Steps) != 1 || disconnect.Steps[0].Key != "kind" || disconnect.Branch == nil {
		t.Fatalf("disconnect root wizard = %#v", disconnect)
	}
	provider := disconnect.Branch("kind", "provider", nil, map[string]string{})
	if provider == nil || len(provider.Steps) != 1 || provider.Steps[0].Key != "provider_model" {
		t.Fatalf("provider disconnect branch = %#v", provider)
	}
	if got := provider.BuildExecLine(map[string]string{"provider_model": "ollama/qwen3"}); got != "/disconnect provider ollama/qwen3" {
		t.Fatalf("provider disconnect exec line = %q", got)
	}
	acp := disconnect.Branch("kind", "acp", nil, map[string]string{})
	if acp == nil || len(acp.Steps) != 1 || acp.Steps[0].Key != "disconnect_agent" {
		t.Fatalf("ACP disconnect branch = %#v", acp)
	}
	if got := acp.BuildExecLine(map[string]string{"disconnect_agent": "codex"}); got != "/disconnect acp codex" {
		t.Fatalf("ACP disconnect exec line = %q", got)
	}
}

func TestDefaultCommandsHideBTWFromDefaultTUI(t *testing.T) {
	for _, command := range DefaultCommands() {
		if command == "btw" {
			t.Fatalf("DefaultCommands() unexpectedly includes hidden command %q", command)
		}
	}
}

func sliceContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
